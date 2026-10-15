#!/bin/sh
# Swagger 文档冒烟验证：页面可打开、openapi.json 可解析、登录→鉴权接口→401 边界。
BASE=${BASE:-http://nginx}
BODY=$(mktemp)
FAIL=0
check() {
  if [ "$2" = "$3" ]; then echo "PASS: $1 ($2)"; else echo "FAIL: $1 期望 $2 实际 $3"; FAIL=1; fi
}

check "/swagger.html 200" 200 "$(curl -s -o "$BODY" -w '%{http_code}' "$BASE/swagger.html")"
check "页面引用 openapi.json" 0 "$(grep -c "url: '/openapi.json'" "$BODY" >/dev/null; echo $?)"

curl -s "$BASE/openapi.json" -o "$BODY"
check "/openapi.json 200 且为 JSON" 0 "$(grep -q '"openapi": "3.0.3"' "$BODY"; echo $?)"
check "包含 /api/class/students 路径" 0 "$(grep -q '"/api/class/students"' "$BODY"; echo $?)"
check "包含 /api/tutor/conversations/{id}/messages 路径" 0 "$(grep -q '"/api/tutor/conversations/{id}/messages"' "$BODY"; echo $?)"
check "声明 bearerAuth 安全方案" 0 "$(grep -q '"bearerAuth"' "$BODY"; echo $?)"
check "包含 /api/tutor/mcp/servers 路径" 0 "$(grep -q '"/api/tutor/mcp/servers"' "$BODY"; echo $?)"
check "包含 MCP connect 路径" 0 "$(grep -q '"/api/tutor/mcp/servers/{id}/connect"' "$BODY"; echo $?)"
check "包含 MCP disconnect 路径" 0 "$(grep -q '"/api/tutor/mcp/servers/{id}/disconnect"' "$BODY"; echo $?)"
check "包含 MCP 调用审计路径" 0 "$(grep -q '"/api/tutor/mcp/calls"' "$BODY"; echo $?)"
check "包含 /api/tutor/agents 路径" 0 "$(grep -q '"/api/tutor/agents"' "$BODY"; echo $?)"
check "包含技能绑定路径" 0 "$(grep -q '"/api/tutor/agents/{id}/skills"' "$BODY"; echo $?)"
check "不再含已退役的助手 prompt 路径" 1 "$(grep -q '"/api/tutor/assistant/prompt"' "$BODY"; echo $?)"

printf '%s' '{"username":"teacherA","password":"change_me_teacher_a"}' > "$BODY"
LOGIN=$(curl -s -X POST "$BASE/api/sessions" -H 'Content-Type: application/json' --data @"$BODY")
TOKEN=$(echo "$LOGIN" | sed -n 's/.*"accessToken":"\([^"]*\)".*/\1/p')
check "登录返回 accessToken" 0 "$([ -n "$TOKEN" ]; echo $?)"

check "携带 token 调学生名单 200" 200 "$(curl -s -o "$BODY" -w '%{http_code}' "$BASE/api/class/students" -H "Authorization: Bearer $TOKEN")"
check "未带 token 调学生名单 401" 401 "$(curl -s -o "$BODY" -w '%{http_code}' "$BASE/api/class/students")"
check "教师调 MCP 审计列表 200" 200 "$(curl -s -o "$BODY" -w '%{http_code}' "$BASE/api/tutor/mcp/calls" -H "Authorization: Bearer $TOKEN")"
check "教师调 MCP 服务器列表 200" 200 "$(curl -s -o "$BODY" -w '%{http_code}' "$BASE/api/tutor/mcp/servers" -H "Authorization: Bearer $TOKEN")"
check "未带 token 调 MCP 服务器列表 401" 401 "$(curl -s -o "$BODY" -w '%{http_code}' "$BASE/api/tutor/mcp/servers")"

rm -f "$BODY"
echo "FAIL=$FAIL"
exit $FAIL
