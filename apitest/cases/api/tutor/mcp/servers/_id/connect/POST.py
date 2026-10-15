"""POST /api/tutor/mcp/servers/{id}/connect 连接：权限、跨班 404、握手+工具发现、失败 502 且保持未连接。"""
import json
import time

from lib.common import STUDENT, TEACHER, TAG, Check, cfg, finish, make_result, sql_str

GOOD_NAME = f"it-conn-{TAG}"
GOOD_URL = "http://mcp-demo:8090"
BAD_NAME = f"it-conn-bad-{TAG}"
BAD_URL = "http://127.0.0.1:9"  # 容器内无监听端口，必然连不上
TOKEN = cfg("MCP_AUTH_TOKEN")


def other_class_server(ctx, my_class):
    rows = ctx.db(f"SELECT id FROM mcp_servers WHERE class_id <> {my_class} ORDER BY id LIMIT 1")
    return int(rows[0][0]) if rows else None


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("POST", "/api/tutor/mcp/servers/{id}/connect")
    teacher = ctx.login(TEACHER)
    me = ctx.db_one("SELECT class_id FROM users WHERE username = " + sql_str(TEACHER[0]))
    my_class = int(me[0]) if me else -1
    OTHER = other_class_server(ctx, my_class)

    st, _, _ = ctx.http("POST", "/api/tutor/mcp/servers/1/connect")
    checks.append(Check("未登录返回 401", st == 401, f"实际 {st}"))
    st, _, _ = ctx.http("POST", "/api/tutor/mcp/servers/1/connect", token=ctx.login(STUDENT))
    checks.append(Check("学生返回 403", st == 403, f"实际 {st}"))

    st, _, _ = ctx.http("POST", "/api/tutor/mcp/servers/999999999/connect", token=teacher)
    checks.append(Check("不存在 id 返回 404", st == 404, f"实际 {st}"))
    if OTHER:
        st, _, _ = ctx.http("POST", f"/api/tutor/mcp/servers/{OTHER}/connect", token=teacher)
        checks.append(Check(f"跨班 id({OTHER}) 返回 404", st == 404, f"实际 {st}"))

    good_id, bad_id = None, None

    # 正常连接：握手 + tools/list，返回工具清单并写入缓存
    st, _, body = ctx.http("POST", "/api/tutor/mcp/servers", token=teacher,
                           json_body={"name": GOOD_NAME, "baseUrl": GOOD_URL, "authToken": TOKEN})
    checks.append(Check("登记可连接服务器返回 201", st == 201, f"实际 {st} body={body[:120]!r}"))
    if st == 201:
        good_id = json.loads(body)["id"]
        st, _, body = ctx.http("POST", f"/api/tutor/mcp/servers/{good_id}/connect", token=teacher)
        checks.append(Check("连接成功返回 200", st == 200, f"实际 {st} body={body[:200]!r}"))
        if st == 200:
            srv = json.loads(body.decode("utf-8", "replace"))
            names = [t.get("name") for t in srv.get("tools", [])]
            checks.append(Check("响应 connected=true", srv.get("connected") is True, f"resp={srv}"))
            checks.append(Check("发现的工具含 formula_hint 与 unit_convert",
                                "formula_hint" in names and "unit_convert" in names, f"tools={names}"))
            checks.append(Check("工具条目含 description 与 inputSchema",
                                all(t.get("description") and t.get("inputSchema") for t in srv.get("tools", [])),
                                f"tools={srv.get('tools')}"))
            checks.append(Check("响应不含令牌原文", not TOKEN or TOKEN not in body.decode("utf-8", "replace")))
            row = ctx.db_one(
                f"SELECT connected, tool_cache, last_error FROM mcp_servers WHERE id = {good_id}")
            checks.append(Check("DB connected=1 且 last_error 为空",
                                row is not None and row[0] == "1" and row[2] == "NULL", f"db={row}"))
            cache = row[1] if row else ""
            checks.append(Check("DB tool_cache 含两个工具名",
                                "formula_hint" in cache and "unit_convert" in cache, f"cache={cache[:200]}"))

    # 失败连接：不可达地址返回 502，且保持未连接、清单为空
    st, _, body = ctx.http("POST", "/api/tutor/mcp/servers", token=teacher,
                           json_body={"name": BAD_NAME, "baseUrl": BAD_URL})
    checks.append(Check("登记不可达服务器返回 201", st == 201, f"实际 {st} body={body[:120]!r}"))
    if st == 201:
        bad_id = json.loads(body)["id"]
        st, _, body = ctx.http("POST", f"/api/tutor/mcp/servers/{bad_id}/connect", token=teacher)
        checks.append(Check("不可达地址返回 502", st == 502, f"实际 {st} body={body[:160]!r}"))
        checks.append(Check("502 响应含可读提示", b"error" in body, f"body={body[:160]!r}"))
        row = ctx.db_one(
            f"SELECT connected, tool_cache, last_error FROM mcp_servers WHERE id = {bad_id}")
        checks.append(Check("失败后 DB connected=0 且清单为空",
                            row is not None and row[0] == "0" and row[1] == "NULL", f"db={row}"))
        checks.append(Check("失败后记录 last_error", row is not None and row[2] not in (None, "NULL"),
                            f"db={row}"))

    # 清理
    for sid in (good_id, bad_id):
        if sid:
            st, _, _ = ctx.http("DELETE", f"/api/tutor/mcp/servers/{sid}", token=teacher)
            gone = ctx.db_one(f"SELECT id FROM mcp_servers WHERE id = {sid}") is None
            checks.append(Check(f"清理：服务器 {sid} 已删除", st == 200 and gone, f"status={st}"))
    return finish(res, checks, started)
