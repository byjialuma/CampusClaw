"""POST /api/tutor/agents 新建智能体：权限、参数校验、重名与 agents 表一致性（测后删除）。"""
import json
import time

from lib.common import STUDENT, TEACHER, TAG, Check, finish, make_result, sql_str

NAME = f"接口测试智能体{TAG}"
PROMPT = "接口测试提示词：请耐心引导"


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("POST", "/api/tutor/agents")

    teacher = ctx.login(TEACHER)
    cls = int(ctx.db_one("SELECT class_id FROM users WHERE username = " + sql_str(TEACHER[0]))[0])

    # 未登录 401 / 学生 403
    st, _, _ = ctx.http("POST", "/api/tutor/agents", json_body={"name": NAME, "systemPrompt": PROMPT})
    checks.append(Check("未登录返回 401", st == 401, f"实际 {st}"))
    st, _, _ = ctx.http("POST", "/api/tutor/agents", token=ctx.login(STUDENT),
                        json_body={"name": NAME, "systemPrompt": PROMPT})
    checks.append(Check("学生返回 403", st == 403, f"实际 {st}"))

    # 参数校验 400
    st, _, _ = ctx.http("POST", "/api/tutor/agents", token=teacher, json_body={"name": "  ", "systemPrompt": PROMPT})
    checks.append(Check("空名称返回 400", st == 400, f"实际 {st}"))
    st, _, _ = ctx.http("POST", "/api/tutor/agents", token=teacher, json_body={"name": "长" * 121, "systemPrompt": PROMPT})
    checks.append(Check("名称超 120 字返回 400", st == 400, f"实际 {st}"))
    st, _, _ = ctx.http("POST", "/api/tutor/agents", token=teacher, json_body={"name": NAME, "systemPrompt": "长" * 2001})
    checks.append(Check("提示词超 2000 字返回 400", st == 400, f"实际 {st}"))

    # 正常新建
    st, _, body = ctx.http("POST", "/api/tutor/agents", token=teacher,
                           json_body={"name": NAME, "systemPrompt": PROMPT})
    checks.append(Check("教师新建返回 201", st == 201, f"实际 {st} body={body[:160]!r}"))
    if st != 201:
        return finish(res, checks, started)
    ag = json.loads(body)
    aid = ag.get("id")
    ok = (isinstance(aid, int) and aid > 0 and ag.get("name") == NAME
          and ag.get("systemPrompt") == PROMPT and ag.get("skillIds") == [] and ag.get("handoutIds") == [])
    checks.append(Check("响应字段符合预期（空绑定）", ok, f"resp={ag}"))

    # agents 表落库且归属本班
    row = ctx.db_one(f"SELECT class_id, name, system_prompt FROM agents WHERE id = {aid}") if aid else None
    checks.append(Check("agents 表落库且归属本班",
                        row is not None and int(row[0]) == cls and row[1] == NAME and row[2] == PROMPT,
                        f"db={row} class={cls}"))

    # 同班重名 400
    st, _, _ = ctx.http("POST", "/api/tutor/agents", token=teacher, json_body={"name": NAME, "systemPrompt": "dup"})
    checks.append(Check("同班重名返回 400", st == 400, f"实际 {st}"))

    # 清理
    if aid:
        ctx.db(f"DELETE FROM agent_skills WHERE agent_id = {aid}")
        ctx.db(f"DELETE FROM agent_materials WHERE agent_id = {aid}")
        ctx.db(f"DELETE FROM agents WHERE id = {aid}")
        checks.append(Check("清理后 agents 行消失",
                            ctx.db_one(f"SELECT id FROM agents WHERE id = {aid}") is None))
    return finish(res, checks, started)
