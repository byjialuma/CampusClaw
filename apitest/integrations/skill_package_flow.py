"""集成用例：示例技能包「勾股定理解题引导」端到端验收。

链路：连接演示 MCP（campus-tools）→ 上传 apitest/fixtures/skill-pythagorean-guide
→ 详情可见声明资源与 scripts 条目 → 启用 → 绑定新建智能体
→ 学生提问「勾股定理中已知两条直角边，斜边怎么求？」
→ SSE meta 命中技能、资源已加载且无截断、formula_hint 调用 success
→ 审计出现 success；回答不写出最终数值、scripts 未被读入上下文。
数据全部自建、测后清理。
"""
import json
import re
import time
import traceback

from lib.common import (
    STUDENT,
    TAG,
    TEACHER,
    Check,
    cfg,
    delete_skill_by_name,
    ensure_mcp_server,
    finish,
    make_result,
    multipart_file,
    parse_sse,
    sql_str,
    zip_fixture,
)

FIXTURE = "skill-pythagorean-guide"
SKILL_NAME = "勾股定理解题引导"
MCP_NAME = "campus-tools"
MCP_URL = "http://mcp-demo:8090"
TOOL_REF = "formula_hint@campus-tools"
QUESTION = "勾股定理中已知两条直角边，斜边怎么求？"


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("集成", "上传示例技能包 → 绑定智能体 → 学生提问 → 资源加载与工具调用")

    token = cfg("MCP_AUTH_TOKEN")
    if not token:
        checks.append(Check("前置：已配置 MCP_AUTH_TOKEN", False, "缺少 MCP_AUTH_TOKEN"))
        return finish(res, checks, started)

    teacher = ctx.login(TEACHER)
    student = ctx.login(STUDENT)
    ids = {}

    def flow():
        # 1) 确保演示 MCP 已连接
        ids["srv"], created = ensure_mcp_server(ctx, teacher, MCP_NAME, MCP_URL, token)
        ids["srv_created"] = created
        st, _, body = ctx.http("GET", "/api/tutor/mcp/servers", token=teacher)
        srv = next((s for s in json.loads(body) if s.get("id") == ids["srv"]), {}) if st == 200 else {}
        names = [t.get("name") for t in srv.get("tools") or []]
        checks.append(Check("演示 MCP 已连接且含 formula_hint",
                            srv.get("connected") is True and "formula_hint" in names, f"server={srv}"))

        # 2) 上传示例技能包（先清同名，保证可重复执行）
        delete_skill_by_name(ctx, teacher, SKILL_NAME)
        body, ctype = multipart_file("file", "skill-pythagorean-guide.zip",
                                     zip_fixture(FIXTURE), "application/zip")
        st, _, body = ctx.http("POST", "/api/tutor/assistant/skills/package", token=teacher, raw=body,
                               headers={"Content-Type": ctype})
        checks.append(Check("上传示例技能包返回 201", st == 201, f"实际 {st} body={body[:300]!r}"))
        if st != 201:
            return
        detail = json.loads(body)
        ids["skill"] = detail["id"]
        files = [f.get("path") for f in detail.get("files") or []]
        checks.append(Check("详情包含 3 条声明资源与 scripts/check_triangle.py",
                            len(detail.get("resources") or []) == 3
                            and "scripts/check_triangle.py" in files, f"files={files}"))
        checks.append(Check("工具声明为 formula_hint@campus-tools + knowledge_search",
                            {t.get("raw") for t in detail.get("tools") or []}
                            == {"formula_hint@campus-tools", "knowledge_search"},
                            f"tools={detail.get('tools')}"))

        # 3) 启用并绑定到新建智能体
        st, _, _ = ctx.http("PUT", f"/api/tutor/assistant/skills/{ids['skill']}", token=teacher,
                            json_body={"enabled": True})
        checks.append(Check("启用技能返回 200", st == 200, f"实际 {st}"))

        st, _, body = ctx.http("POST", "/api/tutor/agents", token=teacher,
                               json_body={"name": f"技能包智能体{TAG}",
                                          "systemPrompt": "只给思路与下一步提示，不直接给出最终数值结果。"})
        checks.append(Check("新建智能体返回 201", st == 201, f"实际 {st} body={body[:160]!r}"))
        if st != 201:
            return
        ids["agent"] = json.loads(body)["id"]
        st, _, body = ctx.http("PUT", f"/api/tutor/agents/{ids['agent']}/skills", token=teacher,
                               json_body={"skillIds": [ids["skill"]]})
        checks.append(Check("绑定技能到智能体返回 200", st == 200, f"实际 {st} body={body[:160]!r}"))

        # 4) 学生会话提问
        st, _, body = ctx.http("POST", "/api/tutor/conversations", token=student,
                               json_body={"agentId": ids["agent"]})
        checks.append(Check("学生建会话返回 201", st == 201, f"实际 {st} body={body[:160]!r}"))
        if st != 201:
            return
        ids["conv"] = json.loads(body)["id"]

        st, headers, body = ctx.http("POST", f"/api/tutor/conversations/{ids['conv']}/messages",
                                     token=student, json_body={"question": QUESTION}, timeout=180)
        ctype = next((v for k, v in headers.items() if k.lower() == "content-type"), "")
        checks.append(Check("提问返回 SSE 200", st == 200 and "text/event-stream" in ctype,
                            f"status={st} content-type={ctype}"))
        if st != 200:
            return

        events = parse_sse(body)
        kinds = [e for e, _ in events]
        checks.append(Check("事件流无 error", "error" not in kinds, f"events={kinds}"))
        meta = next((d for e, d in events if e == "meta"), {})
        checks.append(Check("meta.skills 命中示例技能",
                            [s.get("id") for s in (meta.get("skills") or [])] == [ids["skill"]],
                            f"skills={meta.get('skills')}"))

        # 5) 资源加载状态：3 条全部 loaded、无截断、脚本未进入上下文
        statuses = meta.get("skillResources") or []
        loaded = [s for s in statuses if s.get("loaded")]
        paths = [s.get("path") for s in statuses]
        checks.append(Check("meta.skillResources 记录 3 条声明资源且全部加载",
                            len(statuses) == 3 and len(loaded) == 3, f"resources={statuses}"))
        checks.append(Check("资源均未截断且无失败原因",
                            all(not s.get("truncated") and not s.get("error") for s in statuses),
                            f"resources={statuses}"))
        checks.append(Check("scripts 未被读入上下文",
                            not any(str(p).startswith("scripts/") for p in paths), f"paths={paths}"))

        calls = meta.get("toolCalls") or []
        hit = next((c for c in calls if c.get("toolRef") == TOOL_REF), None)
        checks.append(Check("meta.toolCalls 记录 formula_hint 且 status=success",
                            hit is not None and hit.get("status") == "success", f"toolCalls={calls}"))

        # 6) 审计表中出现 success 记录
        row = ctx.db_one(
            "SELECT status, error FROM mcp_calls "
            f"WHERE conversation_id = {ids['conv']} AND tool_ref = {sql_str(TOOL_REF)} "
            "ORDER BY id DESC LIMIT 1")
        checks.append(Check("mcp_calls 出现该引用的 success 记录",
                            row is not None and row[0] == "success", f"db={row}"))

        # 7) 回答不写出最终数值结果
        answer = "".join(d.get("text", "") for e, d in events if e == "delta")
        cleaned = re.sub(r"(?m)^\s*\d+[.、)]\s*", "", answer)
        checks.append(Check("回答未给出最终数值结果",
                            bool(answer.strip()) and not re.search(r"[0-9]", cleaned),
                            f"answer={answer[:300]!r}"))

    err = ""
    try:
        flow()
    except Exception:
        err = traceback.format_exc(limit=3)

    # 清理：会话与审计先删，再删智能体、技能与测试期间新建的 MCP 服务器
    if ids.get("conv"):
        ctx.db(f"DELETE FROM mcp_calls WHERE conversation_id = {ids['conv']}")
        ctx.db(f"DELETE FROM conversations WHERE id = {ids['conv']}")
    if ids.get("agent"):
        st, _, _ = ctx.http("DELETE", f"/api/tutor/agents/{ids['agent']}", token=teacher)
        checks.append(Check("清理：智能体已删除",
                            st == 200 and ctx.db_one(f"SELECT id FROM agents WHERE id = {ids['agent']}") is None))
    if ids.get("skill"):
        st, _, _ = ctx.http("DELETE", f"/api/tutor/assistant/skills/{ids['skill']}", token=teacher)
        checks.append(Check("清理：技能包已删除",
                            st == 200 and ctx.db_one(f"SELECT id FROM skills WHERE id = {ids['skill']}") is None))
    if ids.get("srv_created"):
        ctx.http("DELETE", f"/api/tutor/mcp/servers/{ids['srv']}", token=teacher)
    if ids.get("conv"):
        checks.append(Check("清理：会话与审计已删除",
                            ctx.db_one(f"SELECT id FROM conversations WHERE id = {ids['conv']}") is None
                            and ctx.db_one(f"SELECT id FROM mcp_calls WHERE conversation_id = {ids['conv']}") is None))

    return finish(res, checks, started, err)
