"""GET /api/tutor/assistant 本班技能库视图：权限裁剪（无提示词）与 skills 表核对。"""
import json
import time

from lib.common import STUDENT, TEACHER, Check, finish, make_result, sql_str


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("GET", "/api/tutor/assistant")

    # 未登录 401 / 学生 403
    st, _, _ = ctx.http("GET", "/api/tutor/assistant")
    checks.append(Check("未登录返回 401", st == 401, f"实际 {st}"))
    st, _, _ = ctx.http("GET", "/api/tutor/assistant", token=ctx.login(STUDENT))
    checks.append(Check("学生返回 403", st == 403, f"实际 {st}"))

    teacher = ctx.login(TEACHER)
    cls = int(ctx.db_one("SELECT class_id FROM users WHERE username = " + sql_str(TEACHER[0]))[0])
    db_ids = {int(r[0]) for r in ctx.db(
        f"SELECT s.id FROM skills s JOIN assistants a ON a.id = s.assistant_id WHERE a.class_id = {cls}")}

    st, _, body = ctx.http("GET", "/api/tutor/assistant", token=teacher)
    checks.append(Check("教师返回 200", st == 200, f"实际 {st} body={body[:160]!r}"))
    if st != 200:
        return finish(res, checks, started)

    data = json.loads(body)
    checks.append(Check("响应不含提示词字段 systemPrompt", "systemPrompt" not in data, f"keys={sorted(data.keys())}"))
    skills = data.get("skills") or []
    checks.append(Check("技能库与 skills 表 id 集合一致",
                        {int(s["id"]) for s in skills} == db_ids, f"resp={len(skills)} db={sorted(db_ids)}"))
    checks.append(Check("每条技能均含 tools 字段（列表或 null）",
                        all("tools" in s and (s["tools"] is None or isinstance(s["tools"], list))
                            for s in skills), f"sample={skills[:1]}"))
    return finish(res, checks, started)
