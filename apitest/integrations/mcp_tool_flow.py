"""集成用例：连接演示 MCP → 注册引用 formula_hint 的技能 → 绑定智能体 → 会话提问 → 审计落库 success。

覆盖 MCP 全链路（跨接口，不映射单一 URL）：
  登记服务器 → 连接并发现工具 → 新建技能（正文声明 `工具：formula_hint@<server>` 与「适用条件」）
  → 启用 → 新建智能体 → 全量绑定技能 → 学生会话提问命中路由 → 远端工具调用
  → mcp_calls 落库 success → 教师审计接口可见（tool@server / 状态 / 用户 / 时间）。

数据全部自建、测后清理，不依赖也不污染种子数据。
"""
import json
import time
import traceback

from lib.common import STUDENT, TEACHER, TAG, Check, cfg, finish, make_result, parse_sse, sql_str

SRV = f"it-flow-{TAG}"
SRV_URL = "http://mcp-demo:8090"
TOKEN = cfg("MCP_AUTH_TOKEN")
SKILL_NAME = f"集成换算技能{TAG}"
TOOL = "formula_hint"
COND = "一元二次"
REF = f"{TOOL}@{SRV}"
QUESTION = f"一元二次方程的判别式怎么求？（集成标记{TAG}）"


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("MCP", "连接演示 MCP → 技能引用 formula_hint → 会话提问 → 审计 success")

    if not TOKEN:
        checks.append(Check("前置：已配置 MCP_AUTH_TOKEN", False, "缺少 MCP_AUTH_TOKEN，无法连接演示 MCP"))
        return finish(res, checks, started)

    teacher = ctx.login(TEACHER)
    student = ctx.login(STUDENT)
    su = ctx.db_one("SELECT id, display_name FROM users WHERE username = " + sql_str(STUDENT[0]))
    student_id = int(su[0]) if su else 0
    student_name = su[1] if su else ""

    ids = {}

    def flow():
        """按顺序推进全链路；前置条件不满足时提前返回，已建数据由 cleanup 兜底。"""
        # 1) 登记演示 MCP 服务器
        st, _, body = ctx.http("POST", "/api/tutor/mcp/servers", token=teacher,
                               json_body={"name": SRV, "baseUrl": SRV_URL, "authToken": TOKEN})
        checks.append(Check("登记演示 MCP 返回 201", st == 201, f"实际 {st} body={body[:160]!r}"))
        if st != 201:
            return
        text = body.decode("utf-8", "replace")
        srv = json.loads(text)
        ids["srv"] = srv["id"]
        checks.append(Check("登记响应只回 hasAuth 布尔、不含令牌原文",
                            srv.get("hasAuth") is True and "authToken" not in text))

        # 2) 连接并发现工具（「列出工具」入口）
        st, _, body = ctx.http("POST", f"/api/tutor/mcp/servers/{ids['srv']}/connect", token=teacher)
        checks.append(Check("连接演示 MCP 返回 200", st == 200, f"实际 {st} body={body[:200]!r}"))
        if st != 200:
            return
        conn = json.loads(body.decode("utf-8", "replace"))
        names = [t.get("name") for t in conn.get("tools", [])]
        checks.append(Check("连接后 connected=true 且工具清单含 formula_hint",
                            conn.get("connected") is True and TOOL in names, f"tools={names}"))

        # 3) 新建技能（正文声明工具引用）并启用
        content = f"工具：{TOOL}@{SRV}\n适用条件：{COND}\n请引用工具返回的公式与分步提示。"
        st, _, body = ctx.http("POST", "/api/tutor/assistant/skills", token=teacher,
                               json_body={"name": SKILL_NAME, "content": content})
        checks.append(Check("新建声明工具引用的技能返回 201", st == 201, f"实际 {st} body={body[:200]!r}"))
        if st != 201:
            return
        sk = json.loads(body)
        ids["skill"] = sk["id"]
        checks.append(Check("新建技能默认停用", sk.get("enabled") is False, f"resp={sk}"))
        st, _, _ = ctx.http("PUT", f"/api/tutor/assistant/skills/{ids['skill']}", token=teacher,
                            json_body={"enabled": True})
        checks.append(Check("启用技能返回 200", st == 200, f"实际 {st}"))

        # 4) 新建智能体并全量绑定该技能
        st, _, body = ctx.http("POST", "/api/tutor/agents", token=teacher,
                               json_body={"name": f"集成智能体{TAG}", "systemPrompt": "只依据工具与材料作答，用简洁中文。"})
        checks.append(Check("新建集成智能体返回 201", st == 201, f"实际 {st} body={body[:160]!r}"))
        if st != 201:
            return
        ids["agent"] = json.loads(body)["id"]
        st, _, body = ctx.http("PUT", f"/api/tutor/agents/{ids['agent']}/skills", token=teacher,
                               json_body={"skillIds": [ids["skill"]]})
        bound_ok = False
        if st == 200:
            ast, _, abody = ctx.http("GET", "/api/tutor/agents", token=teacher)
            agents = json.loads(abody.decode("utf-8", "replace")) if ast == 200 else []
            hit_agent = next((x for x in agents if x.get("id") == ids["agent"]), None)
            bound_ok = hit_agent is not None and hit_agent.get("skillIds") == [ids["skill"]]
        checks.append(Check("绑定技能到智能体成功（回读 skillIds 一致）",
                            st == 200 and bound_ok, f"PUT={st} body={body[:160]!r}"))

        # 5) 学生用该智能体建会话并提问（命中「适用条件」→ 触发远端调用）
        st, _, body = ctx.http("POST", "/api/tutor/conversations", token=student,
                               json_body={"agentId": ids["agent"]})
        checks.append(Check("学生建会话返回 201", st == 201, f"实际 {st} body={body[:160]!r}"))
        if st != 201:
            return
        ids["conv"] = json.loads(body)["id"]

        st, headers, body = ctx.http("POST", f"/api/tutor/conversations/{ids['conv']}/messages",
                                     token=student, json_body={"question": QUESTION}, timeout=180)
        ctype = next((v for k, v in headers.items() if k.lower() == "content-type"), "")
        checks.append(Check("提问返回 200 且为 SSE", st == 200 and "text/event-stream" in ctype,
                            f"status={st} content-type={ctype}"))
        if st != 200:
            return

        events = parse_sse(body)
        kinds = [e for e, _ in events]
        checks.append(Check("事件流无 error", "error" not in kinds, f"events={kinds}"))
        meta = next((d for e, d in events if e == "meta"), {})
        checks.append(Check("meta.skills 命中绑定技能",
                            [s.get("id") for s in (meta.get("skills") or [])] == [ids["skill"]],
                            f"skills={meta.get('skills')}"))
        calls_meta = meta.get("toolCalls") or []
        hit = next((c for c in calls_meta if c.get("toolRef") == REF), None)
        checks.append(Check("meta.toolCalls 记录该引用且 status=success",
                            hit is not None and hit.get("status") == "success", f"toolCalls={calls_meta}"))

        # 6) 审计表出现 success 记录
        row = ctx.db_one(
            "SELECT id, server_id, server_name, tool_ref, status, error, user_id, conversation_id, created_at "
            f"FROM mcp_calls WHERE conversation_id = {ids['conv']} AND tool_ref = {sql_str(REF)} "
            "ORDER BY id DESC LIMIT 1")
        checks.append(Check("mcp_calls 落库一条该引用的记录", row is not None, f"db={row}"))
        if row:
            checks.append(Check("审计 status=success 且 error 为空",
                                row[4] == "success" and row[5] == "NULL", f"db={row}"))
            checks.append(Check("审计 user_id / conversation_id 正确",
                                int(row[6]) == student_id and int(row[7]) == ids["conv"], f"db={row}"))
            checks.append(Check("审计 server_name / server_id 正确",
                                row[2] == SRV and int(row[1]) == ids["srv"], f"db={row}"))
            checks.append(Check("审计写入时间非空", row[8] not in (None, "NULL"), f"db={row}"))

        # 7) 教师审计接口可见该记录（tool@server / 状态 / 用户 / 时间）
        st, _, body = ctx.http("GET", "/api/tutor/mcp/calls", token=teacher)
        checks.append(Check("教师读审计列表返回 200", st == 200, f"实际 {st}"))
        if st == 200:
            listed = json.loads(body.decode("utf-8", "replace"))
            checks.append(Check("审计列表为数组且不超过 50 条",
                                isinstance(listed, list) and len(listed) <= 50,
                                f"type={type(listed).__name__} len={len(listed) if isinstance(listed, list) else '-'}"))
            got = next((c for c in listed if c.get("toolRef") == REF and c.get("conversationId") == ids["conv"]), None)
            checks.append(Check("审计列表含该条记录", got is not None,
                                f"toolRefs={[c.get('toolRef') for c in listed][:8]}"))
            if got:
                checks.append(Check("列表行含 tool@server / 状态 / 用户 / 时间",
                                    got.get("serverId") == ids["srv"] and got.get("serverName") == SRV
                                    and got.get("status") == "success"
                                    and got.get("username") == student_name and bool(got.get("createdAt")),
                                    f"row={got}"))

    err = ""
    try:
        flow()
    except Exception:
        err = traceback.format_exc(limit=3)

    # 清理（先删审计与会话以释放智能体外键，再删智能体/技能/服务器），并核对行已消失
    if ids.get("conv"):
        ctx.db(f"DELETE FROM mcp_calls WHERE conversation_id = {ids['conv']}")
        ctx.db(f"DELETE FROM conversations WHERE id = {ids['conv']}")
    if ids.get("agent"):
        st, _, _ = ctx.http("DELETE", f"/api/tutor/agents/{ids['agent']}", token=teacher)
        gone = ctx.db_one(f"SELECT id FROM agents WHERE id = {ids['agent']}") is None
        checks.append(Check("清理：集成智能体已删除", st == 200 and gone, f"status={st} 残留={not gone}"))
    if ids.get("skill"):
        ctx.http("DELETE", f"/api/tutor/assistant/skills/{ids['skill']}", token=teacher)
        checks.append(Check("清理：技能已删除",
                            ctx.db_one(f"SELECT id FROM skills WHERE id = {ids['skill']}") is None))
    if ids.get("srv"):
        ctx.http("DELETE", f"/api/tutor/mcp/servers/{ids['srv']}", token=teacher)
        checks.append(Check("清理：MCP 服务器已删除",
                            ctx.db_one(f"SELECT id FROM mcp_servers WHERE id = {ids['srv']}") is None))
    if ids.get("conv"):
        checks.append(Check("清理：审计与会话已删除",
                            ctx.db_one(f"SELECT id FROM mcp_calls WHERE conversation_id = {ids['conv']}") is None
                            and ctx.db_one(f"SELECT id FROM conversations WHERE id = {ids['conv']}") is None))

    return finish(res, checks, started, err)
