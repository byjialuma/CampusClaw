#!/bin/sh
# CampusClaw 端到端鉴权矩阵。
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
T_JAR=/tmp/cc_teacher.jar
A1_JAR=/tmp/cc_a1.jar
B1_JAR=/tmp/cc_b1.jar
UP_NAME='教师上传验证.txt'
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

grepbody() { grep -q "$1" "$BODY"; }

get() { # path jar
  if [ -n "${2:-}" ]; then
    curl -s -c "$2" -o "$BODY" -w '%{http_code}' -b "$2" "$BASE$1"
  else
    curl -s -o "$BODY" -w '%{http_code}' "$BASE$1"
  fi
}
post_json() { # path jar json
  curl -s -c "$2" -o "$BODY" -w '%{http_code}' -b "$2" \
    -H 'Content-Type: application/json' -X POST -d "$3" "$BASE$1"
}
del() { # path jar
  curl -s -c "$2" -o "$BODY" -w '%{http_code}' -b "$2" -X DELETE "$BASE$1"
}

echo "== 健康检查 =="
check "GET /health 200" 200 "$(get /health)"
grepbody '"status":"ok"' && { echo "PASS: health body ok"; PASS=$((PASS+1)); } || { echo "FAIL: health body"; FAIL=$((FAIL+1)); }

echo "== 未认证 =="
check "未登录 GET /api/materials 401" 401 "$(get /api/materials)"
check "错误密码 401" 401 "$(post_json /api/sessions "$T_JAR" "{\"username\":\"$T_USER\",\"password\":\"wrong\"}")"

echo "== 登录 =="
check "教师登录 200" 200 "$(post_json /api/sessions "$T_JAR" "{\"username\":\"$T_USER\",\"password\":\"$T_PW\"}")"
check "学生 A1 登录 200" 200 "$(post_json /api/sessions "$A1_JAR" "{\"username\":\"$A1_USER\",\"password\":\"$A1_PW\"}")"
check "学生 B1 登录 200" 200 "$(post_json /api/sessions "$B1_JAR" "{\"username\":\"$B1_USER\",\"password\":\"$B1_PW\"}")"

echo "== 班级隔离：列表 =="
get /api/materials "$T_JAR"
grepbody 'A班' && ! grepbody 'B班' \
  && { echo "PASS: 教师列表只含 A 班材料"; PASS=$((PASS+1)); } \
  || { echo "FAIL: 教师列表班级过滤"; FAIL=$((FAIL+1)); }

get /api/materials "$B1_JAR"
grepbody 'B班' && ! grepbody '《A班' \
  && { echo "PASS: B1 列表只含 B 班材料"; PASS=$((PASS+1)); } \
  || { echo "FAIL: B1 列表班级过滤"; FAIL=$((FAIL+1)); }
B_ID=$(grep -o '"id":[0-9]*' "$BODY" | head -1 | cut -d: -f2)
echo "B 班材料 id=$B_ID"

echo "== 跨班访问被拒 =="
check "A1 看 B 班 content 403" 403 "$(get /api/materials/$B_ID/content "$A1_JAR")"
check "A1 申请 B 班下载票据 403" 403 "$(post_json /api/materials/$B_ID/download-ticket "$A1_JAR" '')"

echo "== 学生上传 403 =="
echo 'student content' >/tmp/student.txt
code=$(curl -s -o "$BODY" -w '%{http_code}' -b "$A1_JAR" -c "$A1_JAR" \
  -F "file=@/tmp/student.txt;filename=student.txt" "$BASE/api/materials")
check "学生上传 403" 403 "$code"
grepbody '学生无权限上传文件' \
  && { echo "PASS: 403 文案正确"; PASS=$((PASS+1)); } \
  || { echo "FAIL: 403 文案"; FAIL=$((FAIL+1)); }

echo "== 教师上传 =="
printf 'not word' >/tmp/bad.docx
code=$(curl -s -o "$BODY" -w '%{http_code}' -b "$T_JAR" -c "$T_JAR" \
  -F "file=@/tmp/bad.docx;filename=bad.docx" "$BASE/api/materials")
check "上传 docx 400" 400 "$code"

printf 'this-is-not-a-real-pdf' >/tmp/fake.pdf
code=$(curl -s -o "$BODY" -w '%{http_code}' -b "$T_JAR" -c "$T_JAR" \
  -F "file=@/tmp/fake.pdf;filename=fake.pdf" "$BASE/api/materials")
check "伪造 pdf 400" 400 "$code"

echo '教师上传的正文内容' >/tmp/upload.txt
code=$(curl -s -o "$BODY" -w '%{http_code}' -b "$T_JAR" -c "$T_JAR" \
  -F "file=@/tmp/upload.txt;filename=$UP_NAME" "$BASE/api/materials")
check "教师上传 txt 201" 201 "$code"
NEW_ID=$(grep -o '"id":[0-9]*' "$BODY" | head -1 | cut -d: -f2)

get /api/materials "$T_JAR"
grepbody "$UP_NAME" \
  && { echo "PASS: 上传后本班列表可查"; PASS=$((PASS+1)); } \
  || { echo "FAIL: 列表查不到新上传"; FAIL=$((FAIL+1)); }
get /api/materials "$B1_JAR"
if grepbody "$UP_NAME"; then
  echo "FAIL: 外班列表出现新材料"; FAIL=$((FAIL+1))
else
  echo "PASS: 外班列表不含新材料"; PASS=$((PASS+1))
fi

echo "== 内容预览 =="
code=$(curl -s -o "$BODY" -D "$HDR" -w '%{http_code}' -b "$T_JAR" "$BASE/api/materials/$NEW_ID/content")
check "本班 content 200" 200 "$code"
grep -iq 'content-type: text/plain' "$HDR" \
  && { echo "PASS: txt Content-Type"; PASS=$((PASS+1)); } \
  || { echo "FAIL: Content-Type: $(grep -i content-type "$HDR")"; FAIL=$((FAIL+1)); }
[ "$(cat "$BODY")" = '教师上传的正文内容' ] \
  && { echo "PASS: 内容字节一致"; PASS=$((PASS+1)); } \
  || { echo "FAIL: 内容不一致: $(cat "$BODY")"; FAIL=$((FAIL+1)); }

echo "== 一次性票据下载 =="
check "本班申请票据 200" 200 "$(post_json /api/materials/$NEW_ID/download-ticket "$T_JAR" '')"
TICKET=$(sed -n 's/.*"ticket":"\([^"]*\)".*/\1/p' "$BODY")
code=$(curl -s -o "$BODY" -D "$HDR" -w '%{http_code}' "$BASE/api/download?ticket=$TICKET")
check "首次凭票下载 200" 200 "$code"
grep -iq 'content-disposition: attachment' "$HDR" && grep -q "filename\*=UTF-8''" "$HDR" \
  && { echo "PASS: Content-Disposition attachment + UTF-8 文件名"; PASS=$((PASS+1)); } \
  || { echo "FAIL: 下载响应头: $(grep -i content-disposition "$HDR")"; FAIL=$((FAIL+1)); }
code=$(curl -s -o "$BODY" -w '%{http_code}' "$BASE/api/download?ticket=$TICKET")
check "票据复用（刷新）410" 410 "$code"
check "缺少票据 401" 401 "$(get '/api/download')"

echo "== 退出 =="
check "退出 204" 204 "$(del /api/sessions "$T_JAR")"
check "退出后 /api/me 401" 401 "$(get /api/me "$T_JAR")"

echo
echo "RESULT: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
