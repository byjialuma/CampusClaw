#!/bin/sh
# CampusClaw 端到端鉴权矩阵（JWT 版本）。
# 用法（在 compose 网络内运行）：
#   docker run --rm --network campusclaw_ccnet \
#     -v "$PWD/deploy/tests:/tests" -e BASE=http://backend:8080 curlimages/curl:8.10.1 sh /tests/e2e-auth-matrix.sh
# 经 Nginx 验收时：-e BASE=http://nginx
set -u

BASE="${BASE:-http://backend:8080}"
T_USER="${SEED_TEACHER_A_USERNAME:-teacherA}"
T_PW="${SEED_TEACHER_A_PASSWORD:-change_me_teacher_a}"
A1_USER="${SEED_STUDENT_A1_USERNAME:-studentA1}"
A1_PW="${SEED_STUDENT_A1_PASSWORD:-change_me_student_a1}"
B1_USER="${SEED_STUDENT_B1_USERNAME:-studentB1}"
B1_PW="${SEED_STUDENT_B1_PASSWORD:-change_me_student_b1}"

BODY=/tmp/cc_body.txt
HDR=/tmp/cc_hdr.txt
PASS=0
FAIL=0

# 全局 token 变量
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

grepbody() { grep -q "$1" "$BODY"; }

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
del() { # path token
  curl -s -o "$BODY" -w '%{http_code}' \
    -H "Authorization: Bearer $2" -X DELETE "$BASE$1"
}

# 登录并提取 JWT token 到全局变量
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
grepbody '"status":"ok"' && { echo "PASS: health body ok"; PASS=$((PASS+1)); } || { echo "FAIL: health body"; FAIL=$((FAIL+1)); }

echo "== 未认证 =="
check "未登录 GET /api/materials 401" 401 "$(get /api/materials)"
check "错误密码 401" 401 "$(curl -s -o "$BODY" -w '%{http_code}' -H 'Content-Type: application/json' -X POST -d "{\"username\":\"$T_USER\",\"password\":\"wrong\"}" "$BASE/api/sessions")"

echo "== 登录 =="
login_and_save_token T_TOKEN "$T_USER" "$T_PW"
login_and_save_token A1_TOKEN "$A1_USER" "$A1_PW"
login_and_save_token B1_TOKEN "$B1_USER" "$B1_PW"

echo "== JWT 格式与响应 =="
# 断言登录响应不含 Set-Cookie
curl -s -o "$BODY" -D "$HDR" -w '%{http_code}' \
  -H 'Content-Type: application/json' -X POST -d "{\"username\":\"$T_USER\",\"password\":\"$T_PW\"}" "$BASE/api/sessions" >/dev/null
if grep -qi 'Set-Cookie: cc_session=' "$HDR" 2>/dev/null; then
  echo "FAIL: JWT 登录不应返回会话 Cookie"
  FAIL=$((FAIL + 1))
else
  echo "PASS: JWT 登录无会话 Cookie"
  PASS=$((PASS + 1))
fi
# 断言登录响应含 accessToken 和 expiresAt
grepbody '"accessToken"' && grepbody '"expiresAt"' \
  && { echo "PASS: 登录响应含 accessToken 与 expiresAt"; PASS=$((PASS+1)); } \
  || { echo "FAIL: 登录响应缺少字段"; FAIL=$((FAIL+1)); }

echo "== 无效 token 被拒绝 =="
check "无效 token 401" 401 "$(get /api/me 'invalid-token')"

echo "== 班级隔离：列表 =="
get /api/materials "$T_TOKEN"
grepbody 'A班' && ! grepbody 'B班' \
  && { echo "PASS: 教师列表只含 A 班材料"; PASS=$((PASS+1)); } \
  || { echo "FAIL: 教师列表班级过滤"; FAIL=$((FAIL+1)); }

get /api/materials "$B1_TOKEN"
grepbody 'B班' && ! grepbody '《A班' \
  && { echo "PASS: B1 列表只含 B 班材料"; PASS=$((PASS+1)); } \
  || { echo "FAIL: B1 列表班级过滤"; FAIL=$((FAIL+1)); }
B_ID=$(grep -o '"id":[0-9]*' "$BODY" | head -1 | cut -d: -f2)
echo "B 班材料 id=$B_ID"

echo "== 跨班访问被拒 =="
check "A1 看 B 班 content 403" 403 "$(get /api/materials/$B_ID/content "$A1_TOKEN")"
check "A1 申请 B 班下载票据 403" 403 "$(post_json /api/materials/$B_ID/download-ticket "$A1_TOKEN" '')"

echo "== 学生上传 403 =="
echo 'student content' >/tmp/student.txt
code=$(curl -s -o "$BODY" -w '%{http_code}' -H "Authorization: Bearer $A1_TOKEN" \
  -F "file=@/tmp/student.txt;filename=student.txt" "$BASE/api/materials")
check "学生上传 403" 403 "$code"
grepbody '学生无权限上传文件' \
  && { echo "PASS: 403 文案正确"; PASS=$((PASS+1)); } \
  || { echo "FAIL: 403 文案"; FAIL=$((FAIL+1)); }

echo "== 教师上传 =="
printf 'not word' >/tmp/bad.docx
code=$(curl -s -o "$BODY" -w '%{http_code}' -H "Authorization: Bearer $T_TOKEN" \
  -F "file=@/tmp/bad.docx;filename=bad.docx" "$BASE/api/materials")
check "上传 docx 400" 400 "$code"

printf 'this-is-not-a-real-pdf' >/tmp/fake.pdf
code=$(curl -s -o "$BODY" -w '%{http_code}' -H "Authorization: Bearer $T_TOKEN" \
  -F "file=@/tmp/fake.pdf;filename=fake.pdf" "$BASE/api/materials")
check "伪造 pdf 400" 400 "$code"

UP_NAME='教师上传验证.txt'
echo '教师上传的正文内容' >/tmp/upload.txt
code=$(curl -s -o "$BODY" -w '%{http_code}' -H "Authorization: Bearer $T_TOKEN" \
  -F "file=@/tmp/upload.txt;filename=$UP_NAME" "$BASE/api/materials")
check "教师上传 txt 201" 201 "$code"
NEW_ID=$(grep -o '"id":[0-9]*' "$BODY" | head -1 | cut -d: -f2)

get /api/materials "$T_TOKEN"
grepbody "$UP_NAME" \
  && { echo "PASS: 上传后本班列表可查"; PASS=$((PASS+1)); } \
  || { echo "FAIL: 列表查不到新上传"; FAIL=$((FAIL+1)); }
get /api/materials "$B1_TOKEN"
if grepbody "$UP_NAME"; then
  echo "FAIL: 外班列表出现新材料"; FAIL=$((FAIL+1))
else
  echo "PASS: 外班列表不含新材料"; PASS=$((PASS+1))
fi

echo "== 内容预览 =="
code=$(curl -s -o "$BODY" -D "$HDR" -w '%{http_code}' -H "Authorization: Bearer $T_TOKEN" "$BASE/api/materials/$NEW_ID/content")
check "本班 content 200" 200 "$code"
grep -iq 'content-type: text/plain' "$HDR" \
  && { echo "PASS: txt Content-Type"; PASS=$((PASS+1)); } \
  || { echo "FAIL: Content-Type: $(grep -i content-type "$HDR")"; FAIL=$((FAIL+1)); }
[ "$(cat "$BODY")" = '教师上传的正文内容' ] \
  && { echo "PASS: 内容字节一致"; PASS=$((PASS+1)); } \
  || { echo "FAIL: 内容不一致: $(cat "$BODY")"; FAIL=$((FAIL+1)); }

echo "== 一次性票据下载 =="
check "本班申请票据 200" 200 "$(post_json /api/materials/$NEW_ID/download-ticket "$T_TOKEN" '')"
TICKET=$(sed -n 's/.*"ticket":"\([^"]*\)".*/\1/p' "$BODY")
code=$(curl -s -o "$BODY" -D "$HDR" -w '%{http_code}' -H "Authorization: Bearer $T_TOKEN" "$BASE/api/download?ticket=$TICKET")
check "首次凭票下载 200" 200 "$code"
grep -iq 'content-disposition: attachment' "$HDR" && grep -q "filename\*=UTF-8''" "$HDR" \
  && { echo "PASS: Content-Disposition attachment + UTF-8 文件名"; PASS=$((PASS+1)); } \
  || { echo "FAIL: 下载响应头: $(grep -i content-disposition "$HDR")"; FAIL=$((FAIL+1)); }
code=$(curl -s -o "$BODY" -w '%{http_code}' -H "Authorization: Bearer $T_TOKEN" "$BASE/api/download?ticket=$TICKET")
check "票据复用（刷新）410" 410 "$code"
check "缺少票据 401" 401 "$(get '/api/download')"

echo "== 退出 =="
check "退出 204" 204 "$(del /api/sessions "$T_TOKEN")"
# JWT 无状态：退出后端不销毁 token，客户端丢弃即可
check "退出后 token 仍有效（无状态）" 200 "$(get /api/me "$T_TOKEN")"

echo
echo "RESULT: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
