"""PUT /api/tutor/mcp/servers/{id} 修改：权限、跨班 404、重名 400、令牌保留/覆盖、变更后重置连接。"""
import json
import time

from lib.common import STUDENT, TEACHER, TAG, Check, cfg, finish, make_result, sql_str

NAME = f"it-put-{TAG}"
NAME2 = f"it-put2-{TAG}"
BASE_URL = "http://mcp-demo:8090"
TOKEN = f"it-token-{TAG}"
MCP_AUTH = cfg("MCP_AUTH_TOKEN")


def other_class_server(ctx, my_class):
    """取一台其他班级的服务器 id（用于跨班 404 校验）。"""
    rows = ctx.db(f"SELECT id FROM mcp_servers WHERE class_id <> {my_class} ORDER BY id LIMIT 1")
    return int(rows[0][0]) if rows else None


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("PUT", "/api/tutor/mcp/servers/{id}")
    teacher = ctx.login(TEACHER)
    me = ctx.db_one("SELECT class_id FROM users WHERE username = " + sql_str(TEACHER[0]))
    my_class = int(me[0]) if me else -1

    created = []
    sid = None
    OTHER = other_class_server(ctx, my_class)

    st, _, _ = ctx.http("PUT", "/api/tutor/mcp/servers/1", json_body={"name": NAME, "baseUrl": BASE_URL})
    checks.append(Check("未登录返回 401", st == 401, f"实际 {st}"))
    st, _, _ = ctx.http("PUT", "/api/tutor/mcp/servers/1", token=ctx.login(STUDENT),
                        json_body={"name": NAME, "baseUrl": BASE_URL})
    checks.append(Check("学生返回 403", st == 403, f"实际 {st}"))

    # 登记两台服务器用于改名/重名测试
    for nm in (NAME, NAME2):
        st, _, body = ctx.http("POST", "/api/tutor/mcp/servers", token=teacher,
                               json_body={"name": nm, "baseUrl": BASE_URL, "authToken": TOKEN})
        checks.append(Check(f"登记 {nm} 返回 201", st == 201, f"实际 {st} body={body[:120]!r}"))
        if st == 201:
            d = json.loads(body)
            created.append(d)
            if nm == NAME:
                sid = d["id"]

    # 不存在 / 跨班 均 404
    st, _, _ = ctx.http("PUT", "/api/tutor/mcp/servers/999999999", token=teacher,
                        json_body={"name": NAME, "baseUrl": BASE_URL})
    checks.append(Check("不存在 id 返回 404", st == 404, f"实际 {st}"))
    if OTHER:
        st, _, _ = ctx.http("PUT", f"/api/tutor/mcp/servers/{OTHER}", token=teacher,
                            json_body={"name": NAME, "baseUrl": BASE_URL})
        checks.append(Check(f"跨班 id({OTHER}) 返回 404", st == 404, f"实际 {st}"))

    # 非法入参 400
    st, _, _ = ctx.http("PUT", f"/api/tutor/mcp/servers/{sid}", token=teacher,
                        json_body={"name": "Bad Name", "baseUrl": BASE_URL})
    checks.append(Check("非法名称返回 400", st == 400, f"实际 {st}"))
    st, _, _ = ctx.http("PUT", f"/api/tutor/mcp/servers/{sid}", token=teacher,
                        json_body={"name": NAME, "baseUrl": "ftp://x"})
    checks.append(Check("非法 baseUrl 返回 400", st == 400, f"实际 {st}"))

    # 重名 400（把一号改为二号的名字）
    if len(created) == 2:
        st, _, _ = ctx.http("PUT", f"/api/tutor/mcp/servers/{created[0]['id']}", token=teacher,
                            json_body={"name": NAME2, "baseUrl": BASE_URL})
        checks.append(Check("改成同班重名返回 400", st == 400, f"实际 {st}"))

    # 令牌缺省：保留原令牌（auth_token 不变、hasAuth 仍 true）
    st, _, body = ctx.http("PUT", f"/api/tutor/mcp/servers/{sid}", token=teacher,
                           json_body={"name": NAME, "baseUrl": BASE_URL})
    checks.append(Check("缺省 authToken 修改返回 200", st == 200, f"实际 {st} body={body[:120]!r}"))
    text = body.decode("utf-8", "replace")
    checks.append(Check("缺省令牌时响应仍 hasAuth=true",
                        json.loads(text).get("hasAuth") is True, f"resp={text[:200]}"))
    checks.append(Check("响应不含 authToken 键与令牌原文",
                        "authToken" not in text and TOKEN not in text))
    row = ctx.db_one(f"SELECT name, base_url, auth_token, connected FROM mcp_servers WHERE id = {sid}")
    checks.append(Check("DB 令牌被保留（未因缺省而清空）",
                        row is not None and row[2] == TOKEN, f"db={row}"))

    # 覆盖为真实令牌（用于连接），连接成功后改回缺省 -> 连接状态与清单必须重置
    if MCP_AUTH:
        st, _, body = ctx.http("PUT", f"/api/tutor/mcp/servers/{sid}", token=teacher,
                              json_body={"name": NAME, "baseUrl": BASE_URL, "authToken": MCP_AUTH})
        checks.append(Check("覆盖令牌修改返回 200", st == 200, f"实际 {st} body={body[:120]!r}"))
        row = ctx.db_one(f"SELECT auth_token FROM mcp_servers WHERE id = {sid}")
        checks.append(Check("覆盖令牌后 DB 令牌为新值", row is not None and row[0] == MCP_AUTH, f"db={row}"))

        st, _, body = ctx.http("POST", f"/api/tutor/mcp/servers/{sid}/connect", token=teacher)
        checks.append(Check("连接成功返回 200（连接能力可用）", st == 200,
                            f"实际 {st} body={body[:160]!r}"))
        st2, _, _ = ctx.http("PUT", f"/api/tutor/mcp/servers/{sid}", token=teacher,
                             json_body={"name": NAME, "baseUrl": BASE_URL})
        checks.append(Check("修改返回 200", st2 == 200, f"实际 {st2}"))
        row = ctx.db_one(f"SELECT connected, tool_cache FROM mcp_servers WHERE id = {sid}")
        checks.append(Check("修改后连接状态重置为 0 且清单清空",
                            row is not None and row[0] == "0" and row[1] == "NULL", f"db={row}"))

    # 显式空令牌：清除令牌（hasAuth=false）
    st, _, body = ctx.http("PUT", f"/api/tutor/mcp/servers/{sid}", token=teacher,
                           json_body={"name": NAME, "baseUrl": BASE_URL, "authToken": ""})
    checks.append(Check("显式空 authToken 修改返回 200", st == 200, f"实际 {st}"))
    row = ctx.db_one(f"SELECT auth_token FROM mcp_servers WHERE id = {sid}")
    checks.append(Check("显式空令牌后 DB 令牌被清空", row is not None and row[0] == "NULL", f"db={row}"))
    checks.append(Check("显式空令牌后响应 hasAuth=false",
                        json.loads(body.decode("utf-8", "replace")).get("hasAuth") is False,
                        f"resp={body[:200]!r}"))

    # 清理
    for d in created:
        st, _, _ = ctx.http("DELETE", f"/api/tutor/mcp/servers/{d['id']}", token=teacher)
        gone = ctx.db_one(f"SELECT id FROM mcp_servers WHERE id = {d['id']}") is None
        checks.append(Check(f"清理：{d['name']} 已删除", st == 200 and gone, f"status={st} 残留={not gone}"))
    return finish(res, checks, started)
