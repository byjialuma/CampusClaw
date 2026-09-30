#!/bin/sh
# CampusClaw 知识问答：班级隔离与可用性端到端矩阵（JWT 版本）。
# 前置：backend 以 EMBEDDING_PROVIDER=stub 连接真实 Qdrant，种子材料已回填。
# 用法（在 compose 网络内运行）：
#   docker run --rm --network campusclaw_ccnet \
#     -v "$PWD/deploy/tests:/tests" -e BASE=http://backend:8080 curlimages/curl:8.10.1 sh /tests/e2e-qa-matrix.sh
# 经 Nginx 验收时：-e BASE=http://nginx
set -u

BASE="${BASE:-http://backend:8080}"
T_USER="${SEED_TEACHER_A_USERNAME:-teacherA}"
T_PW="${SEED_TEACHER_A_PASSWORD:-change_me_teacher_a}"
A1_USER="${SEED_STUDENT_A1_USERNAME:-studentA1}"
A1_PW="${SEED_STUDENT_A1_PASSWORD:-change_me_student_a1}"
B1_USER="${SEED_STUDENT_B1_USERNAME:-studentB1}"
B1_PW="${SEED_STUDENT_B1_PASSWORD:-change_me_student_b1}"

BODY=/tmp/ccqa_body.txt
PASS=0
FAIL=0

T_TOKEN=""
A1_TOKEN=""
B1_TOKEN=""

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

get() { # path [token]
  if [ -n "${2:-}" ]; then
    curl -s -o "$BODY" -w '%{http_code}' -H "Authorization: Bearer $2" "$BASE$1"
  else
    curl -s -o "$BODY" -w '%{http_code}' "$BASE$1"
  fi
}
post_json() { # path token json
  curl -s -o "$BODY" -w '%{http_code}' \
    -H 'Content-Type: application/json' \
    -H "Authorization: Bearer ${2:-}" \
    -X POST -d "$3" "$BASE$1"
}

countof() { # pattern  （在 $BODY 中出现次数）
  grep -o "$1" "$BODY" | wc -l | tr -d ' '
}

login_and_save_token() { # var_name username password
  code=$(curl -s -o "$BODY" -w '%{http_code}' \
    -H 'Content-Type: application/json' \
    -X POST -d "{\"username\":\"$2\",\"password\":\"$3\"}" "$BASE/api/sessions")
  if [ "$code" != "200" ]; then
    echo "FAIL: 登录 $2 失败: $code"
    cat "$BODY"
    FAIL=$((FAIL + 1))
    return 1
  fi
  tok=$(sed -n 's/.*"accessToken":"\([^"]*\)".*/\1/p' "$BODY")
  if [ -z "$tok" ]; then
    echo "FAIL: 登录响应缺少 accessToken"
    FAIL=$((FAIL + 1))
    return 1
  fi
  eval "$1=\"$tok\""
  echo "PASS: $2 登录成功并获取 token"
  PASS=$((PASS + 1))
}

echo "== 健康检查 =="
check "GET /health 200" 200 "$(get /health)"

echo "== 登录 =="
login_and_save_token T_TOKEN "$T_USER" "$T_PW"
login_and_save_token A1_TOKEN "$A1_USER" "$A1_PW"
login_and_save_token B1_TOKEN "$B1_USER" "$B1_PW"

echo "== 未认证 / 参数校验 =="
check "未登录问答 401" 401 "$(curl -s -o "$BODY" -w '%{http_code}' \
  -H 'Content-Type: application/json' -X POST -d '{"question":"test"}' "$BASE/api/qa")"
check "空问题 400" 400 "$(post_json /api/qa "$T_TOKEN" '{"question":""}')"
check "超长问题 400" 400 "$(post_json /api/qa "$T_TOKEN" "{\"question\":\"$(printf 'a%.0s' $(seq 1 501))\"}")"

echo "== 等待种子材料回填 ready =="
wait_ready() { # token expectCount label
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
wait_ready "$T_TOKEN" 2 "A 班两份种子材料" && { echo "PASS: A 班种子材料全部 ready"; PASS=$((PASS+1)); }
wait_ready "$B1_TOKEN" 1 "B 班 PDF" && { echo "PASS: B 班 PDF ready"; PASS=$((PASS+1)); }

# 记录各班材料 id 集合。
get /api/materials "$T_TOKEN" >/dev/null
A_IDS=$(grep -o '"id":[0-9]*' "$BODY" | cut -d: -f2 | sort -n -u | tr '\n' ' ')
get /api/materials "$B1_TOKEN" >/dev/null
B_IDS=$(grep -o '"id":[0-9]*' "$BODY" | cut -d: -f2 | sort -n -u | tr '\n' ' ')
echo "A 班材料 ids: $A_IDS / B 班材料 ids: $B_IDS"

echo "== 班级隔离：问答 =="
# A 班教师提问「集合」
check "A 班教师问答 200" 200 "$(post_json /api/qa "$T_TOKEN" '{"question":"什么是集合？"}')"
A_CITATIONS=$(countof '"documentId":')
if [ "$A_CITATIONS" -gt 0 ]; then
  echo "PASS: A 班问答有 $A_CITATIONS 条引用"; PASS=$((PASS+1))
else
  echo "FAIL: A 班问答零引用: $(cat "$BODY")"; FAIL=$((FAIL+1))
fi
# 断言所有引用都属于 A 班
GOT_A_IDS=$(grep -o '"documentId":[0-9]*' "$BODY" | cut -d: -f2 | sort -n -u | tr '\n' ' ')
subset=1
for id in $GOT_A_IDS; do
  case " $A_IDS" in *" $id "*) ;; *) subset=0 ;; esac
done
[ "$subset" = 1 ] && { echo "PASS: A 班问答引用全部属于 A 班: $GOT_A_IDS"; PASS=$((PASS+1)); } \
  || { echo "FAIL: A 班问答引用混入外班 id: $GOT_A_IDS"; FAIL=$((FAIL+1)); }
# 每条引用必须带来源（文件名 + snippet + locator），无无来源条目。
[ "$A_CITATIONS" = "$(countof '"fileName":"')" ] && [ "$A_CITATIONS" = "$(countof '"snippet":"')" ] && [ "$A_CITATIONS" = "$(countof '"locator":{')" ] \
  && { echo "PASS: A 班每条引用均含 fileName + snippet + locator"; PASS=$((PASS+1)); } \
  || { echo "FAIL: A 班存在无来源条目"; FAIL=$((FAIL+1)); }

# B 班学生提问同一问题
check "B 班学生问答 200" 200 "$(post_json /api/qa "$B1_TOKEN" '{"question":"什么是集合？"}')"
B_CITATIONS=$(countof '"documentId":')
# B 班可能零召回（中文材料不在 B 班），也可能有英文 Sets 相关命中（score>0 时）。
# 关键断言：引用全部属于 B 班。
if [ "$B_CITATIONS" -gt 0 ]; then
  GOT_B_IDS=$(grep -o '"documentId":[0-9]*' "$BODY" | cut -d: -f2 | sort -n -u | tr '\n' ' ')
  subset=1
  for id in $GOT_B_IDS; do
    case " $B_IDS" in *" $id "*) ;; *) subset=0 ;; esac
  done
  [ "$subset" = 1 ] && { echo "PASS: B 班问答引用全部属于 B 班: $GOT_B_IDS"; PASS=$((PASS+1)); } \
    || { echo "FAIL: B 班问答引用混入外班 id: $GOT_B_IDS"; FAIL=$((FAIL+1)); }
else
  echo "PASS: B 班问答零引用（中文材料不在 B 班）"; PASS=$((PASS+1))
fi

echo "== 伪造班级参数无效 =="
# 在问答请求中附加伪造 class_id 参数，服务端必须忽略。
code=$(curl -s -o "$BODY" -w '%{http_code}' \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $B1_TOKEN" \
  -X POST -d '{"question":"什么是集合？","class_id":1,"classId":1}' \
  "$BASE/api/qa")
check "B1 伪造 class_id 仍 200" 200 "$code"
B_CITATIONS_FORGED=$(countof '"documentId":')
if [ "$B_CITATIONS_FORGED" -gt 0 ]; then
  GOT_B_IDS_FORGED=$(grep -o '"documentId":[0-9]*' "$BODY" | cut -d: -f2 | sort -n -u | tr '\n' ' ')
  subset=1
  for id in $GOT_B_IDS_FORGED; do
    case " $B_IDS" in *" $id "*) ;; *) subset=0 ;; esac
  done
  [ "$subset" = 1 ] && { echo "PASS: B1 伪造参数后引用仍锁定 B 班"; PASS=$((PASS+1)); } \
    || { echo "FAIL: B1 伪造参数后引用异常: $GOT_B_IDS_FORGED"; FAIL=$((FAIL+1)); }
else
  echo "PASS: B1 伪造参数后零引用（仍锁定 B 班）"; PASS=$((PASS+1))
fi

echo "== 学生 A1 问答 =="
check "A1 学生问答 200" 200 "$(post_json /api/qa "$A1_TOKEN" '{"question":"什么是集合？"}')"
A1_CITATIONS=$(countof '"documentId":')
if [ "$A1_CITATIONS" -gt 0 ]; then
  GOT_A1_IDS=$(grep -o '"documentId":[0-9]*' "$BODY" | cut -d: -f2 | sort -n -u | tr '\n' ' ')
  subset=1
  for id in $GOT_A1_IDS; do
    case " $A_IDS" in *" $id "*) ;; *) subset=0 ;; esac
  done
  [ "$subset" = 1 ] && { echo "PASS: A1 问答引用全部属于 A 班"; PASS=$((PASS+1)); } \
    || { echo "FAIL: A1 问答引用混入外班 id: $GOT_A1_IDS"; FAIL=$((FAIL+1)); }
else
  echo "FAIL: A1 问答零引用: $(cat "$BODY")"; FAIL=$((FAIL+1))
fi

echo
echo "RESULT: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
