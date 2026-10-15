"""POST /api/tutor/assistant/skills 在线新建技能：权限控制与 skills 表一致性（测后删除）。"""
import json
import time

from lib.common import STUDENT, TEACHER, TAG, Check, finish, make_result, sql_str

NAME = f"接口测试技能{TAG}"
CONTENT = f"解题时先复述题意再分步引导。接口测试标记{TAG}"


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("POST", "/api/tutor/assistant/skills")

    teacher = ctx.login(TEACHER)

    # 未登录 401 / 学生 403
    st, _, _ = ctx.http("POST", "/api/tutor/assistant/skills",
                        json_body={"name": NAME, "content": CONTENT})
    checks.append(Check("未登录返回 401", st == 401, f"实际 {st}"))
    st, _, _ = ctx.http("POST", "/api/tutor/assistant/skills",
                        token=ctx.login(STUDENT), json_body={"name": NAME, "content": CONTENT})
    checks.append(Check("学生返回 403", st == 403, f"实际 {st}"))

    # 空名称 / 空内容 400
    st, _, _ = ctx.http("POST", "/api/tutor/assistant/skills",
                        token=teacher, json_body={"name": " ", "content": CONTENT})
    checks.append(Check("空名称返回 400", st == 400, f"实际 {st}"))
    st, _, _ = ctx.http("POST", "/api/tutor/assistant/skills",
                        token=teacher, json_body={"name": NAME, "content": " "})
    checks.append(Check("空内容返回 400", st == 400, f"实际 {st}"))

    # 教师新建成功：默认停用、来源 online、非内置
    st, _, body = ctx.http("POST", "/api/tutor/assistant/skills",
                           token=teacher, json_body={"name": NAME, "content": CONTENT})
    checks.append(Check("教师新建返回 201", st == 201, f"实际 {st} body={body[:200]!r}"))
    if st != 201:
        return finish(res, checks, started)
    sk = json.loads(body)
    sid = sk.get("id")
    ok = (isinstance(sid, int) and sid > 0 and sk.get("name") == NAME and sk.get("content") == CONTENT
          and sk.get("enabled") is False and sk.get("source") == "online" and sk.get("builtin") is False)
    checks.append(Check("响应字段符合预期（默认停用/online/非内置）", ok, f"resp={sk}"))

    # skills 表：内容与归属本班助手一致
    me = ctx.db_one("SELECT class_id FROM users WHERE username = " + sql_str(TEACHER[0]))
    row = ctx.db_one(
        "SELECT s.name, s.content, s.source, s.enabled, s.builtin, a.class_id "
        "FROM skills s JOIN assistants a ON a.id = s.assistant_id WHERE s.id = " + str(sid)) if sid else None
    checks.append(Check("skills 表存在该技能", row is not None))
    if row and me:
        same = (row[0] == NAME and row[1] == CONTENT and row[2] == "online"
                and row[3] == "0" and row[4] == "0" and int(row[5]) == int(me[0]))
        checks.append(Check("skills 行内容与班级归属一致", same, f"db={row} class={me[0]}"))

    # 清理：走 DELETE 接口删除（顺带验证删除路径），并核对行消失
    if sid:
        st, _, _ = ctx.http("DELETE", f"/api/tutor/assistant/skills/{sid}", token=teacher)
        gone = ctx.db_one(f"SELECT id FROM skills WHERE id = {sid}") is None
        checks.append(Check("DELETE 删除成功且 skills 行消失", st == 200 and gone,
                            f"status={st} 行残留={not gone}"))
    return finish(res, checks, started)
