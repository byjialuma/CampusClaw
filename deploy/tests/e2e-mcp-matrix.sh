#!/bin/sh
# CampusClaw MCP 工具链端到端矩阵：登记/连接/发现/绑定/鉴权调用/审计/角色边界。
# 前置：docker compose 已起（backend 与 mcp-demo 均 healthy），MCP_AUTH_TOKEN 与 .env 一致。
# 用法（在 compose 网络内运行，容器 curl 镜像即可）：
#   docker run --rm --network campusclaw_ccnet -v "$PWD/deploy/tests:/tests" \
#     -e BASE=http://backend:8080 -e MCP_AUTH_TOKEN="$MCP_AUTH_TOKEN" \
#     curlimages/curl:8.10.1 sh /tests/e2e-mcp-matrix.sh
# 经 Nginx 验收：-e BASE=http://nginx
# 相似度下限过滤：另需 -e MIN_SCORE_HIGH=1，且 backend 以 SEARCH_MIN_SCORE=1（正阈值）启动，
#   此时「绑定材料命中被下限丢弃、引用为空」分支生效；默认（阈值 0.0）验证命中产生引用。
set -u

BASE="${BASE:-http://backend:8080}"
MCP_AUTH_TOKEN="${MCP_AUTH_TOKEN:-}"
T_USER="${SEED_TEACHER_A_USERNAME:-teacherA}"
T_PW="${SEED_TEACHER_A_PASSWORD:-change_me_teacher_a}"
A1_USER="${SEED_STUDENT_A1_USERNAME:-studentA1}"
A1_PW="${SEED_STUDENT_A1_PASSWORD:-change_me_student_a1}"

TS=$(date +%s)
MCP_URL="http://mcp-demo:8090"
E="e2e-mcp-$TS"        # 可连接服务器
BAD="e2e-bad-$TS"      # 不可达服务器
D="e2e-disc-$TS"       # 连接后断开（用于 denied）
G="e2e-gone-$TS"       # 连接后删除（用于 denied 且无审计）
SK1="e2e接口技能A$TS"
SK2="e2e接口技能B$TS"
SK3="e2e接口技能C$TS"
SK4="e2e路由高分$TS"       # 路由：适用条件命中 2 个
SK5="e2e路由低分$TS"       # 路由：适用条件命中 1 个；亦用于「未绑定技能不参与」
SK6="e2e路由同分甲$TS"     # 路由：同分，编号较小者
SK7="e2e路由同分乙$TS"     # 路由：同分，编号较大者
SK8="e2e路由零命中$TS"     # 路由：适用条件不命中
SKN="e2e路由未绑工具$TS"   # 启用但未绑定（其工具不得被调用）
SKT="e2e路由命中工具$TS"   # 启用且绑定，本轮命中（其工具被调用）
SKU="e2e路由未命中工具$TS" # 启用且绑定，本轮未命中（其工具不得被调用、无审计）

BODY=/tmp/ccemcp_body.txt
SSE=/tmp/ccemcp_sse.txt
META=/tmp/ccemcp_meta.json
HDR=/tmp/ccemcp_hdr.txt
PASS=0
FAIL=0

T_TOKEN=""
A1_TOKEN=""
CID=""
E_ID=""
BAD_ID=""
D_ID=""
G_ID=""
SK1_ID=""
SK2_ID=""
SK3_ID=""
LEGAL_ID=""
SK4_ID=""
SK5_ID=""
SK6_ID=""
SK7_ID=""
SK8_ID=""
SKN_ID=""
SKT_ID=""
SKU_ID=""
AGENT_ID=""
AGENT_R=""
CID_R=""

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

info() { echo "INFO: $1"; }

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

del() { # path token
  curl -s -o "$BODY" -w '%{http_code}' -H "Authorization: Bearer $2" -X DELETE "$BASE$1"
}

sse_post() { # token conv json outfile
  curl -sN -H 'Content-Type: application/json' -H "Authorization: Bearer $1" \
    -X POST --data-binary "$3" "$BASE/api/tutor/conversations/$2/messages" -o "$4"
}

extract_meta() { # sse_file meta_out
  sed -n '/^event: meta$/,/^$/p' "$1" | sed -n 's/^data: //p' > "$2"
}

countof() { # pattern [file]
  f="${2:-$BODY}"
  grep -o "$1" "$f" | wc -l | tr -d ' '
}

sse_deltas() { # sse_file  -> 拼接的 delta 文本
  sed -n 's/^data: {"text":"\(.*\)"}$/\1/p' "$1" | tr -d '\n'
}

last_id() { sed -n 's/^{"id":\([0-9]*\).*/\1/p' "$BODY"; }

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

register_server() { # var_name name baseurl [token]
  if [ -n "${4:-}" ]; then
    code=$(post_json /api/tutor/mcp/servers "$T_TOKEN" \
      "{\"name\":\"$2\",\"baseUrl\":\"$3\",\"authToken\":\"$4\"}")
  else
    code=$(post_json /api/tutor/mcp/servers "$T_TOKEN" "{\"name\":\"$2\",\"baseUrl\":\"$3\"}")
  fi
  check "登记 $2 返回 201" 201 "$code"
  eval "$1=\"$(last_id)\""
}

create_skill() { # var_name token name content
  code=$(post_json /api/tutor/assistant/skills "$2" \
    "{\"name\":\"$3\",\"content\":\"$4\"}")
  check "新建技能「$3」返回 201" 201 "$code"
  eval "$1=\"$(last_id)\""
}

enable_skill() { # token id enabled
  check "技能 $2 enabled=$3" 200 "$(put_json /api/tutor/assistant/skills/$2 "$1" "{\"enabled\":$3}")"
}

create_agent() { # var_name name prompt
  code=$(post_json /api/tutor/agents "$T_TOKEN" "{\"name\":\"$2\",\"systemPrompt\":\"$3\"}")
  check "新建智能体「$2」返回 201" 201 "$code"
  eval "$1=\"$(last_id)\""
}

bind_skills() { # agent_id csv
  check "绑定技能 [$2] 到智能体 $1" 200 "$(put_json "/api/tutor/agents/$1/skills" "$T_TOKEN" "{\"skillIds\":[$2]}")"
}

bind_materials() { # agent_id csv
  check "绑定材料 [$2] 到智能体 $1" 200 "$(put_json "/api/tutor/agents/$1/materials" "$T_TOKEN" "{\"handoutIds\":[$2]}")"
}

cleanup() {
  if [ -n "$T_TOKEN" ]; then
    for sid in "$SK1_ID" "$SK2_ID" "$SK3_ID" "$LEGAL_ID" \
               "$SK4_ID" "$SK5_ID" "$SK6_ID" "$SK7_ID" "$SK8_ID" \
               "$SKN_ID" "$SKT_ID" "$SKU_ID"; do
      [ -n "$sid" ] && del "/api/tutor/assistant/skills/$sid" "$T_TOKEN" >/dev/null 2>&1
    done
    for srv in "$E_ID" "$BAD_ID" "$D_ID" "$G_ID"; do
      [ -n "$srv" ] && del "/api/tutor/mcp/servers/$srv" "$T_TOKEN" >/dev/null 2>&1
    done
    # 智能体被会话引用（外键 RESTRICT）时删除会 409，此处静默忽略；测试数据由宿主机侧另行清理。
    for ag in "$AGENT_ID" "$AGENT_R"; do
      [ -n "$ag" ] && del "/api/tutor/agents/$ag" "$T_TOKEN" >/dev/null 2>&1
    done
  fi
}
trap cleanup EXIT INT TERM

echo "== 健康检查 =="
check "GET /health 200" 200 "$(get /health)"

echo "== 登录 =="
login_and_save_token T_TOKEN "$T_USER" "$T_PW"
login_and_save_token A1_TOKEN "$A1_USER" "$A1_PW"

if [ -z "$MCP_AUTH_TOKEN" ]; then
  echo "FAIL: 未提供 MCP_AUTH_TOKEN，无法验证连接与工具调用"; FAIL=$((FAIL + 1))
fi

echo "== 智能体（会话必须绑定本班智能体） =="
create_agent AGENT_ID "e2e工具链智能体$TS" "你是CampusClaw工具链验收助教，请始终用简洁中文回答。"
ok_if "教师视图含 systemPrompt" "grep -q '\"systemPrompt\":\"你是CampusClaw工具链验收助教' \"\$BODY\""

echo "== 权限边界：未登录 401 / 学生 403（7 条管理接口全覆盖） =="
check "未登录 GET servers 401" 401 "$(get /api/tutor/mcp/servers)"
check "未登录 POST servers 401" 401 "$(post_json /api/tutor/mcp/servers '' '{"name":"x","baseUrl":"http://x"}')"
check "未登录 PUT servers/{id} 401" 401 "$(put_json /api/tutor/mcp/servers/1 '' '{"name":"x","baseUrl":"http://x"}')"
check "未登录 DELETE servers/{id} 401" 401 "$(del /api/tutor/mcp/servers/1 '')"
check "未登录 connect 401" 401 "$(post_json /api/tutor/mcp/servers/1/connect '' '{}')"
check "未登录 disconnect 401" 401 "$(post_json /api/tutor/mcp/servers/1/disconnect '' '{}')"
check "未登录 GET calls 401" 401 "$(get /api/tutor/mcp/calls)"
check "学生 GET servers 403" 403 "$(get /api/tutor/mcp/servers "$A1_TOKEN")"
check "学生 POST servers 403" 403 "$(post_json /api/tutor/mcp/servers "$A1_TOKEN" '{"name":"x","baseUrl":"http://x"}')"
check "学生 PUT servers/{id} 403" 403 "$(put_json /api/tutor/mcp/servers/1 "$A1_TOKEN" '{"name":"x","baseUrl":"http://x"}')"
check "学生 DELETE servers/{id} 403" 403 "$(del /api/tutor/mcp/servers/1 "$A1_TOKEN")"
check "学生 connect 403" 403 "$(post_json /api/tutor/mcp/servers/1/connect "$A1_TOKEN" '{}')"
check "学生 disconnect 403" 403 "$(post_json /api/tutor/mcp/servers/1/disconnect "$A1_TOKEN" '{}')"
check "学生 GET calls 403" 403 "$(get /api/tutor/mcp/calls "$A1_TOKEN")"

echo "== 列表：只回 hasAuth，绝不回传令牌 =="
check "教师 GET servers 200" 200 "$(get /api/tutor/mcp/servers "$T_TOKEN")"
ok_if "列表含预置 campus-tools" 'grep -q "\"name\":\"campus-tools\"" "$BODY"'
ok_if "预置服务器 hasAuth=true" 'grep -q "\"name\":\"campus-tools\",\"baseUrl\":\"http://mcp-demo:8090\",\"hasAuth\":true" "$BODY"'
ok_if "响应不含 authToken 键" '! grep -q "authToken" "$BODY"'
if [ -n "$MCP_AUTH_TOKEN" ]; then
  ok_if "响应不含令牌原文" "! grep -q '$MCP_AUTH_TOKEN' \"\$BODY\""
fi

echo "== 登记与同班重名 =="
register_server E_ID "$E" "$MCP_URL" "$MCP_AUTH_TOKEN"
ok_if "登记响应只含 hasAuth 布尔" 'grep -q "\"hasAuth\":true" "$BODY"'
ok_if "登记响应未回传令牌原文" "[ -z \"$MCP_AUTH_TOKEN\" ] || ! grep -q '$MCP_AUTH_TOKEN' \"\$BODY\""
ok_if "登记响应不含 authToken 键" '! grep -q "authToken" "$BODY"'
code=$(post_json /api/tutor/mcp/servers "$T_TOKEN" "{\"name\":\"$E\",\"baseUrl\":\"$MCP_URL\"}")
check "同班重名登记 400" 400 "$code"
code=$(post_json /api/tutor/mcp/servers "$T_TOKEN" '{"name":"Bad Name","baseUrl":"http://x"}')
check "非法名称 400" 400 "$code"
code=$(post_json /api/tutor/mcp/servers "$T_TOKEN" "{\"name\":\"$BAD\",\"baseUrl\":\"not-a-url\"}")
check "非法 Base URL 400" 400 "$code"

echo "== 连接与工具发现 =="
check "连接 $E 返回 200" 200 "$(post_json /api/tutor/mcp/servers/$E_ID/connect "$T_TOKEN" '{}')"
ok_if "连接响应 connected=true" 'grep -q "\"connected\":true" "$BODY"'
ok_if "发现 formula_hint" 'grep -q "\"name\":\"formula_hint\"" "$BODY"'
ok_if "发现 unit_convert" 'grep -q "\"name\":\"unit_convert\"" "$BODY"'
ok_if "工具条目含 inputSchema" 'grep -q "\"inputSchema\"" "$BODY"'
ok_if "连接响应不含令牌原文" "[ -z \"$MCP_AUTH_TOKEN\" ] || ! grep -q '$MCP_AUTH_TOKEN' \"\$BODY\""

echo "== 错误地址连接失败 502 且保持未连接 =="
register_server BAD_ID "$BAD" "http://127.0.0.1:9"
check "连接不可达服务器 502" 502 "$(post_json /api/tutor/mcp/servers/$BAD_ID/connect "$T_TOKEN" '{}')"
ok_if "502 含可读错误提示" 'grep -q "\"error\"" "$BODY"'
check "GET servers 200" 200 "$(get /api/tutor/mcp/servers "$T_TOKEN")"
ok_if "不可达服务器保持 connected=false" "grep -q '{\"id\":$BAD_ID,\"name\":\"$BAD\",\"baseUrl\":\"http://127.0.0.1:9\",\"hasAuth\":false,\"connected\":false,\"tools\":\[\]' \"\$BODY\""

echo "== 断开清空清单 =="
check "断开 $E 返回 200" 200 "$(post_json /api/tutor/mcp/servers/$E_ID/disconnect "$T_TOKEN" '{}')"
ok_if "断开响应 connected=false" 'grep -q "\"connected\":false" "$BODY"'
ok_if "断开响应 tools 为空" 'grep -q "\"tools\":\[\]" "$BODY"'
check "GET 确认清单已清空" 200 "$(get /api/tutor/mcp/servers "$T_TOKEN")"
ok_if "DB 呈现 tools=[] 且 connected=false" "grep -q '\"id\":$E_ID,\"name\":\"$E\",\"baseUrl\":\"$MCP_URL\",\"hasAuth\":true,\"connected\":false,\"tools\":\[\]' \"\$BODY\""
check "重新连接 $E 返回 200" 200 "$(post_json /api/tutor/mcp/servers/$E_ID/connect "$T_TOKEN" '{}')"

echo "== 非法绑定被拒 400 =="
register_server D_ID "$D" "$MCP_URL" "$MCP_AUTH_TOKEN"
code=$(post_json /api/tutor/assistant/skills "$T_TOKEN" \
  "{\"name\":\"e2e接口技能X$TS\",\"content\":\"工具：nope@badserver\"}")
check "语法非法引用 400" 400 "$code"
ok_if "拒绝原因指明具体引用" 'grep -q "nope@badserver" "$BODY"'
code=$(post_json /api/tutor/assistant/skills "$T_TOKEN" \
  "{\"name\":\"e2e接口技能Y$TS\",\"content\":\"工具：formula_hint@nope-server-$TS\"}")
check "服务器不存在引用 400" 400 "$code"
ok_if "拒绝原因含服务器不存在" 'grep -q "不存在" "$BODY"'
code=$(post_json /api/tutor/assistant/skills "$T_TOKEN" \
  "{\"name\":\"e2e接口技能Z$TS\",\"content\":\"工具：formula_hint@$D\"}")
check "服务器未连接引用 400" 400 "$code"
ok_if "拒绝原因含尚未连接" 'grep -q "尚未连接" "$BODY"'
code=$(post_json /api/tutor/assistant/skills "$T_TOKEN" \
  "{\"name\":\"e2e接口技能W$TS\",\"content\":\"工具：formula_hint@$E，knowledge_search\"}")
check "合法引用（含内置）201" 201 "$code"
LEGAL_ID=$(last_id)
ok_if "技能正文原样保存（含内置引用）" "grep -q 'knowledge_search' \"\$BODY\""
code=$(post_json /api/tutor/assistant/skills "$T_TOKEN" \
  "{\"name\":\"e2e接口技能V$TS\",\"content\":\"工具：nosuchtool@$E\"}")
check "工具不在清单引用 400" 400 "$code"
ok_if "拒绝原因含不在清单" 'grep -q "不在服务器" "$BODY"'

echo "== 调用成功：meta 含引用 + 回答体现工具结果 + 审计 success =="
create_skill SK1_ID "$T_TOKEN" "$SK1" "工具：formula_hint@$E\n适用条件：一元二次\n请优先引用工具返回的公式与分步提示。"
enable_skill "$T_TOKEN" "$SK1_ID" true
bind_skills "$AGENT_ID" "$SK1_ID"
code=$(post_json /api/tutor/conversations "$A1_TOKEN" "{\"agentId\":$AGENT_ID}")
check "学生创建会话 201" 201 "$code"
CID=$(last_id)
sse_post "$A1_TOKEN" "$CID" '{"question":"一元二次方程的求根公式是什么？"}' "$SSE"
ok_if "收到 meta 事件" 'grep -q "^event: meta" "$SSE"'
ok_if "收到 done 事件" 'grep -q "^event: done" "$SSE"'
ok_if "无 error 事件" '! grep -q "^event: error" "$SSE"'
extract_meta "$SSE" "$META"
ok_if "meta 含 toolCalls 引用 formula_hint@$E" "grep -q '\"toolRef\":\"formula_hint@$E\"' \"\$META\""
ok_if "meta 该引用 status=success" "grep -q '\"toolRef\":\"formula_hint@$E\",\"serverName\":\"$E\",\"status\":\"success\"' \"\$META\""
ANS=$(sse_deltas "$SSE")
ok_if "回答非空（长度 ${#ANS}）" '[ -n "$ANS" ]'
ok_if "回答体现工具结果（含判别式/4ac）" "echo \"\$ANS\" | grep -qE '4ac|判别式'"
ok_if "meta 不含令牌原文" "[ -z \"$MCP_AUTH_TOKEN\" ] || ! grep -q '$MCP_AUTH_TOKEN' \"\$META\""
ok_if "meta 不含内部地址 mcp-demo" '! grep -q "mcp-demo" "$META"'
check "教师 GET calls 200" 200 "$(get /api/tutor/mcp/calls "$T_TOKEN")"
ok_if "审计含 formula_hint@$E 且 success" "grep -q '\"serverName\":\"$E\",\"toolRef\":\"formula_hint@$E\",\"status\":\"success\"' \"\$BODY\""
ok_if "审计 serverId 非空（服务器仍在）" "grep -q '\"id\":[0-9]*,\"serverId\":$E_ID,' \"\$BODY\""

echo "== 调用被拒：服务器未连接 -> denied + 审计 denied =="
enable_skill "$T_TOKEN" "$SK1_ID" false
check "连接 $D 返回 200" 200 "$(post_json /api/tutor/mcp/servers/$D_ID/connect "$T_TOKEN" '{}')"
create_skill SK2_ID "$T_TOKEN" "$SK2" "工具：unit_convert@$D\n适用条件：千米,米"
enable_skill "$T_TOKEN" "$SK2_ID" true
bind_skills "$AGENT_ID" "$SK1_ID,$SK2_ID"
check "断开 $D 返回 200" 200 "$(post_json /api/tutor/mcp/servers/$D_ID/disconnect "$T_TOKEN" '{}')"
sse_post "$A1_TOKEN" "$CID" '{"question":"5 千米是多少米？"}' "$SSE"
ok_if "收到 meta+done" 'grep -q "^event: meta" "$SSE" && grep -q "^event: done" "$SSE"'
extract_meta "$SSE" "$META"
ok_if "meta 含引用 unit_convert@$D" "grep -q '\"toolRef\":\"unit_convert@$D\"' \"\$META\""
ok_if "meta 该引用 status=denied" "grep -q '\"toolRef\":\"unit_convert@$D\",\"serverName\":\"$D\",\"status\":\"denied\"' \"\$META\""
check "教师 GET calls 200" 200 "$(get /api/tutor/mcp/calls "$T_TOKEN")"
ok_if "审计含 unit_convert@$D 且 denied（未连接不调用远端）" "grep -q '\"toolRef\":\"unit_convert@$D\",\"status\":\"denied\"' \"\$BODY\""

echo "== 调用被拒：服务器已删除 -> denied 且不写审计 =="
enable_skill "$T_TOKEN" "$SK2_ID" false
register_server G_ID "$G" "$MCP_URL" "$MCP_AUTH_TOKEN"
check "连接 $G 返回 200" 200 "$(post_json /api/tutor/mcp/servers/$G_ID/connect "$T_TOKEN" '{}')"
create_skill SK3_ID "$T_TOKEN" "$SK3" "工具：unit_convert@$G\n适用条件：克,千克"
enable_skill "$T_TOKEN" "$SK3_ID" true
bind_skills "$AGENT_ID" "$SK1_ID,$SK2_ID,$SK3_ID"
check "删除 $G 返回 200" 200 "$(del /api/tutor/mcp/servers/$G_ID "$T_TOKEN")"
G_ID=""
check "教师 GET calls 200" 200 "$(get /api/tutor/mcp/calls "$T_TOKEN")"
BEFORE=$(countof "\"toolRef\":\"unit_convert@$G\"")
ok_if "删除前该引用无审计行（before=0）" '[ "$BEFORE" = "0" ]'
sse_post "$A1_TOKEN" "$CID" '{"question":"把 2000 克换算成千克。"}' "$SSE"
ok_if "收到 meta+done" 'grep -q "^event: meta" "$SSE" && grep -q "^event: done" "$SSE"'
extract_meta "$SSE" "$META"
ok_if "meta 含引用 unit_convert@$G" "grep -q '\"toolRef\":\"unit_convert@$G\"' \"\$META\""
ok_if "meta 该引用 status=denied" "grep -q '\"toolRef\":\"unit_convert@$G\",\"serverName\":\"$G\",\"status\":\"denied\"' \"\$META\""
check "教师 GET calls 200" 200 "$(get /api/tutor/mcp/calls "$T_TOKEN")"
AFTER=$(countof "\"toolRef\":\"unit_convert@$G\"")
ok_if "已删除服务器调用不写审计（after=0）" '[ "$AFTER" = "0" ]'

echo "== 审计三态汇总 =="
check "教师 GET calls 200" 200 "$(get /api/tutor/mcp/calls "$T_TOKEN")"
N_SUCCESS=$(countof '"status":"success"')
N_DENIED=$(countof '"status":"denied"')
ok_if "审计存在 success 行（$N_SUCCESS 条）" '[ "$N_SUCCESS" -ge 1 ]'
ok_if "审计存在 denied 行（$N_DENIED 条）" '[ "$N_DENIED" -ge 1 ]'
N_ERROR=$(countof '"status":"error"')
if [ "$N_ERROR" -ge 1 ]; then
  ok_if "审计存在 error 行（$N_ERROR 条）" 'true'
else
  info "审计无 error 行：内置演示工具无失败路径，error 状态由后端单测覆盖（远端 5xx/超时/非法响应/isError）"
fi

echo "== 断开后学生调用不再触发工具（能力收敛） =="
enable_skill "$T_TOKEN" "$SK3_ID" false
sse_post "$A1_TOKEN" "$CID" '{"question":"集合有哪些基本运算？"}' "$SSE"
ok_if "停用全部带工具技能后正常 meta+done" 'grep -q "^event: meta" "$SSE" && grep -q "^event: done" "$SSE"'

# ---------- 词法路由与相似度下限（多智能体技能路由） ----------

echo "== 路由：单技能最高分命中 =="
create_agent AGENT_R "e2e路由智能体$TS" "你是CampusClaw路由验收助教，请始终用简洁中文回答。"
create_skill SK4_ID "$T_TOKEN" "$SK4" "适用条件：一元二次,判别式\n（验收技能：命中 2 个关键词）"
create_skill SK5_ID "$T_TOKEN" "$SK5" "适用条件：一元二次\n（验收技能：命中 1 个关键词）"
enable_skill "$T_TOKEN" "$SK4_ID" true
enable_skill "$T_TOKEN" "$SK5_ID" true
bind_skills "$AGENT_R" "$SK4_ID,$SK5_ID"
code=$(post_json /api/tutor/conversations "$A1_TOKEN" "{\"agentId\":$AGENT_R}")
check "路由会话创建 201" 201 "$code"
CID_R=$(last_id)
sse_post "$A1_TOKEN" "$CID_R" '{"question":"一元二次方程的判别式怎么求？"}' "$SSE"
ok_if "路由提问 meta+done" 'grep -q "^event: meta" "$SSE" && grep -q "^event: done" "$SSE"'
extract_meta "$SSE" "$META"
ok_if "命中最高分技能（SK4）" "grep -q '\"id\":$SK4_ID,' \"\$META\""
ok_if "未命中较低分技能（SK5）" "! grep -q '\"id\":$SK5_ID,' \"\$META\""

echo "== 路由：同分取技能编号较小者 =="
create_skill SK6_ID "$T_TOKEN" "$SK6" "适用条件：集合,子集"
create_skill SK7_ID "$T_TOKEN" "$SK7" "适用条件：集合,并集"
enable_skill "$T_TOKEN" "$SK6_ID" true
enable_skill "$T_TOKEN" "$SK7_ID" true
bind_skills "$AGENT_R" "$SK6_ID,$SK7_ID"
sse_post "$A1_TOKEN" "$CID_R" '{"question":"集合的并集和子集分别是什么？"}' "$SSE"
ok_if "同分提问 meta+done" 'grep -q "^event: meta" "$SSE" && grep -q "^event: done" "$SSE"'
extract_meta "$SSE" "$META"
ok_if "同分命中编号较小者（SK6）" "grep -q '\"id\":$SK6_ID,' \"\$META\""
ok_if "同分未命中编号较大者（SK7）" "! grep -q '\"id\":$SK7_ID,' \"\$META\""

echo "== 路由：零命中不选技能 =="
create_skill SK8_ID "$T_TOKEN" "$SK8" "适用条件：三角函数"
enable_skill "$T_TOKEN" "$SK8_ID" true
bind_skills "$AGENT_R" "$SK8_ID"
sse_post "$A1_TOKEN" "$CID_R" '{"question":"一元二次方程怎么解？"}' "$SSE"
extract_meta "$SSE" "$META"
ok_if "零命中 meta.skills 为空" 'grep -q "\"skills\":\[\]" "$META"'

echo "== 路由：未绑定技能不参与（其工具不被调用、无审计） =="
create_skill SKN_ID "$T_TOKEN" "$SKN" "适用条件：一元二次\n工具：formula_hint@campus-tools"
enable_skill "$T_TOKEN" "$SKN_ID" true
bind_skills "$AGENT_R" "$SK5_ID"   # 仅绑定 SK5；SKN 已启用且条件命中但未绑定
get /api/tutor/mcp/calls "$T_TOKEN"
BEFORE_UNBOUND=$(countof "\"toolRef\":\"formula_hint@campus-tools\"")
sse_post "$A1_TOKEN" "$CID_R" '{"question":"一元二次方程怎么解？"}' "$SSE"
extract_meta "$SSE" "$META"
ok_if "命中已绑定技能（SK5）" "grep -q '\"id\":$SK5_ID,' \"\$META\""
ok_if "未绑定技能未被选中" "! grep -q '\"id\":$SKN_ID,' \"\$META\""
ok_if "未绑定技能的工具未被调用（meta 无该引用）" '! grep -q "formula_hint@campus-tools" "$META"'
get /api/tutor/mcp/calls "$T_TOKEN"
AFTER_UNBOUND=$(countof "\"toolRef\":\"formula_hint@campus-tools\"")
ok_if "未绑定技能的工具无审计（before=$BEFORE_UNBOUND after=$AFTER_UNBOUND）" "[ \"$AFTER_UNBOUND\" = \"$BEFORE_UNBOUND\" ]"

echo "== 路由：未命中技能不调用工具且无审计 =="
create_skill SKT_ID "$T_TOKEN" "$SKT" "工具：formula_hint@campus-tools\n适用条件：一元二次,判别式"
create_skill SKU_ID "$T_TOKEN" "$SKU" "工具：unit_convert@campus-tools\n适用条件：三角函数"
enable_skill "$T_TOKEN" "$SKT_ID" true
enable_skill "$T_TOKEN" "$SKU_ID" true
bind_skills "$AGENT_R" "$SKT_ID,$SKU_ID"
get /api/tutor/mcp/calls "$T_TOKEN"
BEFORE_UNIT=$(countof "\"toolRef\":\"unit_convert@campus-tools\"")
sse_post "$A1_TOKEN" "$CID_R" '{"question":"一元二次方程的判别式怎么解？"}' "$SSE"
extract_meta "$SSE" "$META"
ok_if "命中带工具技能（SKT）" "grep -q '\"id\":$SKT_ID,' \"\$META\""
ok_if "命中技能的工具被调用且成功" "grep -q '\"toolRef\":\"formula_hint@campus-tools\",\"serverName\":\"campus-tools\",\"status\":\"success\"' \"\$META\""
ok_if "未命中技能的工具未被调用" '! grep -q "unit_convert@campus-tools" "$META"'
get /api/tutor/mcp/calls "$T_TOKEN"
AFTER_UNIT=$(countof "\"toolRef\":\"unit_convert@campus-tools\"")
ok_if "未命中技能的工具无新增审计（before=$BEFORE_UNIT after=$AFTER_UNIT）" "[ \"$AFTER_UNIT\" = \"$BEFORE_UNIT\" ]"

echo "== 检索：绑定材料命中（默认阈值） / 相似度下限过滤（正阈值） =="
get /api/materials "$T_TOKEN"
MAT_IDS=$(grep -o '"id":[0-9]*' "$BODY" | sed 's/"id"://' | tr '\n' ',' | sed 's/,$//')
ok_if "取到本班材料（ids=$MAT_IDS）" '[ -n "$MAT_IDS" ]'
bind_skills "$AGENT_R" ""
bind_materials "$AGENT_R" "$MAT_IDS"
sse_post "$A1_TOKEN" "$CID_R" '{"question":"集合有哪些基本运算？"}' "$SSE"
extract_meta "$SSE" "$META"
if [ "${MIN_SCORE_HIGH:-0}" = "1" ]; then
  ok_if "正阈值过滤：全部召回被下限丢弃，引用为空" 'grep -q "\"citations\":\[\]" "$META"'
else
  ok_if "默认阈值：绑定材料命中产生引用（引用非空）" '! grep -q "\"citations\":\[\]" "$META"'
fi

echo "== 检索落空（未绑定材料）仍按提示词作答且引用为空 =="
bind_materials "$AGENT_R" ""
bind_skills "$AGENT_R" ""
sse_post "$A1_TOKEN" "$CID_R" '{"question":"请用一句话说明什么是集合。"}' "$SSE"
ok_if "落空提问 meta+done" 'grep -q "^event: meta" "$SSE" && grep -q "^event: done" "$SSE"'
extract_meta "$SSE" "$META"
ok_if "未绑定材料时引用为空" 'grep -q "\"citations\":\[\]" "$META"'
ANS_EMPTY=$(sse_deltas "$SSE")
ok_if "回答非空（长度 ${#ANS_EMPTY}）" '[ -n "$ANS_EMPTY" ]'
ok_if "未出现固定兜底文案" '! echo "$ANS_EMPTY" | grep -q "根据已有材料无法回答该问题"'

echo
echo "RESULT: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
