"""POST /api/tutor/mcp/servers 登记服务器：权限、入参校验、同班重名 400、令牌入库但不回传。"""
import json
import time

from lib.common import STUDENT, TEACHER, TAG, Check, finish, make_result, sql_str

NAME = f"it-srv-{TAG}"
BASE_URL = "http://mcp-demo:8090"
TOKEN = f"it-token-{TAG}"


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("POST", "/api/tutor/mcp/servers")
    teacher = ctx.login(TEACHER)
    sid = None

    # 未登录 401 / 学生 403
    st, _, _ = ctx.http("POST", "/api/tutor/mcp/servers",
                        json_body={"name": NAME, "baseUrl": BASE_URL, "authToken": TOKEN})
    checks.append(Check("未登录返回 401", st == 401, f"实际 {st}"))
    st, _, _ = ctx.http("POST", "/api/tutor/mcp/servers", token=ctx.login(STUDENT),
                        json_body={"name": NAME, "baseUrl": BASE_URL, "authToken": TOKEN})
    checks.append(Check("学生返回 403", st == 403, f"实际 {st}"))

    # 入参校验：空名称 / 非法名称 / 非法地址均 400
    st, _, _ = ctx.http("POST", "/api/tutor/mcp/servers", token=teacher,
                        json_body={"name": " ", "baseUrl": BASE_URL})
    checks.append(Check("空名称返回 400", st == 400, f"实际 {st}"))
    st, _, _ = ctx.http("POST", "/api/tutor/mcp/servers", token=teacher,
                        json_body={"name": "Bad Name", "baseUrl": BASE_URL})
    checks.append(Check("非法名称（大写/空格）返回 400", st == 400, f"实际 {st}"))
    st, _, _ = ctx.http("POST", "/api/tutor/mcp/servers", token=teacher,
                        json_body={"name": NAME, "baseUrl": "not-a-url"})
    checks.append(Check("非法 baseUrl 返回 400", st == 400, f"实际 {st}"))

    me = ctx.db_one("SELECT class_id FROM users WHERE username = " + sql_str(TEACHER[0]))

    # 正常登记 201
    st, _, body = ctx.http("POST", "/api/tutor/mcp/servers", token=teacher,
                           json_body={"name": NAME, "baseUrl": BASE_URL, "authToken": TOKEN})
    checks.append(Check("教师登记返回 201", st == 201, f"实际 {st} body={body[:200]!r}"))
    if st == 201:
        created = json.loads(body)
        sid = created.get("id")
        text = body.decode("utf-8", "replace")
        checks.append(Check("响应字段符合预期（hasAuth=true/未连接/空清单）",
                            created.get("name") == NAME and created.get("baseUrl") == BASE_URL
                            and created.get("hasAuth") is True and created.get("connected") is False
                            and created.get("tools") == [], f"resp={created}"))
        checks.append(Check("响应不含 authToken 键", "authToken" not in text))
        checks.append(Check("响应不含令牌原文", TOKEN not in text))

        # mcp_servers 表：班级取自 JWT，令牌落库但只由 hasAuth 暴露
        row = ctx.db_one(
            f"SELECT class_id, name, base_url, auth_token, connected, tool_cache "
            f"FROM mcp_servers WHERE id = {sid}") if sid else None
        checks.append(Check("mcp_servers 表存在该行", row is not None))
        if row and me:
            checks.append(Check("DB 行与本班/入参一致（含令牌已入库）",
                                int(row[0]) == int(me[0]) and row[1] == NAME and row[2] == BASE_URL
                                and row[3] == TOKEN and row[4] == "0" and row[5] == "NULL",
                                f"db={row} class={me[0]}"))

        # 同班重名 400
        st, _, _ = ctx.http("POST", "/api/tutor/mcp/servers", token=teacher,
                            json_body={"name": NAME, "baseUrl": BASE_URL})
        checks.append(Check("同班重名返回 400", st == 400, f"实际 {st}"))
        dup = ctx.db_one(
            f"SELECT count(*) FROM mcp_servers WHERE class_id = {int(me[0])} AND name = {sql_str(NAME)}")
        checks.append(Check("重名被拒后未产生重复行", dup is not None and dup[0] == "1", f"db={dup}"))

    # 清理：走 DELETE 删除登记行
    if sid:
        st, _, _ = ctx.http("DELETE", f"/api/tutor/mcp/servers/{sid}", token=teacher)
        gone = ctx.db_one(f"SELECT id FROM mcp_servers WHERE id = {sid}") is None
        checks.append(Check("清理：登记行已删除", st == 200 and gone, f"status={st} 残留={not gone}"))
    return finish(res, checks, started)
