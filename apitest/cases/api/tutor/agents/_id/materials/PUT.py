"""PUT /api/tutor/agents/{id}/materials 全量替换材料绑定：幂等、跨班 404 与 agent_materials 表核对。"""
import json
import time

from lib.common import STUDENT, TEACHER, TAG, Check, finish, make_result, sql_str


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("PUT", "/api/tutor/agents/{id}/materials")

    teacher = ctx.login(TEACHER)
    cls = int(ctx.db_one("SELECT class_id FROM users WHERE username = " + sql_str(TEACHER[0]))[0])
    cls_handouts = [int(r[0]) for r in ctx.db(f"SELECT id FROM handouts WHERE class_id = {cls} ORDER BY id")]
    other_handout = ctx.db_one(f"SELECT id FROM handouts WHERE class_id <> {cls} LIMIT 1")

    st, _, body = ctx.http("POST", "/api/tutor/agents", token=teacher,
                           json_body={"name": f"材料用{TAG}", "systemPrompt": ""})
    if st != 201:
        checks.append(Check("前置：创建临时智能体", False, f"HTTP {st} {body[:160]!r}"))
        return finish(res, checks, started)
    aid = json.loads(body)["id"]

    try:
        # 未登录 401 / 学生 403
        st, _, _ = ctx.http("PUT", f"/api/tutor/agents/{aid}/materials", json_body={"handoutIds": cls_handouts[:1]})
        checks.append(Check("未登录返回 401", st == 401, f"实际 {st}"))
        st, _, _ = ctx.http("PUT", f"/api/tutor/agents/{aid}/materials", token=ctx.login(STUDENT),
                            json_body={"handoutIds": cls_handouts[:1]})
        checks.append(Check("学生返回 403", st == 403, f"实际 {st}"))

        # 全量替换：集合精确等于入参
        pick = cls_handouts[:2] if len(cls_handouts) >= 2 else cls_handouts[:1]
        st, _, _ = ctx.http("PUT", f"/api/tutor/agents/{aid}/materials", token=teacher, json_body={"handoutIds": pick})
        rowset = {int(r[0]) for r in ctx.db(f"SELECT handout_id FROM agent_materials WHERE agent_id = {aid}")}
        checks.append(Check("替换成功且绑定集合精确等于入参",
                            st == 200 and rowset == set(pick), f"status={st} db={sorted(rowset)} pick={pick}"))

        # 幂等
        ctx.http("PUT", f"/api/tutor/agents/{aid}/materials", token=teacher, json_body={"handoutIds": pick})
        rows = ctx.db(f"SELECT handout_id FROM agent_materials WHERE agent_id = {aid}")
        checks.append(Check("重复替换幂等无重复行", len(rows) == len(pick), f"行数={len(rows)} pick={len(pick)}"))

        # 空列表清空绑定
        st, _, _ = ctx.http("PUT", f"/api/tutor/agents/{aid}/materials", token=teacher, json_body={"handoutIds": []})
        empty = ctx.db_one(f"SELECT handout_id FROM agent_materials WHERE agent_id = {aid}") is None
        checks.append(Check("空列表清空绑定", st == 200 and empty, f"status={st}"))

        # 含跨班材料 id → 404 且绑定不变
        if other_handout:
            before = {int(r[0]) for r in ctx.db(f"SELECT handout_id FROM agent_materials WHERE agent_id = {aid}")}
            st, _, _ = ctx.http("PUT", f"/api/tutor/agents/{aid}/materials", token=teacher,
                                json_body={"handoutIds": [int(other_handout[0])]})
            after = {int(r[0]) for r in ctx.db(f"SELECT handout_id FROM agent_materials WHERE agent_id = {aid}")}
            checks.append(Check("含跨班材料 id 返回 404 且绑定不变",
                                st == 404 and after == before, f"status={st} before={sorted(before)} after={sorted(after)}"))

        # 智能体不存在 404
        st, _, _ = ctx.http("PUT", "/api/tutor/agents/99999999/materials", token=teacher, json_body={"handoutIds": []})
        checks.append(Check("智能体不存在返回 404", st == 404, f"实际 {st}"))
    finally:
        ctx.db(f"DELETE FROM agent_skills WHERE agent_id = {aid}")
        ctx.db(f"DELETE FROM agent_materials WHERE agent_id = {aid}")
        ctx.db(f"DELETE FROM agents WHERE id = {aid}")
    return finish(res, checks, started)
