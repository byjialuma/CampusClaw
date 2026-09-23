#!/bin/sh
# 9.2 故障演练：Qdrant 停服期间 /health 与 /api/search 的故障映射。
# 前置：docker compose stop qdrant
# 恢复：docker compose start qdrant 后本脚本以 RECOVERED=1 重跑应全部 200。
set -u
BASE="${BASE:-http://backend:8080}"
PASS=0
FAIL=0

check() { # desc expected actual
  if [ "$2" = "$3" ]; then
    echo "PASS: $1 ($3)"
    PASS=$((PASS + 1))
  else
    echo "FAIL: $1 (expected=$2 actual=$3)"
    FAIL=$((FAIL + 1))
  fi
}

echo "== /health =="
H_BODY=$(curl -s -o /tmp/fd_h -w '%{http_code}' "$BASE/health")
H_JSON=$(cat /tmp/fd_h)
echo "body=$H_JSON"
if [ "${RECOVERED:-0}" = "1" ]; then
  check "health 恢复 200" 200 "$H_BODY"
  echo "$H_JSON" | grep -q '"vector":"up"' && { echo "PASS: health vector=up"; PASS=$((PASS + 1)); } || { echo "FAIL: health vector 非 up: $H_JSON"; FAIL=$((FAIL + 1)); }
else
  case "$H_BODY" in
    5??) echo "PASS: health 故障 5xx ($H_BODY)"; PASS=$((PASS + 1)) ;;
    *) echo "FAIL: health 应 5xx，实际 $H_BODY"; FAIL=$((FAIL + 1)) ;;
  esac
  echo "$H_JSON" | grep -q '"vector":"down"' && { echo "PASS: health vector=down"; PASS=$((PASS + 1)); } || { echo "FAIL: health vector 非 down: $H_JSON"; FAIL=$((FAIL + 1)); }
fi

echo "== 登录 =="
L_CODE=$(curl -s -c /tmp/fd_c -o /dev/null -w '%{http_code}' -X POST "$BASE/api/sessions" \
  -H 'Content-Type: application/json' \
  -d '{"username":"teacherA","password":"change_me_teacher_a"}')
check "教师登录 200" 200 "$L_CODE"

echo "== /api/search =="
S_CODE=$(curl -s -b /tmp/fd_c -o /tmp/fd_s -w '%{http_code}' "$BASE/api/search?q=%E9%9B%86%E5%90%88")
S_JSON=$(cat /tmp/fd_s)
echo "body=$S_JSON"
if [ "${RECOVERED:-0}" = "1" ]; then
  check "search 恢复 200" 200 "$S_CODE"
else
  case "$S_CODE" in
    5??) echo "PASS: search 故障 5xx ($S_CODE)"; PASS=$((PASS + 1)) ;;
    *) echo "FAIL: search 应 5xx，实际 $S_CODE"; FAIL=$((FAIL + 1)) ;;
  esac
  if echo "$S_JSON" | grep -Eq 'http://|https://|6333|qdrant'; then
    echo "FAIL: 响应泄漏内部地址: $S_JSON"
    FAIL=$((FAIL + 1))
  else
    echo "PASS: 响应不含内部地址/端口"
    PASS=$((PASS + 1))
  fi
fi

echo
echo "RESULT: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
