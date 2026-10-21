"""GET /api/tutor/assistant/skills/{id}/resources 技能包资源读取：班级隔离、路径限制与缓存头（测后删除）。"""
import json
import time

from lib.common import (
    STUDENT,
    TEACHER,
    Check,
    cfg,
    delete_skill_by_name,
    ensure_mcp_server,
    finish,
    make_result,
    multipart_file,
    zip_fixture,
)

FIXTURE = "skill-pythagorean-guide"
NAME = "勾股定理解题引导"
MCP_NAME = "campus-tools"
MCP_URL = "http://mcp-demo:8090"


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("GET", "/api/tutor/assistant/skills/{id}/resources")

    teacher = ctx.login(TEACHER)
    student = ctx.login(STUDENT)
    token = cfg("MCP_AUTH_TOKEN")
    checks.append(Check("前置：已配置 MCP_AUTH_TOKEN", bool(token), "缺少 MCP_AUTH_TOKEN"))
    if not token:
        return finish(res, checks, started)
    ensure_mcp_server(ctx, teacher, MCP_NAME, MCP_URL, token)

    delete_skill_by_name(ctx, teacher, NAME)
    body, ctype = multipart_file("file", "skill.zip", zip_fixture(FIXTURE), "application/zip")
    st, _, resp = ctx.http("POST", "/api/tutor/assistant/skills/package", token=teacher, raw=body,
                           headers={"Content-Type": ctype})
    checks.append(Check("前置：上传示例技能包返回 201", st == 201, f"实际 {st} body={resp[:200]!r}"))
    if st != 201:
        return finish(res, checks, started)
    sid = json.loads(resp)["id"]

    # 正常读取文本资源：200 + no-store + 内容命中
    st, headers, data = ctx.http(
        "GET", f"/api/tutor/assistant/skills/{sid}/resources?path=references/pythagorean-theorem.md",
        token=teacher)
    h = {k.lower(): v for k, v in headers.items()}
    checks.append(Check("读取 references 资源返回 200", st == 200, f"实际 {st}"))
    checks.append(Check("响应带 Cache-Control: no-store", h.get("cache-control") == "no-store",
                        f"headers={h.get('cache-control')}"))
    checks.append(Check("文本资源 Content-Type 为 text/plain",
                        "text/plain" in h.get("content-type", ""), f"ct={h.get('content-type')}"))
    checks.append(Check("资源内容包含勾股定理关键字",
                        "勾股" in data.decode("utf-8", "replace"), f"body={data[:120]!r}"))

    # 脚本资源可下载但仅作文本返回
    st, headers, data = ctx.http(
        "GET", f"/api/tutor/assistant/skills/{sid}/resources?path=scripts/check_triangle.py",
        token=teacher)
    checks.append(Check("scripts 资源可读取且不作为内容执行",
                        st == 200 and (b"def " in data or b"print" in data), f"status={st}"))

    # 越界与缺失
    st, _, _ = ctx.http("GET", f"/api/tutor/assistant/skills/{sid}/resources?path=../SKILL.md",
                        token=teacher)
    checks.append(Check("路径穿越返回 400", st == 400, f"实际 {st}"))
    st, _, _ = ctx.http("GET", f"/api/tutor/assistant/skills/{sid}/resources?path=SKILL.md",
                        token=teacher)
    checks.append(Check("resources 目录外的路径返回 400", st == 400, f"实际 {st}"))
    st, _, _ = ctx.http("GET", f"/api/tutor/assistant/skills/{sid}/resources?path=references/none.md",
                        token=teacher)
    checks.append(Check("不存在的资源返回 404", st == 404, f"实际 {st}"))
    st, _, _ = ctx.http("GET", f"/api/tutor/assistant/skills/{sid}/resources", token=teacher)
    checks.append(Check("缺少 path 参数返回 400", st == 400, f"实际 {st}"))

    # 学生 403
    st, _, _ = ctx.http(
        "GET", f"/api/tutor/assistant/skills/{sid}/resources?path=references/pythagorean-theorem.md",
        token=student)
    checks.append(Check("学生读取资源返回 403", st == 403, f"实际 {st}"))

    # 清理
    st, _, _ = ctx.http("DELETE", f"/api/tutor/assistant/skills/{sid}", token=teacher)
    checks.append(Check("清理：技能包已删除", st == 200, f"status={st}"))

    return finish(res, checks, started)
