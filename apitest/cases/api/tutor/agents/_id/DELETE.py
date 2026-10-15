"""DELETE /api/tutor/agents/{id} 删除智能体：权限、被会话引用 409、跨班 404 与 agents 表核对。"""
import json
import time

from lib.common import STUDENT, TEACHER, TAG, Check, finish, make_result, sql_str


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("DELETE", "/api/tutor/agents/{id}")

    teacher = ctx.login(TEACHER)
    student = ctx.login(STUDENT)
    cls = int(ctx.db_one("SELECT class_id FROM users WHERE username = " + sql_str(TEACHER[0]))[0])
    other = ctx.db_one(f"SELECT id FROM agents WHERE class_id <> {cls} LIMIT 1")

    # 临时智能体
    st, _, body = ctx.http("POST", "/api/tutor/agents", token=teacher,
                           json_body={"name": f"删除用{TAG}", "systemPrompt": ""})
    if st != 201:
        checks.append(Check("前置：创建临时智能体", False, f"HTTP {st} {body[:160]!r}"))
        return finish(res, checks, started)
    aid = json.loads(body)["id"]

    # 先绑一条技能，验证删除时绑定行被级联清理
    skill = ctx.db_one(
        f"SELECT s.id FROM skills s JOIN assistants a ON a.id = s.assistant_id "
        f"WHERE a.class_id = {cls} ORDER BY s.id LIMIT 1")
    if skill:
        ctx.http("PUT", f"/api/tutor/agents/{aid}/skills", token=teacher, json_body={"skillIds": [int(skill[0])]})

    try:
        # 未登录 401 / 学生 403
        st, _, _ = ctx.http("DELETE", f"/api/tutor/agents/{aid}")
        checks.append(Check("未登录返回 401", st == 401, f"实际 {st}"))
        st, _, _ = ctx.http("DELETE", f"/api/tutor/agents/{aid}", token=student)
        checks.append(Check("学生返回 403", st == 403, f"实际 {st}"))

        # 不存在 404 / 跨班 404
        st, _, _ = ctx.http("DELETE", "/api/tutor/agents/99999999", token=teacher)
        checks.append(Check("不存在返回 404", st == 404, f"实际 {st}"))
        if other:
            st, _, _ = ctx.http("DELETE", f"/api/tutor/agents/{other[0]}", token=teacher)
            checks.append(Check("跨班删除返回 404", st == 404, f"实际 {st}"))

        # 被会话引用 → 409
        st, _, cbody = ctx.http("POST", "/api/tutor/conversations", token=student, json_body={"agentId": aid})
        cid = json.loads(cbody).get("id") if st == 201 else None
        checks.append(Check("学生可建绑定该智能体的会话", st == 201, f"实际 {st} {cbody[:120]!r}"))
        st, _, _ = ctx.http("DELETE", f"/api/tutor/agents/{aid}", token=teacher)
        checks.append(Check("被会话引用时删除返回 409", st == 409, f"实际 {st}"))
        if cid:
            ctx.db(f"DELETE FROM conversations WHERE id = {cid}")

        # 解除引用后删除成功，agents 与绑定行一并清理
        st, _, _ = ctx.http("DELETE", f"/api/tutor/agents/{aid}", token=teacher)
        gone = ctx.db_one(f"SELECT id FROM agents WHERE id = {aid}") is None
        bind_gone = ctx.db_one(f"SELECT agent_id FROM agent_skills WHERE agent_id = {aid}") is None
        checks.append(Check("删除成功且 agents 行消失", st == 200 and gone, f"status={st} 残留={not gone}"))
        checks.append(Check("绑定行随删除一并清理", bind_gone, "" if bind_gone else "agent_skills 仍有残留"))
    finally:
        ctx.db(f"DELETE FROM messages WHERE conversation_id IN (SELECT id FROM conversations WHERE agent_id = {aid})")
        ctx.db(f"DELETE FROM conversations WHERE agent_id = {aid}")
        ctx.db(f"DELETE FROM agent_skills WHERE agent_id = {aid}")
        ctx.db(f"DELETE FROM agent_materials WHERE agent_id = {aid}")
        ctx.db(f"DELETE FROM agents WHERE id = {aid}")
    return finish(res, checks, started)
