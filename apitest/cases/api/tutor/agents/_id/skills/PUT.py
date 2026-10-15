"""PUT /api/tutor/agents/{id}/skills 全量替换技能绑定：幂等、跨班 404 与 agent_skills 表核对。"""
import json
import time

from lib.common import STUDENT, TEACHER, TAG, Check, finish, make_result, sql_str


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("PUT", "/api/tutor/agents/{id}/skills")

    teacher = ctx.login(TEACHER)
    cls = int(ctx.db_one("SELECT class_id FROM users WHERE username = " + sql_str(TEACHER[0]))[0])
    cls_skills = [int(r[0]) for r in ctx.db(
        f"SELECT s.id FROM skills s JOIN assistants a ON a.id = s.assistant_id "
        f"WHERE a.class_id = {cls} ORDER BY s.id")]
    other_skill = ctx.db_one(
        f"SELECT s.id FROM skills s JOIN assistants a ON a.id = s.assistant_id "
        f"WHERE a.class_id <> {cls} LIMIT 1")

    st, _, body = ctx.http("POST", "/api/tutor/agents", token=teacher,
                           json_body={"name": f"绑定用{TAG}", "systemPrompt": ""})
    if st != 201:
        checks.append(Check("前置：创建临时智能体", False, f"HTTP {st} {body[:160]!r}"))
        return finish(res, checks, started)
    aid = json.loads(body)["id"]

    try:
        # 未登录 401 / 学生 403
        st, _, _ = ctx.http("PUT", f"/api/tutor/agents/{aid}/skills", json_body={"skillIds": cls_skills[:1]})
        checks.append(Check("未登录返回 401", st == 401, f"实际 {st}"))
        st, _, _ = ctx.http("PUT", f"/api/tutor/agents/{aid}/skills", token=ctx.login(STUDENT),
                            json_body={"skillIds": cls_skills[:1]})
        checks.append(Check("学生返回 403", st == 403, f"实际 {st}"))

        # 全量替换：集合精确等于入参
        pick = cls_skills[:2] if len(cls_skills) >= 2 else cls_skills[:1]
        st, _, _ = ctx.http("PUT", f"/api/tutor/agents/{aid}/skills", token=teacher, json_body={"skillIds": pick})
        rowset = {int(r[0]) for r in ctx.db(f"SELECT skill_id FROM agent_skills WHERE agent_id = {aid}")}
        checks.append(Check("替换成功且绑定集合精确等于入参",
                            st == 200 and rowset == set(pick), f"status={st} db={sorted(rowset)} pick={pick}"))

        # 幂等：重复替换不产生重复行
        ctx.http("PUT", f"/api/tutor/agents/{aid}/skills", token=teacher, json_body={"skillIds": pick})
        rows = ctx.db(f"SELECT skill_id FROM agent_skills WHERE agent_id = {aid}")
        checks.append(Check("重复替换幂等无重复行", len(rows) == len(pick), f"行数={len(rows)} pick={len(pick)}"))

        # 空列表清空绑定
        st, _, _ = ctx.http("PUT", f"/api/tutor/agents/{aid}/skills", token=teacher, json_body={"skillIds": []})
        empty = ctx.db_one(f"SELECT skill_id FROM agent_skills WHERE agent_id = {aid}") is None
        checks.append(Check("空列表清空绑定", st == 200 and empty, f"status={st}"))

        # 含跨班技能 id → 404 且绑定不变
        if other_skill:
            before = {int(r[0]) for r in ctx.db(f"SELECT skill_id FROM agent_skills WHERE agent_id = {aid}")}
            st, _, _ = ctx.http("PUT", f"/api/tutor/agents/{aid}/skills", token=teacher,
                                json_body={"skillIds": [int(other_skill[0])]})
            after = {int(r[0]) for r in ctx.db(f"SELECT skill_id FROM agent_skills WHERE agent_id = {aid}")}
            checks.append(Check("含跨班技能 id 返回 404 且绑定不变",
                                st == 404 and after == before, f"status={st} before={sorted(before)} after={sorted(after)}"))

        # 智能体不存在 404
        st, _, _ = ctx.http("PUT", "/api/tutor/agents/99999999/skills", token=teacher, json_body={"skillIds": []})
        checks.append(Check("智能体不存在返回 404", st == 404, f"实际 {st}"))
    finally:
        ctx.db(f"DELETE FROM agent_skills WHERE agent_id = {aid}")
        ctx.db(f"DELETE FROM agent_materials WHERE agent_id = {aid}")
        ctx.db(f"DELETE FROM agents WHERE id = {aid}")
    return finish(res, checks, started)
