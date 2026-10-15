#!/usr/bin/env python3
"""一键执行 apitest/cases 下全部接口用例并生成 HTML 报告。

目录约定：cases/ 下的子路径映射 URL 路径，文件名映射 HTTP 方法——
  cases/api/sessions/POST.py                       -> POST   /api/sessions
  cases/api/tutor/conversations/_id/messages/POST.py -> POST /api/tutor/conversations/{id}/messages
以 "_" 开头的目录段视为路径参数。

跨接口集成用例放在 integrations/ 下，文件名不限（"_" 前缀表示临时脚本，不执行）；
这类用例不映射单一 URL，自身在 make_result() 中声明 name/method/url，例如 MCP 全链路（登记→连接→建技能→提问→审计）。

每个用例模块必须定义 run(ctx)，返回 lib.common.finish() 构造的结果字典。
报告输出到 apitest/reports/（已在 .gitignore 中，不提交）。

用法：python apitest/run_all.py
可选环境变量：APITEST_BASE（默认 http://localhost:8080）、APITEST_MYSQL_CONTAINER 等，见 lib/common.py。
"""
import importlib.util
import sys
import time
import traceback
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

from lib import report  # noqa: E402
from lib.common import BASE, Ctx, finish, make_result  # noqa: E402

METHODS = {"GET", "POST", "PUT", "DELETE", "PATCH"}


def discover():
    cases = []
    for p in sorted((HERE / "cases").rglob("*.py")):
        if p.stem not in METHODS:
            continue
        rel = p.relative_to(HERE / "cases")
        segs = list(rel.parts[:-1])
        url = "/" + "/".join("{" + s[1:] + "}" if s.startswith("_") else s for s in segs)
        cases.append((p, p.stem, url))
    # 跨接口集成用例：不映射 URL，method/url 由用例自身的 make_result 提供。
    for p in sorted((HERE / "integrations").rglob("*.py")):
        if p.name.startswith("_"):
            continue
        cases.append((p, "", ""))
    return cases


def load_module(path):
    name = "case_" + "_".join(path.relative_to(HERE).with_suffix("").parts)
    spec = importlib.util.spec_from_file_location(name, path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def main():
    cases = discover()
    if not cases:
        print("未发现任何用例（cases/**/<METHOD>.py）")
        return 1
    print(f"目标: {BASE}  用例数: {len(cases)}\n")
    ctx = Ctx()
    results = []
    t0 = time.monotonic()
    for path, method, url in cases:
        started = time.monotonic()
        try:
            result = load_module(path).run(ctx)
            result.setdefault("method", method)
            result.setdefault("url", url)
        except Exception:
            # 集成用例在产出结果前异常时 method/url 为空，退化为文件路径以免报告出现无名条目。
            name = url or path.relative_to(HERE).as_posix()
            result = finish(make_result(method or "集成", name), [], started, traceback.format_exc(limit=3))
        results.append(result)
        mark = "PASS" if result["passed"] else "FAIL"
        print(f"[{mark}] {result['name']}  ({result['duration_ms']} ms)")
        if not result["passed"]:
            for a in result["assertions"]:
                if not a["passed"]:
                    print(f"    x {a['name']}: {a['detail'][:200]}")
            if result["error"]:
                print("    异常: " + result["error"].strip().splitlines()[-1][:200])
    total_ms = round((time.monotonic() - t0) * 1000)
    out = report.write(results, BASE, time.strftime("%Y-%m-%d %H:%M:%S"), total_ms, HERE / "reports")
    passed = sum(1 for r in results if r["passed"])
    print(f"\n通过 {passed}/{len(results)}，总耗时 {total_ms} ms")
    print(f"HTML 报告: {out}")
    return 0 if passed == len(results) else 1


if __name__ == "__main__":
    sys.exit(main())
