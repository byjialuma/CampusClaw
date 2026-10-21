"""POST /api/tutor/assistant/skills/package 技能包上传：权限、zip 校验、落库与详情回读（测后删除）。"""
import json
import time

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
    sql_str,
    zip_entries,
    zip_fixture,
)

FIXTURE = "skill-pythagorean-guide"
NAME = "勾股定理解题引导"
MCP_NAME = "campus-tools"
MCP_URL = "http://mcp-demo:8090"


def _upload(ctx, token, body, ctype):
    return ctx.http("POST", "/api/tutor/assistant/skills/package", token=token, raw=body,
                    headers={"Content-Type": ctype, "Accept": "application/json"})


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("POST", "/api/tutor/assistant/skills/package")

    teacher = ctx.login(TEACHER)
    student = ctx.login(STUDENT)
    token = cfg("MCP_AUTH_TOKEN")
    checks.append(Check("前置：已配置 MCP_AUTH_TOKEN", bool(token), "缺少 MCP_AUTH_TOKEN"))
    if not token:
        return finish(res, checks, started)
    ensure_mcp_server(ctx, teacher, MCP_NAME, MCP_URL, token)

    pkg = zip_fixture(FIXTURE)
    body, ctype = multipart_file("file", "skill.zip", pkg, "application/zip")

    # 未登录 401 / 学生 403
    st, _, _ = _upload(ctx, None, body, ctype)
    checks.append(Check("未登录返回 401", st == 401, f"实际 {st}"))
    st, _, _ = _upload(ctx, student, body, ctype)
    checks.append(Check("学生返回 403", st == 403, f"实际 {st}"))

    # 缺 SKILL.md
    bad, bad_ct = multipart_file("file", "bad.zip",
                                 zip_entries({"README.md": b"# no manifest"}),
                                 "application/zip")
    st, _, resp = _upload(ctx, teacher, bad, bad_ct)
    checks.append(Check("缺少 SKILL.md 返回 400", st == 400, f"实际 {st} body={resp[:200]!r}"))

    # 路径穿越
    trav, trav_ct = multipart_file("file", "trav.zip",
                                   zip_entries({"SKILL.md": b"---\nname: x\ndescription: y\n---\n",
                                                "../escape.md": b"escape"}),
                                   "application/zip")
    st, _, resp = _upload(ctx, teacher, trav, trav_ct)
    checks.append(Check("路径穿越返回 400", st == 400, f"实际 {st} body={resp[:200]!r}"))

    # 前置清理：固定 fixture 名称保证用例可重复执行
    delete_skill_by_name(ctx, teacher, NAME)

    # 合法上传
    st, _, resp = _upload(ctx, teacher, body, ctype)
    checks.append(Check("合法技能包返回 201", st == 201, f"实际 {st} body={resp[:300]!r}"))
    if st != 201:
        return finish(res, checks, started)
    detail = json.loads(resp)
    sid = detail.get("id")

    refs = [f.get("path") for f in detail.get("files") or []]
    ok = (detail.get("name") == NAME and detail.get("source") == "package"
          and detail.get("enabled") is False and detail.get("builtin") is False)
    checks.append(Check("响应字段符合预期（package/默认停用）", ok, f"resp={detail}"))
    checks.append(Check("声明资源 3 条且资源目录文件 4 个（含 scripts）",
                        len(detail.get("resources") or []) == 3 and len(refs) == 4, f"files={refs}"))
    checks.append(Check("包内文件包含 references/assets/scripts 三类",
                        any(p.startswith("references/") for p in refs)
                        and any(p.startswith("assets/") for p in refs)
                        and any(p.startswith("scripts/") for p in refs), f"files={refs}"))
    tools = [t.get("raw") for t in detail.get("tools") or []]
    checks.append(Check("工具声明合并自 frontmatter（formula_hint + knowledge_search）",
                        "formula_hint@campus-tools" in tools and "knowledge_search" in tools,
                        f"tools={tools}"))

    # 同班同名再次上传 409
    st, _, resp = _upload(ctx, teacher, body, ctype)
    checks.append(Check("同班同名技能包返回 409", st == 409, f"实际 {st} body={resp[:200]!r}"))

    # 数据库：slug/dir_path/content_hash/frontmatter 已回填
    row = ctx.db_one(
        "SELECT s.slug, s.dir_path, s.content_hash, s.source, a.class_id "
        "FROM skills s JOIN assistants a ON a.id = s.assistant_id WHERE s.id = " + str(sid))
    checks.append(Check("skills 行存在技能包元信息", row is not None, f"db={row}"))
    if row:
        checks.append(Check("slug 非空、dir_path 指向 skills/<class>/<id>、content_hash 为 64 位",
                            bool(row[0]) and row[1].startswith("skills/") and len(row[2]) == 64
                            and row[3] == "package", f"db={row}"))

    # 学生读取详情 403
    st, _, _ = ctx.http("GET", f"/api/tutor/assistant/skills/{sid}", token=student)
    checks.append(Check("学生读取技能包详情返回 403", st == 403, f"实际 {st}"))

    # 跨班 404：在 B 班助手下插入一条技能，A 班教师读取应 404
    b_skill = None
    b_assistant = ctx.db_one(
        "SELECT a.id, a.class_id FROM assistants a JOIN classes c ON c.id = a.class_id "
        "WHERE a.class_id <> (SELECT class_id FROM users WHERE username = " + sql_str(TEACHER[0]) + ") LIMIT 1")
    if b_assistant:
        ctx.db("INSERT INTO skills (assistant_id, name, description, content, source, enabled) VALUES ("
               f"{b_assistant[0]}, '跨班技能{TAG}', '跨班', '跨班内容', 'online', 0)")
        b_skill = ctx.db_one(
            "SELECT id FROM skills WHERE assistant_id = " + b_assistant[0]
            + " AND name = " + sql_str("跨班技能" + TAG))
        if b_skill:
            st, _, _ = ctx.http("GET", f"/api/tutor/assistant/skills/{b_skill[0]}", token=teacher)
            checks.append(Check("跨班技能详情返回 404", st == 404, f"实际 {st} id={b_skill[0]}"))
            st, _, _ = ctx.http("GET", f"/api/tutor/assistant/skills/{b_skill[0]}/resources?path=SKILL.md",
                                token=teacher)
            checks.append(Check("跨班技能资源返回 404", st == 404, f"实际 {st}"))

    # 清理
    st, _, _ = ctx.http("DELETE", f"/api/tutor/assistant/skills/{sid}", token=teacher)
    checks.append(Check("清理：技能包已删除",
                        st == 200 and ctx.db_one(f"SELECT id FROM skills WHERE id = {sid}") is None,
                        f"status={st}"))
    if b_skill:
        ctx.db(f"DELETE FROM skills WHERE id = {b_skill[0]}")

    return finish(res, checks, started)
