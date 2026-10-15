"""HTML 报告渲染与落盘。报告写入 apitest/reports/（已加入 .gitignore，不入库）。"""
import html
import time
from pathlib import Path


def _esc(s):
    return html.escape(str(s))


def render(results, base, started_at, total_ms):
    passed = sum(1 for r in results if r["passed"])
    failed = len(results) - passed
    rate = round(passed / len(results) * 100, 1) if results else 0.0
    blocks = []
    for r in results:
        cls = "pass" if r["passed"] else "fail"
        asserts = "".join(
            f'<tr class="a {"pass" if a["passed"] else "fail"}">'
            f'<td class="mark">{"✓" if a["passed"] else "✗"}</td>'
            f"<td>{_esc(a['name'])}</td>"
            f"<td><code>{_esc(a['detail'])[:800]}</code></td></tr>"
            for a in r["assertions"])
        if r["error"]:
            asserts += (f'<tr class="a fail"><td class="mark">✗</td><td>异常</td>'
                        f"<td><code>{_esc(r['error'])[:800]}</code></td></tr>")
        blocks.append(f"""
<section class="case {cls}">
  <h2><span class="badge {cls}">{"PASS" if r["passed"] else "FAIL"}</span>
      <span class="method">{_esc(r["method"])}</span> <code>{_esc(r["url"])}</code>
      <span class="dur">{r["duration_ms"]} ms</span></h2>
  <table>{asserts}</table>
</section>""")
    return f"""<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8">
<title>CampusClaw 接口测试报告</title>
<style>
 body{{font-family:-apple-system,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif;margin:0;background:#faf5f6;color:#352a2b}}
 header{{background:#94070a;color:#fff;padding:18px 28px}}
 header h1{{margin:0;font-size:20px}}
 header .meta{{font-size:13px;opacity:.9;margin-top:6px}}
 .cards{{display:flex;gap:14px;padding:20px 28px 0}}
 .card{{background:#fff;border:1px solid #e8d8d9;border-radius:10px;padding:12px 22px;box-shadow:0 1px 3px rgba(148,7,10,.06)}}
 .card b{{font-size:22px;display:block}}
 .card.pass b{{color:#1a7f37}} .card.fail b{{color:#c0392b}}
 main{{padding:20px 28px 40px}}
 section.case{{background:#fff;border:1px solid #e8d8d9;border-left:5px solid #1a7f37;border-radius:10px;margin:0 0 16px;padding:12px 16px}}
 section.case.fail{{border-left-color:#c0392b}}
 section.case h2{{font-size:15px;margin:0 0 10px;display:flex;gap:10px;align-items:baseline;flex-wrap:wrap}}
 .badge{{border-radius:6px;padding:2px 8px;font-size:12px;color:#fff}}
 .badge.pass{{background:#1a7f37}} .badge.fail{{background:#c0392b}}
 .method{{color:#94070a;font-weight:700}}
 .dur{{margin-left:auto;color:#6b5b5c;font-size:12px}}
 table{{width:100%;border-collapse:collapse;font-size:13px}}
 td{{padding:5px 8px;border-top:1px solid #f0e6e7;vertical-align:top}}
 tr.a.pass .mark{{color:#1a7f37}} tr.a.fail .mark{{color:#c0392b}}
 td.mark{{width:24px;font-weight:700}}
 code{{font-family:ui-monospace,Consolas,monospace;font-size:12px;white-space:pre-wrap;word-break:break-all}}
</style></head><body>
<header><h1>CampusClaw 接口测试报告</h1>
<div class="meta">目标 {html.escape(base)} · 生成于 {html.escape(started_at)} · 总耗时 {total_ms} ms</div></header>
<div class="cards">
 <div class="card"><b>{len(results)}</b>用例总数</div>
 <div class="card pass"><b>{passed}</b>通过</div>
 <div class="card fail"><b>{failed}</b>失败</div>
 <div class="card"><b>{rate}%</b>通过率</div>
</div>
<main>{''.join(blocks)}
</main></body></html>"""


def write(results, base, started_at, total_ms, out_dir):
    out_dir = Path(out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    doc = render(results, base, started_at, total_ms)
    path = out_dir / ("report-%s.html" % time.strftime("%Y%m%d-%H%M%S"))
    path.write_text(doc, encoding="utf-8")
    (out_dir / "latest.html").write_text(doc, encoding="utf-8")
    return path
