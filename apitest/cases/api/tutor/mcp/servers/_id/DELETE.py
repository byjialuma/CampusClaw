"""DELETE /api/tutor/mcp/servers/{id} 删除：权限、跨班 404、行删除，及审计行 server_id 置 NULL 保留快照。"""
import json
import time

from lib.common import STUDENT, TEACHER, TAG, Check, finish, make_result, sql_str

NAME = f"it-del-{TAG}"
BASE_URL = "http://mcp-demo:8090"
MARK = f"it-del-ref-{TAG}"


def other_class_server(ctx, my_class):
    rows = ctx.db(f"SELECT id FROM mcp_servers WHERE class_id <> {my_class} ORDER BY id LIMIT 1")
    return int(rows[0][0]) if rows else None


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("DELETE", "/api/tutor/mcp/servers/{id}")
    teacher = ctx.login(TEACHER)
    me = ctx.db_one("SELECT id, class_id FROM users WHERE username = " + sql_str(TEACHER[0]))
    my_uid, my_class = (int(me[0]), int(me[1])) if me else (0, -1)
    OTHER = other_class_server(ctx, my_class)

    st, _, _ = ctx.http("DELETE", "/api/tutor/mcp/servers/1")
    checks.append(Check("未登录返回 401", st == 401, f"实际 {st}"))
    st, _, _ = ctx.http("DELETE", "/api/tutor/mcp/servers/1", token=ctx.login(STUDENT))
    checks.append(Check("学生返回 403", st == 403, f"实际 {st}"))

    st, _, _ = ctx.http("DELETE", "/api/tutor/mcp/servers/999999999", token=teacher)
    checks.append(Check("不存在 id 返回 404", st == 404, f"实际 {st}"))
    if OTHER:
        st, _, _ = ctx.http("DELETE", f"/api/tutor/mcp/servers/{OTHER}", token=teacher)
        checks.append(Check(f"跨班 id({OTHER}) 返回 404（不越权删除）", st == 404, f"实际 {st}"))
        alive = ctx.db_one(f"SELECT id FROM mcp_servers WHERE id = {OTHER}")
        checks.append(Check("跨班服务器未被删除", alive is not None))

    # 登记一台服务器，并写入一条引用它的审计行
    st, _, body = ctx.http("POST", "/api/tutor/mcp/servers", token=teacher,
                           json_body={"name": NAME, "baseUrl": BASE_URL})
    checks.append(Check("登记测试服务器返回 201", st == 201, f"实际 {st} body={body[:120]!r}"))
    sid = json.loads(body).get("id") if st == 201 else None
    call_id = None

    if sid:
        ctx.db(f"INSERT INTO mcp_calls (class_id, user_id, server_id, server_name, tool_ref, status, duration_ms) "
               f"VALUES ({my_class}, {my_uid}, {sid}, {sql_str(NAME)}, {sql_str(MARK)}, 'success', 12)")
        row = ctx.db_one(f"SELECT id FROM mcp_calls WHERE tool_ref = {sql_str(MARK)} ORDER BY id DESC LIMIT 1")
        call_id = int(row[0]) if row else None
        checks.append(Check("测试审计行已写入且引用该服务器", call_id is not None))

        # 删除服务器
        st, _, body = ctx.http("DELETE", f"/api/tutor/mcp/servers/{sid}", token=teacher)
        checks.append(Check("删除本班服务器返回 200", st == 200, f"实际 {st} body={body[:120]!r}"))
        checks.append(Check("响应为 {ok:true}", json.loads(body.decode("utf-8", "replace")).get("ok") is True,
                            f"resp={body[:120]!r}"))
        gone = ctx.db_one(f"SELECT id FROM mcp_servers WHERE id = {sid}") is None
        checks.append(Check("mcp_servers 行已删除", gone))

        # 外键 ON DELETE SET NULL：审计行的 server_id 置 NULL，server_name 快照保留
        if call_id:
            row = ctx.db_one(f"SELECT server_id, server_name, tool_ref, status FROM mcp_calls WHERE id = {call_id}")
            checks.append(Check("审计行保留（未被级联删除）", row is not None, f"db={row}"))
            if row:
                checks.append(Check("审计行 server_id 已置 NULL", row[0] == "NULL", f"db={row}"))
                checks.append(Check("审计行 server_name 快照保留", row[1] == NAME, f"db={row}"))

        # 再次删除同一 id：行已不在，返回 404
        st, _, _ = ctx.http("DELETE", f"/api/tutor/mcp/servers/{sid}", token=teacher)
        checks.append(Check("重复删除返回 404", st == 404, f"实际 {st}"))

    # 清理审计行
    if call_id:
        ctx.db(f"DELETE FROM mcp_calls WHERE id = {call_id}")
        checks.append(Check("清理：审计行已删除",
                            ctx.db_one(f"SELECT id FROM mcp_calls WHERE id = {call_id}") is None))
    if sid:
        ctx.db(f"DELETE FROM mcp_servers WHERE id = {sid}")
    return finish(res, checks, started)
