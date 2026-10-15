"""GET /api/tutor/mcp/servers 列表：权限控制、令牌永不回传、与 mcp_servers 表一致。"""
import json
import time

from lib.common import STUDENT, TEACHER, Check, cfg, finish, make_result, sql_str

TOKEN_RAW = cfg("MCP_AUTH_TOKEN")


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("GET", "/api/tutor/mcp/servers")

    # 未登录 401 / 学生 403
    st, _, _ = ctx.http("GET", "/api/tutor/mcp/servers")
    checks.append(Check("未登录返回 401", st == 401, f"实际 {st}"))
    st, _, _ = ctx.http("GET", "/api/tutor/mcp/servers", token=ctx.login(STUDENT))
    checks.append(Check("学生返回 403", st == 403, f"实际 {st}"))

    teacher = ctx.login(TEACHER)
    st, _, body = ctx.http("GET", "/api/tutor/mcp/servers", token=teacher)
    checks.append(Check("教师返回 200", st == 200, f"实际 {st} body={body[:200]!r}"))
    if st != 200:
        return finish(res, checks, started)

    text = body.decode("utf-8", "replace")
    servers = json.loads(text)
    checks.append(Check("响应为数组", isinstance(servers, list), f"type={type(servers).__name__}"))

    # 响应结构：只回 hasAuth 布尔，绝不出现令牌键或令牌原文
    checks.append(Check("响应不含 authToken 键", "authToken" not in text))
    checks.append(Check("响应含 hasAuth 布尔字段", "hasAuth" in text))
    if TOKEN_RAW:
        checks.append(Check("响应不含令牌原文", TOKEN_RAW not in text, "命中即视为泄漏"))

    me = ctx.db_one("SELECT id, class_id FROM users WHERE username = " + sql_str(TEACHER[0]))
    my_class = int(me[1]) if me else -1

    # 播种的 campus-tools 必须存在，且响应字段与 mcp_servers 行一致
    seeded = next((s for s in servers if s.get("name") == "campus-tools"), None)
    checks.append(Check("列表含预置 campus-tools", seeded is not None,
                        f"names={[s.get('name') for s in servers]}"))
    if seeded:
        checks.append(Check("预置服务器 baseUrl 与部署一致",
                            seeded.get("baseUrl") == "http://mcp-demo:8090", f"resp={seeded}"))
        checks.append(Check("预置服务器 hasAuth=true（播种注入了令牌）",
                            seeded.get("hasAuth") is True, f"resp={seeded}"))
        row = ctx.db_one(
            f"SELECT class_id, base_url, connected, auth_token FROM mcp_servers WHERE id = {seeded['id']}")
        checks.append(Check("mcp_servers 行存在且属于教师本班",
                            row is not None and int(row[0]) == my_class, f"db={row} class={my_class}"))
        if row:
            checks.append(Check("DB base_url 与响应一致",
                                row[1] == seeded.get("baseUrl"), f"db={row[1]} resp={seeded.get('baseUrl')}"))
            checks.append(Check("响应 connected 与 DB connected 一致",
                                bool(seeded.get("connected")) == (row[2] == "1"),
                                f"db={row[2]} resp={seeded.get('connected')}"))
            checks.append(Check("DB 令牌列有值（hasAuth 的依据）",
                                (row[3] not in ("NULL", "")) == bool(seeded.get("hasAuth")),
                                f"db_token_set={row[3] not in ('NULL', '')}"))

    # 列表条目全部属于教师本班（跨班服务器不得出现）
    ids = [s["id"] for s in servers if isinstance(s.get("id"), int)]
    if ids:
        rows = ctx.db(
            f"SELECT count(*) FROM mcp_servers WHERE class_id = {my_class} "
            f"AND id IN ({','.join(str(i) for i in ids)})")
        hit = int(rows[0][0]) if rows else 0
        checks.append(Check("列表条目全部属于教师本班", hit == len(ids), f"本班命中 {hit}/{len(ids)}"))
    return finish(res, checks, started)
