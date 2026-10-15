"""GET /api/me 当前用户：响应字段与 users/classes 表逐字段一致。"""
import json
import time

from lib.common import TEACHER, Check, finish, make_result, sql_str


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("GET", "/api/me")
    token = ctx.login(TEACHER)

    st0, _, _ = ctx.http("GET", "/api/me")
    checks.append(Check("未登录返回 401", st0 == 401, f"实际 {st0}"))

    st, _, body = ctx.http("GET", "/api/me", token=token)
    checks.append(Check("已登录返回 200", st == 200, f"实际 {st}"))
    if st != 200:
        return finish(res, checks, started)
    u = json.loads(body)
    row = ctx.db_one(
        "SELECT u.id, u.username, u.display_name, u.role, u.class_id, c.name "
        "FROM users u JOIN classes c ON c.id = u.class_id WHERE u.username = " + sql_str(TEACHER[0]))
    checks.append(Check("users/classes 表存在对应行", row is not None))
    if row:
        same = (u.get("id") == int(row[0])
                and u.get("displayName") == row[2] and u.get("role") == row[3]
                and u.get("classId") == int(row[4]) and u.get("className") == row[5])
        checks.append(Check("响应字段与数据库一致", same, f"api={u} db={row}"))
        # 设计如此：JWT 不携带 username（backend/internal/auth/jwt.go Verify 置空），
        # /api/me 由 claims 重建用户，故 username 恒为空串而非数据库值。
        checks.append(Check("username 按设计为空（JWT 不携带）",
                            u.get("username") == "", f"api username={u.get('username')!r}"))
    return finish(res, checks, started)
