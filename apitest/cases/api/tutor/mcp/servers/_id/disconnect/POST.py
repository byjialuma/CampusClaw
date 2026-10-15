"""POST /api/tutor/mcp/servers/{id}/disconnect 断开：权限、跨班 404、连接状态与清单被清空。"""
import json
import time

from lib.common import STUDENT, TEACHER, TAG, Check, cfg, finish, make_result, sql_str

NAME = f"it-disc-{TAG}"
BASE_URL = "http://mcp-demo:8090"
TOKEN = cfg("MCP_AUTH_TOKEN")


def other_class_server(ctx, my_class):
    rows = ctx.db(f"SELECT id FROM mcp_servers WHERE class_id <> {my_class} ORDER BY id LIMIT 1")
    return int(rows[0][0]) if rows else None


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("POST", "/api/tutor/mcp/servers/{id}/disconnect")
    teacher = ctx.login(TEACHER)
    me = ctx.db_one("SELECT class_id FROM users WHERE username = " + sql_str(TEACHER[0]))
    my_class = int(me[0]) if me else -1
    OTHER = other_class_server(ctx, my_class)
    sid = None

    st, _, _ = ctx.http("POST", "/api/tutor/mcp/servers/1/disconnect")
    checks.append(Check("未登录返回 401", st == 401, f"实际 {st}"))
    st, _, _ = ctx.http("POST", "/api/tutor/mcp/servers/1/disconnect", token=ctx.login(STUDENT))
    checks.append(Check("学生返回 403", st == 403, f"实际 {st}"))

    st, _, _ = ctx.http("POST", "/api/tutor/mcp/servers/999999999/disconnect", token=teacher)
    checks.append(Check("不存在 id 返回 404", st == 404, f"实际 {st}"))
    if OTHER:
        st, _, _ = ctx.http("POST", f"/api/tutor/mcp/servers/{OTHER}/disconnect", token=teacher)
        checks.append(Check(f"跨班 id({OTHER}) 返回 404", st == 404, f"实际 {st}"))

    st, _, body = ctx.http("POST", "/api/tutor/mcp/servers", token=teacher,
                           json_body={"name": NAME, "baseUrl": BASE_URL, "authToken": TOKEN})
    checks.append(Check("登记测试服务器返回 201", st == 201, f"实际 {st} body={body[:120]!r}"))
    if st == 201:
        sid = json.loads(body)["id"]
        st, _, _ = ctx.http("POST", f"/api/tutor/mcp/servers/{sid}/connect", token=teacher)
        checks.append(Check("前置：连接成功返回 200", st == 200, f"实际 {st}"))
        pre = ctx.db_one(f"SELECT connected, tool_cache FROM mcp_servers WHERE id = {sid}")
        checks.append(Check("前置：DB 已连接且清单非空",
                            pre is not None and pre[0] == "1" and pre[1] != "NULL", f"db={pre}"))

        st, _, body = ctx.http("POST", f"/api/tutor/mcp/servers/{sid}/disconnect", token=teacher)
        checks.append(Check("断开返回 200", st == 200, f"实际 {st} body={body[:160]!r}"))
        if st == 200:
            srv = json.loads(body.decode("utf-8", "replace"))
            checks.append(Check("响应 connected=false 且清单为空",
                                srv.get("connected") is False and srv.get("tools") == [], f"resp={srv}"))
            checks.append(Check("响应不含令牌原文", not TOKEN or TOKEN not in body.decode("utf-8", "replace")))
        row = ctx.db_one(f"SELECT connected, tool_cache, last_error FROM mcp_servers WHERE id = {sid}")
        checks.append(Check("DB connected=0 且清单已清空",
                            row is not None and row[0] == "0" and row[1] == "NULL", f"db={row}"))

        # 重复断开仍 200（幂等）
        st, _, _ = ctx.http("POST", f"/api/tutor/mcp/servers/{sid}/disconnect", token=teacher)
        checks.append(Check("重复断开返回 200（幂等）", st == 200, f"实际 {st}"))

    if sid:
        st, _, _ = ctx.http("DELETE", f"/api/tutor/mcp/servers/{sid}", token=teacher)
        gone = ctx.db_one(f"SELECT id FROM mcp_servers WHERE id = {sid}") is None
        checks.append(Check("清理：服务器已删除", st == 200 and gone, f"status={st}"))
    return finish(res, checks, started)
