"""POST /api/sessions 登录：状态码、响应内容、JWT 载荷与 users/classes 表一致性。"""
import json
import re
import time
from datetime import datetime, timezone

from lib.common import TEACHER, Check, finish, jwt_payload, make_result, sql_str


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("POST", "/api/sessions")

    st, _, body = ctx.http("POST", "/api/sessions",
                           json_body={"username": TEACHER[0], "password": TEACHER[1]})
    checks.append(Check("正确凭据返回 200", st == 200, f"实际 {st}"))
    if st != 200:
        return finish(res, checks, started)
    data = json.loads(body)
    checks.append(Check("响应含 accessToken/expiresAt/user",
                        all(k in data for k in ("accessToken", "expiresAt", "user")),
                        str(list(data.keys()))))

    # 响应 user 与 users 表（join classes）一致
    row = ctx.db_one(
        "SELECT u.id, u.display_name, u.role, u.class_id, c.name "
        "FROM users u JOIN classes c ON c.id = u.class_id WHERE u.username = " + sql_str(TEACHER[0]))
    u = data.get("user", {})
    checks.append(Check("users 表存在该账号", row is not None))
    if row:
        same = (u.get("id") == int(row[0]) and u.get("displayName") == row[1]
                and u.get("role") == row[2] and u.get("classId") == int(row[3])
                and u.get("className") == row[4])
        checks.append(Check("user 字段与 users 表一致", same, f"api={u} db={row}"))

    # JWT 载荷可解，sub（若签发）与 users.id 一致
    try:
        payload = jwt_payload(data["accessToken"])
        ok = "sub" not in payload or (row is not None and str(payload["sub"]) == row[0])
        checks.append(Check("JWT sub 与 users.id 一致", ok, f"payload={payload}"))
    except Exception as e:
        checks.append(Check("JWT 可解码", False, str(e)))

    # 过期时间在未来
    try:
        # Go 输出纳秒精度；旧版 Python 的 fromisoformat 仅接受 3/6 位小数，先截断到微秒。
        raw = re.sub(r"(\.\d{6})\d+", r"\1", str(data["expiresAt"]).replace("Z", "+00:00"))
        exp = datetime.fromisoformat(raw)
        checks.append(Check("expiresAt 在未来", exp > datetime.now(timezone.utc), str(data["expiresAt"])))
    except Exception as e:
        checks.append(Check("expiresAt 可解析", False, str(e)))

    # 错误密码 401，且与用户不存在响应完全一致（不泄露账号是否存在）
    st2, _, body2 = ctx.http("POST", "/api/sessions",
                             json_body={"username": TEACHER[0], "password": "wrong-" + TEACHER[1]})
    checks.append(Check("错误密码返回 401", st2 == 401, f"实际 {st2}"))
    st3, _, body3 = ctx.http("POST", "/api/sessions",
                             json_body={"username": "no_such_user_apitest", "password": "x"})
    same = st3 == 401 and json.loads(body3) == json.loads(body2)
    checks.append(Check("用户不存在与密码错误响应一致", same,
                        f"wrong_pw={body2!r} no_user={body3!r}"))

    # 缺字段 400
    st4, _, _ = ctx.http("POST", "/api/sessions", json_body={"username": TEACHER[0]})
    checks.append(Check("缺密码返回 400", st4 == 400, f"实际 {st4}"))
    return finish(res, checks, started)
