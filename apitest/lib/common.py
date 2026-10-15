"""apitest 公共底座：配置加载、HTTP 客户端、数据库查询、SSE/multipart 工具。

配置来源（优先级从高到低）：进程环境变量 > 仓库根 .env > 内置默认值。
可通过 APITEST_BASE 指向任意部署（默认 http://localhost:8080，即本机 Nginx）。
数据库核对通过 `docker exec <mysql容器> mysql` 完成，不依赖任何 Python 第三方包。
"""
from __future__ import annotations

import base64
import json
import os
import subprocess
import time
import urllib.error
import urllib.request
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]  # 仓库根（apitest/lib/ 的上两级）


def _load_dotenv() -> dict:
    env = {}
    p = ROOT / ".env"
    if p.is_file():
        for line in p.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if line and not line.startswith("#") and "=" in line:
                k, v = line.split("=", 1)
                env[k.strip()] = v.strip()
    return env


_DOTENV = _load_dotenv()


def cfg(key: str, default: str = "") -> str:
    return os.environ.get(key) or _DOTENV.get(key) or default


BASE = os.environ.get("APITEST_BASE", "http://localhost:8080").rstrip("/")
MYSQL_CONTAINER = os.environ.get("APITEST_MYSQL_CONTAINER", "campusclaw-mysql")
BACKEND_CONTAINER = os.environ.get("APITEST_BACKEND_CONTAINER", "campusclaw-backend")
QDRANT_NETWORK = os.environ.get("APITEST_QDRANT_NETWORK", "campusclaw_ccnet")
MYSQL_USER = cfg("MYSQL_USER", "campusclaw")
MYSQL_PASSWORD = cfg("MYSQL_PASSWORD")
MYSQL_DATABASE = cfg("MYSQL_DATABASE", "campusclaw")

TEACHER = (cfg("SEED_TEACHER_A_USERNAME", "teacherA"), cfg("SEED_TEACHER_A_PASSWORD"))
STUDENT = (cfg("SEED_STUDENT_A1_USERNAME", "studentA1"), cfg("SEED_STUDENT_A1_PASSWORD"))

TAG = uuid.uuid4().hex[:8]  # 本轮测试的唯一标记，用于构造不冲突的测试数据


class Check:
    """单条断言记录：名称、是否通过、证据细节。"""

    def __init__(self, name, passed, detail=""):
        self.name, self.passed, self.detail = name, bool(passed), str(detail)

    def to_dict(self):
        return {"name": self.name, "passed": self.passed, "detail": self.detail}


def _unescape_batch(s: str) -> str:
    """还原 mysql -B 批处理模式的字段转义（\\\\ -> \\，\\n -> 换行，\\t -> 制表，\\0 -> NUL）。"""
    out = []
    i = 0
    while i < len(s):
        if s[i] == "\\" and i + 1 < len(s):
            nxt = s[i + 1]
            if nxt == "n":
                out.append("\n")
                i += 2
                continue
            if nxt == "t":
                out.append("\t")
                i += 2
                continue
            if nxt == "\\":
                out.append("\\")
                i += 2
                continue
            if nxt == "0":
                out.append("\0")
                i += 2
                continue
        out.append(s[i])
        i += 1
    return "".join(out)


class Ctx:
    """用例共享上下文：HTTP、登录 token 缓存、数据库查询。"""

    def __init__(self):
        self._tokens = {}

    def http(self, method, path, token=None, json_body=None, raw=None, headers=None, timeout=60):
        h = {"Accept": "application/json"}
        h.update(headers or {})
        data = raw
        if json_body is not None:
            data = json.dumps(json_body).encode("utf-8")
            h["Content-Type"] = "application/json"
        if token:
            h["Authorization"] = "Bearer " + token
        req = urllib.request.Request(BASE + path, data=data, method=method, headers=h)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                return resp.status, dict(resp.headers), resp.read()
        except urllib.error.HTTPError as e:
            return e.code, dict(e.headers or {}), e.read()

    def login(self, account):
        """登录并缓存 token；account 为 (username, password) 元组。"""
        if account not in self._tokens:
            st, _, body = self.http(
                "POST", "/api/sessions",
                json_body={"username": account[0], "password": account[1]},
            )
            if st != 200:
                raise RuntimeError(f"登录失败 {account[0]}: HTTP {st} {body[:200]!r}")
            self._tokens[account] = json.loads(body)["accessToken"]
        return self._tokens[account]

    def db(self, sql):
        """在 MySQL 容器内执行 SQL，返回行列表（每行 list[str]，NULL 呈现为 'NULL'）。

        以字节流捕获输出再按 UTF-8 解码，避免 Windows 控制台 GBK 把中文查乱。
        """
        cmd = [
            "docker", "exec", "-e", f"MYSQL_PWD={MYSQL_PASSWORD}", MYSQL_CONTAINER,
            "mysql", f"-u{MYSQL_USER}", "--default-character-set=utf8mb4",
            MYSQL_DATABASE, "-N", "-B", "-e", sql,
        ]
        out = subprocess.run(cmd, capture_output=True, timeout=30)
        if out.returncode != 0:
            raise RuntimeError("DB 执行失败: " + out.stderr.decode("utf-8", "replace").strip()[:300])
        text = out.stdout.decode("utf-8", "replace")
        return [[_unescape_batch(f) for f in line.split("\t")] for line in text.splitlines()]

    def db_one(self, sql):
        rows = self.db(sql)
        return rows[0] if rows else None


def sql_str(s: str) -> str:
    """最小 SQL 字符串字面量转义（测试数据均为本进程生成）。"""
    return "'" + s.replace("\\", "\\\\").replace("'", "''") + "'"


def jwt_payload(token: str) -> dict:
    seg = token.split(".")[1]
    seg += "=" * (-len(seg) % 4)
    return json.loads(base64.urlsafe_b64decode(seg))


def parse_sse(body: bytes):
    """把 SSE 响应体解析为 [(event, data_dict), ...]。"""
    events = []
    for block in body.decode("utf-8", "replace").replace("\r\n", "\n").split("\n\n"):
        ev, data = None, None
        for line in block.split("\n"):
            if line.startswith("event:"):
                ev = line[6:].strip()
            elif line.startswith("data:"):
                data = line[5:].strip()
        if ev:
            try:
                payload = json.loads(data) if data else {}
            except json.JSONDecodeError:
                payload = {"_raw": data}
            events.append((ev, payload))
    return events


def multipart_file(field, filename, content: bytes, content_type="text/markdown"):
    """构造单文件 multipart 表单体，返回 (body, Content-Type)。"""
    boundary = "----apitest" + uuid.uuid4().hex
    body = b"".join([
        f"--{boundary}\r\n".encode(),
        f'Content-Disposition: form-data; name="{field}"; filename="{filename}"\r\n'.encode(),
        f"Content-Type: {content_type}\r\n\r\n".encode(),
        content,
        f"\r\n--{boundary}--\r\n".encode(),
    ])
    return body, f"multipart/form-data; boundary={boundary}"


def qdrant_delete_document(document_id: int):
    """按 payload document_id 删除向量点（检索用例的清理步骤；Qdrant 仅内网可达）。"""
    body = json.dumps({"filter": {"must": [{"key": "document_id", "match": {"value": document_id}}]}})
    subprocess.run(
        ["docker", "run", "--rm", "--network", QDRANT_NETWORK, "curlimages/curl:8.10.1",
         "-s", "-X", "POST", "http://qdrant:6333/collections/chunks/points/delete",
         "-H", "Content-Type: application/json", "-d", body],
        capture_output=True, timeout=60,
    )


def make_result(method, url):
    return {"name": f"{method} {url}", "method": method, "url": url,
            "assertions": [], "passed": True, "duration_ms": 0, "error": ""}


def finish(result, checks, started, error=""):
    result["assertions"] = [c.to_dict() for c in checks]
    result["passed"] = (error == "") and all(c.passed for c in checks)
    result["duration_ms"] = round((time.monotonic() - started) * 1000)
    result["error"] = error
    return result
