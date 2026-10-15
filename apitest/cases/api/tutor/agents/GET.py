"""GET /api/tutor/agents 智能体列表：按角色裁剪响应并与 agents 表核对。"""
import json
import time

from lib.common import STUDENT, TEACHER, Check, finish, make_result, sql_str


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("GET", "/api/tutor/agents")

    # 未登录 401
    st, _, _ = ctx.http("GET", "/api/tutor/agents")
    checks.append(Check("未登录返回 401", st == 401, f"实际 {st}"))

    teacher = ctx.login(TEACHER)
    cls = int(ctx.db_one("SELECT class_id FROM users WHERE username = " + sql_str(TEACHER[0]))[0])
    db_ids = {int(r[0]) for r in ctx.db(f"SELECT id FROM agents WHERE class_id = {cls}")}

    # 教师视图：含 systemPrompt 与绑定清单
    st, _, body = ctx.http("GET", "/api/tutor/agents", token=teacher)
    checks.append(Check("教师列表返回 200", st == 200, f"实际 {st}"))
    if st == 200:
        items = json.loads(body)
        checks.append(Check("教师响应为数组且与 agents 表 id 集合一致",
                            isinstance(items, list) and {int(a["id"]) for a in items} == db_ids,
                            f"resp={len(items)} db={sorted(db_ids)}"))
        ok = bool(items) and all(
            ("systemPrompt" in a and isinstance(a.get("skillIds"), list) and isinstance(a.get("handoutIds"), list))
            for a in items
        )
        checks.append(Check("教师视图含 systemPrompt 与 skillIds/handoutIds", ok, f"sample={items[:1]}"))

    # 学生视图：仅 id/name/updatedAt，不含提示词与绑定
    st, _, body = ctx.http("GET", "/api/tutor/agents", token=ctx.login(STUDENT))
    checks.append(Check("学生列表返回 200", st == 200, f"实际 {st}"))
    if st == 200:
        items = json.loads(body)
        checks.append(Check("学生仅见 id/name/updatedAt",
                            all(set(a.keys()) == {"id", "name", "updatedAt"} for a in items),
                            f"sample={items[:1]}"))
        checks.append(Check("学生看到的是本班智能体",
                            {int(a["id"]) for a in items} == db_ids,
                            f"ids={sorted(int(a['id']) for a in items)}"))
    return finish(res, checks, started)
