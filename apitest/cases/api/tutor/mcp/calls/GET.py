"""GET /api/tutor/mcp/calls 审计列表：权限、班级隔离、响应与 mcp_calls 表行一致、令牌不出现。"""
import json
import time

from lib.common import STUDENT, TEACHER, TAG, Check, finish, make_result, sql_str

MARK = f"it-call-ref-{TAG}"
MARK_OTHER = f"it-call-other-{TAG}"


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("GET", "/api/tutor/mcp/calls")
    teacher = ctx.login(TEACHER)
    me = ctx.db_one("SELECT id, class_id FROM users WHERE username = " + sql_str(TEACHER[0]))
    my_uid, my_class = (int(me[0]), int(me[1])) if me else (0, -1)

    st, _, _ = ctx.http("GET", "/api/tutor/mcp/calls")
    checks.append(Check("未登录返回 401", st == 401, f"实际 {st}"))
    st, _, _ = ctx.http("GET", "/api/tutor/mcp/calls", token=ctx.login(STUDENT))
    checks.append(Check("学生返回 403", st == 403, f"实际 {st}"))

    # 造数：本班一行（server_id 为空、有归属用户）+ 他班一行（不得出现在本班列表）
    ctx.db(f"INSERT INTO mcp_calls (class_id, user_id, server_name, tool_ref, status, error, duration_ms) "
           f"VALUES ({my_class}, {my_uid}, {sql_str('campus-tools')}, {sql_str(MARK)}, 'success', NULL, 123)")
    own = ctx.db_one(f"SELECT id FROM mcp_calls WHERE tool_ref = {sql_str(MARK)} ORDER BY id DESC LIMIT 1")
    own_id = int(own[0]) if own else None
    checks.append(Check("测试审计行已写入", own_id is not None))

    other_class = None
    rows = ctx.db(f"SELECT id FROM classes WHERE id <> {my_class} ORDER BY id LIMIT 1")
    if rows:
        other_class = int(rows[0][0])
        ctx.db(f"INSERT INTO mcp_calls (class_id, server_name, tool_ref, status, duration_ms) "
               f"VALUES ({other_class}, {sql_str('campus-tools')}, {sql_str(MARK_OTHER)}, 'denied', 0)")

    st, _, body = ctx.http("GET", "/api/tutor/mcp/calls", token=teacher)
    checks.append(Check("教师返回 200", st == 200, f"实际 {st} body={body[:200]!r}"))
    if st == 200:
        text = body.decode("utf-8", "replace")
        calls = json.loads(text)
        checks.append(Check("响应为数组", isinstance(calls, list), f"type={type(calls).__name__}"))
        checks.append(Check("响应不含 authToken/token 键",
                            "authToken" not in text and "\"token\"" not in text))

        hit = next((c for c in calls if c.get("toolRef") == MARK), None)
        checks.append(Check("本班审计行出现在列表中", hit is not None,
                            f"toolRefs={[c.get('toolRef') for c in calls][:10]}"))
        if hit and own_id:
            row = ctx.db_one(
                f"SELECT server_id, server_name, tool_ref, status, duration_ms, user_id "
                f"FROM mcp_calls WHERE id = {own_id}")
            checks.append(Check("响应行与 mcp_calls 表内容一致",
                                row is not None and row[0] == "NULL" and row[1] == hit.get("serverName")
                                and row[2] == hit.get("toolRef") and row[3] == hit.get("status")
                                and int(row[4]) == int(hit.get("durationMs") or -1),
                                f"db={row} resp={hit}"))
            checks.append(Check("server_id 为空的行按 serverId 缺省返回",
                                hit.get("serverId") in (None, 0) or "serverId" not in hit, f"resp={hit}"))
            checks.append(Check("响应带归属用户名快照", bool(hit.get("username")), f"resp={hit}"))

        checks.append(Check("他班审计行不出现在本班列表",
                            all(c.get("toolRef") != MARK_OTHER for c in calls)))
        if other_class:
            cnt = ctx.db_one(f"SELECT count(*) FROM mcp_calls WHERE class_id = {other_class} "
                             f"AND tool_ref = {sql_str(MARK_OTHER)}")
            checks.append(Check("他班审计行仍在库中（仅接口做了隔离）",
                                cnt is not None and int(cnt[0]) >= 1, f"db={cnt}"))

    # 清理造数
    if own_id:
        ctx.db(f"DELETE FROM mcp_calls WHERE id = {own_id}")
    if other_class:
        ctx.db(f"DELETE FROM mcp_calls WHERE class_id = {other_class} AND tool_ref = {sql_str(MARK_OTHER)}")
    checks.append(Check("清理：本班造数已删除",
                        own_id is None or ctx.db_one(f"SELECT id FROM mcp_calls WHERE id = {own_id}") is None))
    return finish(res, checks, started)
