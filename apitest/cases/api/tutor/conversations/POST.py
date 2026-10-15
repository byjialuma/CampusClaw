"""POST /api/tutor/conversations 新建会话（需 agentId）：参数校验与 conversations 表一致性。"""
import json
import time

from lib.common import STUDENT, Check, finish, make_result, sql_str


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("POST", "/api/tutor/conversations")

    # 未登录 401
    st, _, _ = ctx.http("POST", "/api/tutor/conversations", json_body={"agentId": 1})
    checks.append(Check("未登录返回 401", st == 401, f"实际 {st}"))

    token = ctx.login(STUDENT)
    s_cls = int(ctx.db_one("SELECT class_id FROM users WHERE username = " + sql_str(STUDENT[0]))[0])
    agent = ctx.db_one(
        f"SELECT id FROM agents WHERE class_id = {s_cls} ORDER BY updated_at DESC, id DESC LIMIT 1")
    checks.append(Check("前置：本班存在智能体", agent is not None, f"class={s_cls}"))
    if not agent:
        return finish(res, checks, started)
    aid = int(agent[0])

    # 缺失/非法 agentId → 400
    st, _, _ = ctx.http("POST", "/api/tutor/conversations", token=token, json_body={})
    checks.append(Check("缺 agentId 返回 400", st == 400, f"实际 {st}"))
    st, _, _ = ctx.http("POST", "/api/tutor/conversations", token=token, json_body={"agentId": 0})
    checks.append(Check("非正 agentId 返回 400", st == 400, f"实际 {st}"))

    # 跨班/不存在 → 404 且不建会话
    before = int(ctx.db_one("SELECT COUNT(*) AS n FROM conversations")[0])
    other = ctx.db_one(f"SELECT id FROM agents WHERE class_id <> {s_cls} LIMIT 1")
    if other:
        st, _, _ = ctx.http("POST", "/api/tutor/conversations", token=token, json_body={"agentId": int(other[0])})
        checks.append(Check("跨班 agentId 返回 404", st == 404, f"实际 {st}"))
    st, _, _ = ctx.http("POST", "/api/tutor/conversations", token=token, json_body={"agentId": 99999999})
    checks.append(Check("不存在 agentId 返回 404", st == 404, f"实际 {st}"))
    after = int(ctx.db_one("SELECT COUNT(*) AS n FROM conversations")[0])
    checks.append(Check("失败请求未创建会话", after == before, f"before={before} after={after}"))

    # 正常创建
    st, _, body = ctx.http("POST", "/api/tutor/conversations", token=token, json_body={"agentId": aid})
    checks.append(Check("学生创建会话返回 201", st == 201, f"实际 {st} body={body[:160]!r}"))
    if st != 201:
        return finish(res, checks, started)
    data = json.loads(body)
    cid = data.get("id")
    checks.append(Check("响应含 id 且标题为初始占位",
                        isinstance(cid, int) and cid > 0 and data.get("title") == "新的解题会话",
                        f"resp={data}"))

    # conversations 表：归属学生本人与其班级，agent_id 与入参一致
    me = ctx.db_one("SELECT id, class_id FROM users WHERE username = " + sql_str(STUDENT[0]))
    row = ctx.db_one(f"SELECT class_id, user_id, agent_id, title FROM conversations WHERE id = {cid}") if cid else None
    checks.append(Check("conversations 表存在该会话", row is not None))
    if row and me:
        same = (int(row[0]) == int(me[1]) and int(row[1]) == int(me[0])
                and int(row[2]) == aid and row[3] == "新的解题会话")
        checks.append(Check("会话归属/agent_id/标题与库一致", same, f"db={row} aid={aid}"))

    # 清理
    if cid:
        ctx.db(f"DELETE FROM conversations WHERE id = {cid}")
        checks.append(Check("清理后会话已删除",
                            ctx.db_one(f"SELECT id FROM conversations WHERE id = {cid}") is None))
    return finish(res, checks, started)
