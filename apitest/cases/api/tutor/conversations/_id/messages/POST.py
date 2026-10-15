"""POST /api/tutor/conversations/{id}/messages SSE 提问：事件流与 messages 表落库一致性。"""
import json
import time

from lib.common import STUDENT, TEACHER, TAG, Check, finish, make_result, parse_sse, sql_str

QUESTION = f"请用一句话回答：1+1等于几？（接口测试标记{TAG}）"


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("POST", "/api/tutor/conversations/{id}/messages")

    token = ctx.login(STUDENT)

    # 先建会话（需绑定本班智能体），拿到真实 convID
    s_cls = int(ctx.db_one("SELECT class_id FROM users WHERE username = " + sql_str(STUDENT[0]))[0])
    agent = ctx.db_one(
        f"SELECT id FROM agents WHERE class_id = {s_cls} ORDER BY updated_at DESC, id DESC LIMIT 1")
    if not agent:
        checks.append(Check("前置：本班存在智能体", False, f"class={s_cls}"))
        return finish(res, checks, started)
    st, _, body = ctx.http("POST", "/api/tutor/conversations", token=token, json_body={"agentId": int(agent[0])})
    if st != 201:
        checks.append(Check("前置：创建会话", False, f"HTTP {st} {body[:200]!r}"))
        return finish(res, checks, started)
    cid = json.loads(body)["id"]

    try:
        # 未登录 401
        st, _, _ = ctx.http("POST", f"/api/tutor/conversations/{cid}/messages",
                            json_body={"question": QUESTION})
        checks.append(Check("未登录返回 401", st == 401, f"实际 {st}"))

        # 非属主（教师）访问一律 404，与无数据表现一致
        st, _, _ = ctx.http("POST", f"/api/tutor/conversations/{cid}/messages",
                            token=ctx.login(TEACHER), json_body={"question": QUESTION})
        checks.append(Check("非属主返回 404", st == 404, f"实际 {st}"))

        # 空问题 400
        st, _, _ = ctx.http("POST", f"/api/tutor/conversations/{cid}/messages",
                            token=token, json_body={"question": "   "})
        checks.append(Check("空问题返回 400", st == 400, f"实际 {st}"))

        # 正常提问：SSE 流式响应（LLM 调用，放宽超时）
        st, headers, body = ctx.http("POST", f"/api/tutor/conversations/{cid}/messages",
                                     token=token, json_body={"question": QUESTION}, timeout=180)
        ctype = next((v for k, v in headers.items() if k.lower() == "content-type"), "")
        checks.append(Check("提问返回 200 且为 SSE", st == 200 and "text/event-stream" in ctype,
                            f"status={st} content-type={ctype}"))
        if st != 200:
            return finish(res, checks, started)

        events = parse_sse(body)
        kinds = [e for e, _ in events]
        checks.append(Check("首个事件为 meta（引用/技能快照）",
                            bool(kinds) and kinds[0] == "meta", f"events={kinds}"))

        # meta 载荷：citations 每项带自增 index（与回答 [n] 对齐）；skills 为 0/1 元素列表
        meta = next((d for e, d in events if e == "meta"), {})
        cits = meta.get("citations") or []
        checks.append(Check("meta.citations 每项带连续自增 index",
                            all(c.get("index") == i + 1 for i, c in enumerate(cits)),
                            f"indices={[c.get('index') for c in cits]}"))
        sk = meta.get("skills")
        checks.append(Check("meta.skills 为 0 或 1 元素列表",
                            isinstance(sk, list) and len(sk) <= 1, f"skills={sk}"))
        deltas = [d.get("text", "") for e, d in events if e == "delta"]
        checks.append(Check("存在至少一个 delta 事件", len(deltas) > 0, f"delta 数={len(deltas)}"))
        checks.append(Check("无 error 事件", "error" not in kinds, f"events={kinds}"))
        done = [d for e, d in events if e == "done"]
        msg_id = done[-1].get("messageId") if done else None
        checks.append(Check("done 事件含 messageId", isinstance(msg_id, int) and msg_id > 0,
                            f"done={done[-1] if done else None}"))
        answer = "".join(deltas)

        # messages 表：user 轮内容 == 提问；assistant 轮 id == messageId 且内容 == delta 拼接全文
        rows = ctx.db(f"SELECT id, role, content FROM messages WHERE conversation_id = {cid} ORDER BY id")
        checks.append(Check("messages 表落库两轮消息", len(rows) == 2, f"行数={len(rows)}"))
        if len(rows) == 2:
            u_row, a_row = rows
            checks.append(Check("user 轮内容与提问一致",
                                u_row[1] == "user" and u_row[2] == QUESTION,
                                f"role={u_row[1]} content={u_row[2][:60]!r}"))
            same = (msg_id is not None and int(a_row[0]) == msg_id
                    and a_row[1] == "assistant" and a_row[2] == answer)
            checks.append(Check("assistant 轮 id/内容与 SSE 输出一致", same,
                                f"db_id={a_row[0]} msg_id={msg_id} 长度 db={len(a_row[2])} sse={len(answer)}"))

        # 首问后会话标题被更新为问题前 30 字
        title = ctx.db_one(f"SELECT title FROM conversations WHERE id = {cid}")
        checks.append(Check("会话标题更新为问题前 30 字",
                            title is not None and title[0] == QUESTION[:30],
                            f"db={title[0][:40]!r}" if title else "无行"))
    finally:
        ctx.db(f"DELETE FROM conversations WHERE id = {cid}")  # 级联清理消息
    return finish(res, checks, started)
