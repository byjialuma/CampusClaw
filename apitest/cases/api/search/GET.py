"""GET /api/search 语义检索：

- 上传一份含独特检索词的自测材料，等待 material_index 就绪；
- 检索结果必须命中该材料、来源字段完整、且每条命中的 documentId 在 handouts 表中均属本班；
- 用毕清理：删除向量点（Qdrant，内网）→ 删除 handouts 行（级联清 material_index）→ 删除落盘文件。
"""
import json
import subprocess
import time
import urllib.parse
import uuid

from lib.common import (BACKEND_CONTAINER, TEACHER, Check, finish, make_result,
                        multipart_file, qdrant_delete_document, sql_str)


def run(ctx):
    started = time.monotonic()
    checks = []
    res = make_result("GET", "/api/search")
    token = ctx.login(TEACHER)

    st0, _, _ = ctx.http("GET", "/api/search?q=test")
    checks.append(Check("未登录返回 401", st0 == 401, f"实际 {st0}"))

    class_row = ctx.db_one("SELECT class_id FROM users WHERE username = " + sql_str(TEACHER[0]))
    class_id = int(class_row[0])
    term = "zzapitest" + uuid.uuid4().hex[:10]
    hid = None
    try:
        content = ("# 检索自测材料\n\n"
                   f"本段包含独特检索词 {term}，用于验证向量检索一致性。\n"
                   f"第二次出现 {term} 以增强召回，结尾再来一次 {term}。\n").encode("utf-8")
        raw, ctype = multipart_file("file", f"apitest-{term}.md", content)
        st, _, body = ctx.http("POST", "/api/materials", token=token, raw=raw,
                               headers={"Content-Type": ctype})
        checks.append(Check("上传自测材料返回 201", st == 201, f"实际 {st} {body[:200]!r}"))
        if st != 201:
            return finish(res, checks, started)
        hid = json.loads(body)["id"]

        status = "missing"
        for _ in range(45):
            row = ctx.db_one(f"SELECT status FROM material_index WHERE handout_id={hid}")
            status = row[0] if row else "missing"
            if status in ("ready", "failed"):
                break
            time.sleep(1)
        checks.append(Check("material_index 状态就绪", status == "ready", f"实际 {status}"))

        st, _, body = ctx.http("GET", "/api/search?q=" + urllib.parse.quote(term), token=token)
        checks.append(Check("检索返回 200", st == 200, f"实际 {st}"))
        results = json.loads(body).get("results", []) if st == 200 else []
        checks.append(Check("独特词命中自测材料",
                            any(r.get("documentId") == hid for r in results),
                            f"共 {len(results)} 条命中"))
        if results:
            need = ("documentId", "chunkId", "fileName", "snippet", "locator", "score")
            missing = [k for k in need if k not in results[0]]
            checks.append(Check("结果含完整来源信息", not missing, f"缺失字段 {missing}"))
        leaked = []
        for r in results:
            did = int(r.get("documentId"))
            row = ctx.db_one(f"SELECT class_id FROM handouts WHERE id={did}")
            if row is None or int(row[0]) != class_id:
                leaked.append(did)
        checks.append(Check("全部命中的 documentId 均属本班 handouts", not leaked,
                            f"越班/无档: {leaked}"))

        st4, _, _ = ctx.http("GET", "/api/search?q=", token=token)
        checks.append(Check("空查询返回 400", st4 == 400, f"实际 {st4}"))
    finally:
        if hid is not None:
            sp = ctx.db_one(f"SELECT stored_path FROM handouts WHERE id={hid}")
            qdrant_delete_document(hid)
            ctx.db(f"DELETE FROM handouts WHERE id={hid}")
            gone = ctx.db_one(f"SELECT id FROM handouts WHERE id={hid}") is None
            checks.append(Check("清理：handouts 行与向量点已删除", gone))
            if sp:
                subprocess.run(["docker", "exec", BACKEND_CONTAINER, "rm", "-f", sp[0]],
                               capture_output=True)
    return finish(res, checks, started)
