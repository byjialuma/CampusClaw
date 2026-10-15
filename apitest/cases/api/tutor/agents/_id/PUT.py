"""PUT /api/tutor/agents/{id} 修改智能体名称与提示词：校验、跨班 404 与 agents 表一致性。"""
import json
import time

from lib.common import STUDENT, TEACHER, TAG, Check, finish, make_result, sql_str

NAME = f"接口测试改名{TAG}"


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("PUT", "/api/tutor/agents/{id}")

    teacher = ctx.login(TEACHER)
    cls = int(ctx.db_one("SELECT class_id FROM users WHERE username = " + sql_str(TEACHER[0]))[0])
    other = ctx.db_one(f"SELECT id FROM agents WHERE class_id <> {cls} LIMIT 1")

    # 建一个临时智能体作为待改对象
    st, _, body = ctx.http("POST", "/api/tutor/agents", token=teacher,
                           json_body={"name": f"临时{TAG}", "systemPrompt": "原提示"})
    if st != 201:
        checks.append(Check("前置：创建临时智能体", False, f"HTTP {st} {body[:160]!r}"))
        return finish(res, checks, started)
    aid = json.loads(body)["id"]

    try:
        # 未登录 401 / 学生 403
        st, _, _ = ctx.http("PUT", f"/api/tutor/agents/{aid}", json_body={"name": NAME, "systemPrompt": "x"})
        checks.append(Check("未登录返回 401", st == 401, f"实际 {st}"))
        st, _, _ = ctx.http("PUT", f"/api/tutor/agents/{aid}", token=ctx.login(STUDENT),
                            json_body={"name": NAME, "systemPrompt": "x"})
        checks.append(Check("学生返回 403", st == 403, f"实际 {st}"))

        # 参数校验 400
        st, _, _ = ctx.http("PUT", f"/api/tutor/agents/{aid}", token=teacher, json_body={"name": " ", "systemPrompt": "x"})
        checks.append(Check("空名称返回 400", st == 400, f"实际 {st}"))
        st, _, _ = ctx.http("PUT", f"/api/tutor/agents/{aid}", token=teacher,
                            json_body={"name": NAME, "systemPrompt": "长" * 2001})
        checks.append(Check("提示词超 2000 字返回 400", st == 400, f"实际 {st}"))

        # 跨班修改 404
        if other:
            st, _, _ = ctx.http("PUT", f"/api/tutor/agents/{other[0]}", token=teacher,
                                json_body={"name": "跨班尝试改名", "systemPrompt": "x"})
            checks.append(Check("跨班修改返回 404", st == 404, f"实际 {st}"))

        # 正常改名与改提示词
        st, _, body = ctx.http("PUT", f"/api/tutor/agents/{aid}", token=teacher,
                               json_body={"name": NAME, "systemPrompt": "新提示词"})
        checks.append(Check("改名返回 200", st == 200, f"实际 {st} body={body[:160]!r}"))
        row = ctx.db_one(f"SELECT name, system_prompt FROM agents WHERE id = {aid}")
        checks.append(Check("agents 表已更新", row is not None and row[0] == NAME and row[1] == "新提示词", f"db={row}"))

        # 同班重名 400
        existing = ctx.db_one(f"SELECT name FROM agents WHERE class_id = {cls} AND id <> {aid} LIMIT 1")
        if existing:
            st, _, _ = ctx.http("PUT", f"/api/tutor/agents/{aid}", token=teacher,
                                json_body={"name": existing[0], "systemPrompt": "x"})
            checks.append(Check("改成同班已存在名称返回 400", st == 400, f"实际 {st}"))
    finally:
        ctx.http("DELETE", f"/api/tutor/agents/{aid}", token=teacher)
        ctx.db(f"DELETE FROM agent_skills WHERE agent_id = {aid}")
        ctx.db(f"DELETE FROM agent_materials WHERE agent_id = {aid}")
        ctx.db(f"DELETE FROM agents WHERE id = {aid}")
    return finish(res, checks, started)
