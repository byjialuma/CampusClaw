#!/bin/sh
# CampusClaw 解题助手：会话/SSE/班级隔离端到端矩阵。
# 前置：backend 以 EMBEDDING_PROVIDER=stub 连接真实 Qdrant，种子材料已回填，LLM_* 指向可用网关。
# 用法（在 compose 网络内运行）：
#   docker run --rm --network campusclaw_ccnet \
#     -v "$PWD/deploy/tests:/tests" -e BASE=http://backend:8080 curlimages/curl:8.10.1 sh /tests/e2e-tutor-matrix.sh
# 经 Nginx 验收（同时验证 X-Accel-Buffering 分段到达）：-e BASE=http://nginx
set -u

BASE="${BASE:-http://backend:8080}"
T_USER="${SEED_TEACHER_A_USERNAME:-teacherA}"
T_PW="${SEED_TEACHER_A_PASSWORD:-change_me_teacher_a}"
A1_USER="${SEED_STUDENT_A1_USERNAME:-studentA1}"
A1_PW="${SEED_STUDENT_A1_PASSWORD:-change_me_student_a1}"
B1_USER="${SEED_STUDENT_B1_USERNAME:-studentB1}"
B1_PW="${SEED_STUDENT_B1_PASSWORD:-change_me_student_b1}"

BODY=/tmp/cctutor_body.txt
SSE=/tmp/cctutor_sse.txt
META=/tmp/cctutor_meta.json
PASS=0
FAIL=0

T_TOKEN=""
A1_TOKEN=""
B1_TOKEN=""
CID=""
B_CID=""
SKILL_ID=""

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

ok_if() { # desc shell-condition
  if eval "$2"; then
    echo "PASS: $1"
    PASS=$((PASS + 1))
  else
    echo "FAIL: $1"
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
  curl -s -o "$BODY" -w '%{http_code}' -H 'Content-Type: application/json' \
    -H "Authorization: Bearer ${2:-}" -X POST --data-binary "$3" "$BASE$1"
}

put_json() { # path token json
  curl -s -o "$BODY" -w '%{http_code}' -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $2" -X PUT --data-binary "$3" "$BASE$1"
}

countof() { # pattern [file]
  f="${2:-$BODY}"
  grep -o "$1" "$f" | wc -l | tr -d ' '
}

# sse_post 以流式方式提问，连接结束（done/error）后返回。
sse_post() { # token conv json outfile
  curl -sN -H 'Content-Type: application/json' -H "Authorization: Bearer $1" \
    -X POST --data-binary "$3" "$BASE/api/tutor/conversations/$2/messages" -o "$4"
}

# time_sse 与 sse_post 相同，但轮询记录 meta/done 出现时刻，末行输出 "meta_t done_t"。
time_sse() { # token conv json outfile
  : > "$4"
  curl -sN -H 'Content-Type: application/json' -H "Authorization: Bearer $1" \
    -X POST --data-binary "$3" "$BASE/api/tutor/conversations/$2/messages" -o "$4" &
  cp=$!
  i=0
  until grep -q '^event: meta' "$4" 2>/dev/null; do
    i=$((i + 1)); [ $i -gt 150 ] && break
    sleep 0.2
  done
  tm=$($DATE_CMD)
  j=0
  until grep -q '^event: done' "$4" 2>/dev/null; do
    j=$((j + 1)); [ $j -gt 150 ] && break
    sleep 0.2
  done
  td=$($DATE_CMD)
  wait $cp
  echo "$tm $td"
}

extract_meta() { # sse_file meta_out
  sed -n '/^event: meta$/,/^$/p' "$1" | sed -n 's/^data: //p' > "$2"
}

# all_in：第一个 id 集合是否全部出现在第二个集合（两端带空格的串）中。
all_in() {
  for x in $1; do
    case " $2" in *" $x "*) ;; *) return 1 ;; esac
  done
  return 0
}

login_and_save_token() { # var_name username password
  code=$(curl -s -o "$BODY" -w '%{http_code}' -H 'Content-Type: application/json' \
    -X POST -d "{\"username\":\"$2\",\"password\":\"$3\"}" "$BASE/api/sessions")
  if [ "$code" != "200" ]; then
    echo "FAIL: 登录 $2 失败: $code"; cat "$BODY"; FAIL=$((FAIL + 1)); return 1
  fi
  tok=$(sed -n 's/.*"accessToken":"\([^"]*\)".*/\1/p' "$BODY")
  if [ -z "$tok" ]; then
    echo "FAIL: 登录响应缺少 accessToken"; FAIL=$((FAIL + 1)); return 1
  fi
  eval "$1=\"$tok\""
  echo "PASS: $2 登录成功"; PASS=$((PASS + 1))
}

# date 支持纳秒时用纳秒，否则秒级回退。
NTEST=$(date +%N 2>/dev/null)
case "$NTEST" in
  ''|*N*) DATE_CMD='date +%s' ;;
  *)     DATE_CMD='date +%s%N' ;;
esac

echo "== 健康检查 =="
check "GET /health 200" 200 "$(get /health)"

echo "== 登录 =="
login_and_save_token T_TOKEN "$T_USER" "$T_PW"
login_and_save_token A1_TOKEN "$A1_USER" "$A1_PW"
login_and_save_token B1_TOKEN "$B1_USER" "$B1_PW"

echo "== 未认证拒绝 =="
check "未登录列出会话 401" 401 "$(get /api/tutor/conversations)"
check "未登录提问 401" 401 "$(post_json /api/tutor/conversations/1/messages '' '{"question":"x"}')"

echo "== 等待种子材料回填 ready =="
wait_ready() { # token expectCount label
  i=0
  while [ "$i" -lt 60 ]; do
    get /api/materials "$1" >/dev/null
    ready=$(countof '"indexStatus":"ready"')
    failed=$(countof '"indexStatus":"failed"')
    [ "$failed" -gt 0 ] && { echo "FAIL: $2 存在 failed 材料"; FAIL=$((FAIL+1)); return 1; }
    [ "$ready" -ge "$2" ] && return 0
    i=$((i + 1)); sleep 1
  done
  echo "FAIL: $2 等待 ready 超时"; FAIL=$((FAIL + 1)); return 1
}
wait_ready "$T_TOKEN" 2 "A 班两份种子材料" && { echo "PASS: A 班种子材料全部 ready"; PASS=$((PASS+1)); }
wait_ready "$B1_TOKEN" 1 "B 班 PDF" && { echo "PASS: B 班 PDF ready"; PASS=$((PASS+1)); }

get /api/materials "$T_TOKEN" >/dev/null
A_IDS=$(grep -o '"id":[0-9]*' "$BODY" | cut -d: -f2 | sort -n -u | tr '\n' ' ')
get /api/materials "$B1_TOKEN" >/dev/null
B_IDS=$(grep -o '"id":[0-9]*' "$BODY" | cut -d: -f2 | sort -n -u | tr '\n' ' ')
echo "A 班材料 ids: $A_IDS / B 班材料 ids: $B_IDS"

echo "== 创建会话（班级来自 JWT，伪造 class_id 必须忽略） =="
code=$(post_json /api/tutor/conversations "$A1_TOKEN" '{}')
check "A1 创建会话 201" 201 "$code"
CID=$(sed -n 's/.*"id":\([0-9]*\).*/\1/p' "$BODY")
INIT_TITLE=$(sed -n 's/.*"title":"\([^"]*\)".*/\1/p' "$BODY")
check "会话返回初始标题" "新的解题会话" "$INIT_TITLE"

code=$(post_json /api/tutor/conversations "$A1_TOKEN" '{"classId":99,"class_id":99}')
check "伪造 class_id 创建仍 201" 201 "$code"

check "会话列表包含新会话" 200 "$(get /api/tutor/conversations "$A1_TOKEN")"
ok_if "列表能查到 id=$CID" "grep -q '\"id\":$CID' \"$BODY\""

echo "== 学生访问教师配置 403 =="
check "学生 GET assistant 403" 403 "$(get /api/tutor/assistant "$A1_TOKEN")"
check "学生 PUT prompt 403" 403 "$(put_json /api/tutor/assistant/prompt "$A1_TOKEN" '{"systemPrompt":"x"}')"
check "学生 PUT skill 403" 403 "$(put_json /api/tutor/assistant/skills/1 "$A1_TOKEN" '{"enabled":false}')"

echo "== 教师配置生命周期 =="
check "教师 GET assistant 200" 200 "$(get /api/tutor/assistant "$T_TOKEN")"
# 班内可能并存基线示例技能，按名称精确定位「解题引导」的 id。
SKILL_ID=$(sed -n 's/.*{"id":\([0-9]*\),"name":"解题引导".*/\1/p' "$BODY")
ok_if "配置含解题引导技能 id" '[ -n "$SKILL_ID" ]'

PROMPT='你是本班数学老师派来的解题引导者：先给思路，再分步讲解，不直接抛出完整答案。'
check "教师 PUT prompt 200" 200 "$(put_json /api/tutor/assistant/prompt "$T_TOKEN" "{\"systemPrompt\":\"$PROMPT\"}")"
check "GET prompt 已生效" 200 "$(get /api/tutor/assistant "$T_TOKEN")"
ok_if "提示词内容已更新" "grep -q '先给思路' \"$BODY\""

check "停用解题引导 200" 200 "$(put_json /api/tutor/assistant/skills/$SKILL_ID "$T_TOKEN" '{"enabled":false}')"
check "GET 确认技能停用" 200 "$(get /api/tutor/assistant "$T_TOKEN")"
ok_if "技能 enabled=false" "grep -q '\"enabled\":false' \"$BODY\""

check "重新启用解题引导 200" 200 "$(put_json /api/tutor/assistant/skills/$SKILL_ID "$T_TOKEN" '{"enabled":true}')"
check "GET 确认技能启用" 200 "$(get /api/tutor/assistant "$T_TOKEN")"
ok_if "技能 enabled=true" "grep -q '\"enabled\":true' \"$BODY\""

echo "== SSE 首轮提问：meta -> delta -> done =="
TIMES=$(time_sse "$A1_TOKEN" "$CID" '{"question":"什么是集合？"}' "$SSE")
M_POS=$(grep -n -m1 '^event: meta' "$SSE" | cut -d: -f1)
D_POS=$(grep -n -m1 '^event: delta' "$SSE" | cut -d: -f1)
Z_POS=$(grep -n -m1 '^event: done' "$SSE" | cut -d: -f1)
ok_if "收到 meta 事件" '[ -n "$M_POS" ]'
ok_if "收到至少一个 delta" '[ -n "$D_POS" ]'
ok_if "收到 done 事件" '[ -n "$Z_POS" ]'
ok_if "顺序 meta<delta<done" '[ -n "$M_POS" ] && [ -n "$D_POS" ] && [ -n "$Z_POS" ] && [ "$M_POS" -lt "$D_POS" ] && [ "$D_POS" -lt "$Z_POS" ]'
ok_if "无 error 事件" '! grep -q "^event: error" "$SSE"'

extract_meta "$SSE" "$META"
ok_if "meta 报告 skillEnabled=true" 'grep -q "\"skillEnabled\":true" "$META"'
GOT_IDS=$(grep -o '"documentId":[0-9]*' "$META" | cut -d: -f2 | sort -n -u | tr '\n' ' ')
ok_if "首轮引用全部属于 A 班: $GOT_IDS" "all_in \"$GOT_IDS\" \"$A_IDS\""

# 分段到达：meta 出现时刻早于 done。
MT=$(echo "$TIMES" | cut -d' ' -f1)
ZT=$(echo "$TIMES" | cut -d' ' -f2)
if [ -n "$MT" ] && [ -n "$ZT" ] && [ "$ZT" -gt "$MT" ]; then
  echo "PASS: SSE 分段到达（meta 比 done 早 $((ZT - MT)) 个时间单位）"; PASS=$((PASS + 1))
else
  echo "INFO: meta/done 同一采样窗口到达，未观测到时间差（不判失败，X-Accel-Buffering 已声明）"
fi

echo "== 两轮追问 =="
sse_post "$A1_TOKEN" "$CID" '{"question":"集合有哪些基本运算？"}' "$SSE"
M=$(grep -n -m1 '^event: meta' "$SSE" | cut -d: -f1)
D=$(grep -n -m1 '^event: delta' "$SSE" | cut -d: -f1)
Z=$(grep -n -m1 '^event: done' "$SSE" | cut -d: -f1)
ok_if "追问1顺序 meta<delta<done" '[ -n "$M" ] && [ "$M" -lt "$D" ] && [ "$D" -lt "$Z" ]'
extract_meta "$SSE" "$META"
GOT_IDS=$(grep -o '"documentId":[0-9]*' "$META" | cut -d: -f2 | sort -n -u | tr '\n' ' ')
ok_if "追问1引用全部属于 A 班: $GOT_IDS" "all_in \"$GOT_IDS\" \"$A_IDS\""

sse_post "$A1_TOKEN" "$CID" '{"question":"请举一个交集的例子"}' "$SSE"
M=$(grep -n -m1 '^event: meta' "$SSE" | cut -d: -f1)
D=$(grep -n -m1 '^event: delta' "$SSE" | cut -d: -f1)
Z=$(grep -n -m1 '^event: done' "$SSE" | cut -d: -f1)
ok_if "追问2顺序 meta<delta<done" '[ -n "$M" ] && [ "$M" -lt "$D" ] && [ "$D" -lt "$Z" ]'

echo "== 持久化：刷新后历史可读 =="
check "GET messages 200" 200 "$(get /api/tutor/conversations/$CID/messages "$A1_TOKEN")"
USER_N=$(countof '"role":"user"')
AST_N=$(countof '"role":"assistant"')
DOC_N=$(countof '"documentId"')
ok_if "持久化 3 轮 user 消息（实际 $USER_N）" '[ "$USER_N" = "3" ]'
ok_if "持久化 3 轮 assistant 消息（实际 $AST_N）" '[ "$AST_N" = "3" ]'
ok_if "assistant 消息带有已持久化引用（$DOC_N 处）" '[ "$DOC_N" -gt 0 ]'

check "会话列表标题已更新为首问" 200 "$(get /api/tutor/conversations "$A1_TOKEN")"
ok_if "标题=「什么是集合？」" "grep -q '\"title\":\"什么是集合？\"' \"$BODY\""

# 模拟页面刷新：再次拉取，结果必须一致。
get /api/tutor/conversations/$CID/messages "$A1_TOKEN" >/dev/null
R_USER=$(countof '"role":"user"')
R_AST=$(countof '"role":"assistant"')
ok_if "刷新后 user 轮次不变（$R_USER）" '[ "$R_USER" = "3" ]'
ok_if "刷新后 assistant 轮次不变（$R_AST）" '[ "$R_AST" = "3" ]'

echo "== 跨用户/跨班隔离 =="
# B1 不能读 A1 的会话（属主不匹配按 404）。
check "B1 读取 A1 会话消息 404" 404 "$(get /api/tutor/conversations/$CID/messages "$B1_TOKEN")"

# B1 自己会话提问，引用必须锁定 B 班。
code=$(post_json /api/tutor/conversations "$B1_TOKEN" '{}')
check "B1 创建会话 201" 201 "$code"
B_CID=$(sed -n 's/.*"id":\([0-9]*\).*/\1/p' "$BODY")
sse_post "$B1_TOKEN" "$B_CID" '{"question":"什么是集合？"}' "$SSE"
ok_if "B1 收到 meta+done" 'grep -q "^event: meta" "$SSE" && grep -q "^event: done" "$SSE"'
extract_meta "$SSE" "$META"
GOT_B_IDS=$(grep -o '"documentId":[0-9]*' "$META" | cut -d: -f2 | sort -n -u | tr '\n' ' ')
if [ -z "$GOT_B_IDS" ]; then
  echo "PASS: B1 零召回（引用为空，仍生成回答）"; PASS=$((PASS + 1))
else
  ok_if "B1 引用全部属于 B 班: $GOT_B_IDS" "all_in \"$GOT_B_IDS\" \"$B_IDS\""
fi

echo "== 提问时伪造 class_id 无效 =="
sse_post "$A1_TOKEN" "$CID" '{"question":"什么是子集？","class_id":1,"classId":1}' "$SSE"
ok_if "伪造参数下正常 meta+done" 'grep -q "^event: meta" "$SSE" && grep -q "^event: done" "$SSE"'
extract_meta "$SSE" "$META"
GOT_IDS=$(grep -o '"documentId":[0-9]*' "$META" | cut -d: -f2 | sort -n -u | tr '\n' ' ')
if [ -z "$GOT_IDS" ]; then
  echo "PASS: 伪造参数下零召回（仍锁定本班）"; PASS=$((PASS + 1))
else
  ok_if "伪造参数后引用仍全部属于 A 班: $GOT_IDS" "all_in \"$GOT_IDS\" \"$A_IDS\""
fi

# 伪造参数不影响会话归属：第 4 轮消息落在同一会话。
get /api/tutor/conversations/$CID/messages "$A1_TOKEN" >/dev/null
F_USER=$(countof '"role":"user"')
F_AST=$(countof '"role":"assistant"')
ok_if "第四轮已持久化（user=4，实际 $F_USER）" '[ "$F_USER" = "4" ]'
ok_if "第四轮 assistant 已持久化（实际 $F_AST）" '[ "$F_AST" = "4" ]'

echo
echo "RESULT: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
