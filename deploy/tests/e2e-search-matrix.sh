#!/bin/sh
# CampusClaw 知识库检索：班级隔离与可用性端到端矩阵。
# 前置：backend 以 EMBEDDING_PROVIDER=stub 连接真实 Qdrant，种子材料已回填。
# 用法（在 compose 网络内运行）：
#   docker run --rm --network campusclaw_ccnet \
#     -v "$PWD/deploy/tests:/tests" -e BASE=http://backend:8080 curlimages/curl:8.10.1 sh /tests/e2e-search-matrix.sh
set -u

BASE="${BASE:-http://backend:8080}"
T_USER="${SEED_TEACHER_A_USERNAME:-teacherA}"
T_PW="${SEED_TEACHER_A_PASSWORD:-change_me_teacher_a}"
B1_USER="${SEED_STUDENT_B1_USERNAME:-studentB1}"
B1_PW="${SEED_STUDENT_B1_PASSWORD:-change_me_student_b1}"

BODY=/tmp/ccs_body.txt
T_JAR=/tmp/ccs_teacher.jar
B1_JAR=/tmp/ccs_b1.jar
PASS=0
FAIL=0

check() { # desc expected actual
  if [ "$2" = "$3" ]; then
    echo "PASS: $1 ($3)"
    PASS=$((PASS + 1))
  else
    echo "FAIL: $1 (expected=$2 actual=$3)"
    cat "$BODY" 2>/dev/null
    FAIL=$((FAIL + 1))
  fi
}

get() { # path jar
  curl -s -c "$2" -o "$BODY" -w '%{http_code}' -b "$2" "$BASE$1"
}
post_json() { # path jar json
  curl -s -c "$2" -o "$BODY" -w '%{http_code}' -b "$2" \
    -H 'Content-Type: application/json' -X POST -d "$3" "$BASE$1"
}
# 带会话的 GET，查询串用原始形式传入（用于注入伪造 class_id 参数）。
getq() { # rawQuery jar
  curl -s -c "$2" -o "$BODY" -w '%{http_code}' -b "$2" "$BASE/api/search?$1"
}
countof() { # pattern  （在 $BODY 中出现次数）
  grep -o "$1" "$BODY" | wc -l | tr -d ' '
}
doc_ids() { # 输出排序去重后的 documentId 列表
  grep -o '"documentId":[0-9]*' "$BODY" | cut -d: -f2 | sort -n -u | tr '\n' ' '
}

echo "== 健康检查（MySQL + Qdrant 双探测）=="
check "GET /health 200" 200 "$(curl -s -o "$BODY" -w '%{http_code}' "$BASE/health")"
grep -q '"vector":"up"' "$BODY" \
  && { echo "PASS: health vector=up"; PASS=$((PASS+1)); } \
  || { echo "FAIL: health vector 状态: $(cat "$BODY")"; FAIL=$((FAIL+1)); }

echo "== 登录 =="
check "教师登录 200" 200 "$(post_json /api/sessions "$T_JAR" "{\"username\":\"$T_USER\",\"password\":\"$T_PW\"}")"
check "学生 B1 登录 200" 200 "$(post_json /api/sessions "$B1_JAR" "{\"username\":\"$B1_USER\",\"password\":\"$B1_PW\"}")"

echo "== 等待种子材料回填 ready =="
wait_ready() { # jar expectCount label
  i=0
  while [ "$i" -lt 60 ]; do
    get /api/materials "$1" >/dev/null
    ready=$(countof '"indexStatus":"ready"')
    failed=$(countof '"indexStatus":"failed"')
    [ "$failed" -gt 0 ] && { echo "FAIL: $2 存在 failed 材料: $(cat "$BODY")"; FAIL=$((FAIL+1)); return 1; }
    [ "$ready" -ge "$2" ] && return 0
    i=$((i + 1))
    sleep 1
  done
  echo "FAIL: $2 等待 ready 超时: $(cat "$BODY")"
  FAIL=$((FAIL + 1))
  return 1
}
wait_ready "$T_JAR" 2 "A 班两份种子材料" && { echo "PASS: A 班种子材料全部 ready"; PASS=$((PASS+1)); }
wait_ready "$B1_JAR" 1 "B 班 PDF" && { echo "PASS: B 班 PDF ready"; PASS=$((PASS+1)); }

# 记录各班材料 id 集合。
get /api/materials "$T_JAR" >/dev/null
A_IDS=$(grep -o '"id":[0-9]*' "$BODY" | cut -d: -f2 | sort -n -u | tr '\n' ' ')
get /api/materials "$B1_JAR" >/dev/null
B_IDS=$(grep -o '"id":[0-9]*' "$BODY" | cut -d: -f2 | sort -n -u | tr '\n' ' ')
echo "A 班材料 ids: $A_IDS / B 班材料 ids: $B_IDS"

echo "== 未认证 / 参数校验 =="
check "未登录检索 401" 401 "$(curl -s -o "$BODY" -w '%{http_code}' "$BASE/api/search?q=x")"
check "空查询 400" 400 "$(get '/api/search?q=' "$T_JAR")"
check "纯空白查询 400" 400 "$(get '/api/search?q=%20%20' "$T_JAR")"
LONGQ=$(printf 'a%.0s' $(seq 1 501))
check "超过 500 字 400" 400 "$(get "/api/search?q=$LONGQ" "$T_JAR")"

echo "== 班级隔离：CJK 查询「集合」 =="
check "A 班检索「集合」200" 200 "$(get '/api/search?q=%E9%9B%86%E5%90%88' "$T_JAR")"
A_HITS=$(countof '"documentId":')
if [ "$A_HITS" -gt 0 ]; then
  echo "PASS: A 班「集合」有 $A_HITS 条命中"; PASS=$((PASS+1))
else
  echo "FAIL: A 班「集合」零命中: $(cat "$BODY")"; FAIL=$((FAIL+1))
fi
GOT_A_IDS=$(doc_ids)
subset=1
for id in $GOT_A_IDS; do
  case " $A_IDS" in *" $id "*) ;; *) subset=0 ;; esac
done
[ "$subset" = 1 ] && { echo "PASS: A 班命中 documentId 全部属于 A 班: $GOT_A_IDS"; PASS=$((PASS+1)); } \
  || { echo "FAIL: A 班结果混入外班 id: $GOT_A_IDS"; FAIL=$((FAIL+1)); }
# 每条命中必须带来源（文件名 + locator），无无来源条目。
[ "$A_HITS" = "$(countof '"fileName":"')" ] && [ "$A_HITS" = "$(countof '"locator":{')" ] \
  && { echo "PASS: A 班每条命中均含 fileName 与 locator"; PASS=$((PASS+1)); } \
  || { echo "FAIL: A 班存在无来源条目"; FAIL=$((FAIL+1)); }
grep -qE '"kind":"(lines|heading)"' "$BODY" \
  && { echo "PASS: A 班命中含行号/章节溯源"; PASS=$((PASS+1)); } \
  || { echo "FAIL: A 班缺少 txt/md locator"; FAIL=$((FAIL+1)); }

check "B 班检索「集合」200（零命中）" 200 "$(get '/api/search?q=%E9%9B%86%E5%90%88' "$B1_JAR")"
[ "$(countof '"documentId":')" = 0 ] \
  && { echo "PASS: B 班对中文「集合」零召回"; PASS=$((PASS+1)); } \
  || { echo "FAIL: B 班竟然召回中文材料（跨班泄漏）: $(cat "$BODY")"; FAIL=$((FAIL+1)); }
grep -q '"results":\[\]' "$BODY" \
  && { echo "PASS: 零命中返回空数组"; PASS=$((PASS+1)); } \
  || { echo "FAIL: 空结果形态错误: $(cat "$BODY")"; FAIL=$((FAIL+1)); }

echo "== 班级隔离：Latin 查询「Sets」 =="
check "B 班检索 Sets 200" 200 "$(get '/api/search?q=Sets' "$B1_JAR")"
B_HITS=$(countof '"documentId":')
[ "$B_HITS" -gt 0 ] \
  && { echo "PASS: B 班 Sets 有 $B_HITS 条命中"; PASS=$((PASS+1)); } \
  || { echo "FAIL: B 班 Sets 零命中: $(cat "$BODY")"; FAIL=$((FAIL+1)); }
GOT_B_IDS=$(doc_ids)
for id in $B_IDS; do WANT_B_ID="$id"; done
[ "$GOT_B_IDS" = "$WANT_B_ID " ] \
  && { echo "PASS: B 班命中 documentId 仅为 B 班 PDF ($WANT_B_ID)"; PASS=$((PASS+1)); } \
  || { echo "FAIL: B 班结果 id=$GOT_B_IDS 不等于 $WANT_B_ID"; FAIL=$((FAIL+1)); }
grep -q '"kind":"page"' "$BODY" && grep -q '"page":1' "$BODY" \
  && { echo "PASS: B 班 PDF 命中带第 1 页页码溯源"; PASS=$((PASS+1)); } \
  || { echo "FAIL: B 班 PDF 缺少页码 locator: $(cat "$BODY")"; FAIL=$((FAIL+1)); }

check "A 班检索 Sets 200（零命中）" 200 "$(get '/api/search?q=Sets' "$T_JAR")"
[ "$(countof '"documentId":')" = 0 ] \
  && { echo "PASS: A 班对 Sets 零召回（两班结果集合互不相交）"; PASS=$((PASS+1)); } \
  || { echo "FAIL: A 班召回了 B 班英文 PDF（跨班泄漏）: $(cat "$BODY")"; FAIL=$((FAIL+1)); }

echo "== 伪造班级参数无效 =="
getq 'q=Sets&class_id=1&classId=1' "$B1_JAR" >/dev/null
[ "$(doc_ids)" = "$B_IDS" ] \
  && { echo "PASS: B1 伪造 class_id=1 仍锁定 B 班"; PASS=$((PASS+1)); } \
  || { echo "FAIL: B1 伪造参数后结果异常: $(doc_ids)"; FAIL=$((FAIL+1)); }
getq 'q=%E9%9B%86%E5%90%88&class_id=2' "$T_JAR" >/dev/null
GOT=$(doc_ids)
subset=1
for id in $GOT; do
  case " $A_IDS" in *" $id "*) ;; *) subset=0 ;; esac
done
[ "$subset" = 1 ] && [ -n "$GOT" ] \
  && { echo "PASS: 教师伪造 class_id=2 仍锁定 A 班"; PASS=$((PASS+1)); } \
  || { echo "FAIL: 教师伪造参数后结果异常: $GOT"; FAIL=$((FAIL+1)); }

echo "== 网络拓扑：Qdrant 仅内网可见 =="
# compose 网络内部可达。
INNER=$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 http://qdrant:6333/healthz)
check "内网 qdrant:6333 可达 200" 200 "$INNER"
# 宿主机发布端口必须不存在（host.docker.internal 经 Docker Desktop NAT 访问宿主）。
for p in 6333 6334; do
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "http://host.docker.internal:$p/healthz" 2>/dev/null)
  code=${code:-000}
  if [ "$code" = "000" ]; then
    echo "PASS: 宿主端口 $p 不可达"; PASS=$((PASS+1))
  else
    echo "FAIL: 宿主端口 $p 竟然可达（Qdrant 不得发布端口）code=$code"; FAIL=$((FAIL+1))
  fi
done

echo
echo "RESULT: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
