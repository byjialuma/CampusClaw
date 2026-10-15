package tutor

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"campusclaw/internal/model"
)

// ---------- 轻量 fake driver：记录 SQL 与参数，返回预置行 ----------

type queryLog struct {
	sql  string
	args []driver.NamedValue
}

type fakeRows struct {
	cols []string
	rows [][]driver.Value
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if len(r.rows) == 0 {
		return io.EOF
	}
	copy(dest, r.rows[0])
	r.rows = r.rows[1:]
	return nil
}

type fakeResult struct{ n int64 }

func (r fakeResult) LastInsertId() (int64, error) { return 101, nil }
func (r fakeResult) RowsAffected() (int64, error) { return r.n, nil }

// fakeConn 记录每次 Query/Exec 的 SQL 与参数，并按序返回预置结果。
type fakeConn struct {
	mu           sync.Mutex
	logs         []queryLog
	queryResults []driver.Rows
	affected     int64
	execErr      error // 非 nil 时 Exec 直接返回该错误（模拟唯一键冲突等）
}

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare 不应被触发")
}
func (c *fakeConn) Close() error              { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) { return nil, errors.New("不使用事务") }

func (c *fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, queryLog{query, args})
	if len(c.queryResults) == 0 {
		return &fakeRows{}, nil
	}
	r := c.queryResults[0]
	c.queryResults = c.queryResults[1:]
	return r, nil
}

func (c *fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, queryLog{query, args})
	if c.execErr != nil {
		return nil, c.execErr
	}
	return fakeResult{n: c.affected}, nil
}

type fakeConnector struct{ conn *fakeConn }

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c fakeConnector) Driver() driver.Driver                        { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return nil, errors.New("使用 OpenDB") }

// newTestRepo 返回挂接 fakeConn 的仓储。
func newTestRepo(affected int64, queryResults ...driver.Rows) (*TutorRepository, *fakeConn) {
	conn := &fakeConn{queryResults: queryResults, affected: affected}
	db := sql.OpenDB(fakeConnector{conn})
	return &TutorRepository{DB: db}, conn
}

// lastLog 返回最后一条记录。
func lastLog(t *testing.T, conn *fakeConn) queryLog {
	t.Helper()
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.logs) == 0 {
		t.Fatal("没有任何 SQL 执行记录")
	}
	return conn.logs[len(conn.logs)-1]
}

// ---------- 属主过滤 ----------

// 会话读取必须把 user_id 条件落在 SQL 层：语句包含属主条件，且参数确实传入 user_id。
func TestConversationForUserOwnerFilter(t *testing.T) {
	now := time.Now()
	rows := &fakeRows{
		cols: []string{"id", "class_id", "user_id", "agent_id", "title", "created_at"},
		rows: [][]driver.Value{{int64(7), int64(1), int64(2), int64(5), "什么是集合？", now}},
	}
	repo, conn := newTestRepo(0, rows)

	got, err := repo.ConversationForUser(context.Background(), 7, 2)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if got.ID != 7 || got.UserID != 2 || got.ClassID != 1 || got.AgentID != 5 || got.Title != "什么是集合？" {
		t.Fatalf("会话字段不符: %+v", got)
	}

	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "user_id = ?") {
		t.Fatalf("属主条件必须写在 SQL 层，语句为: %s", log.sql)
	}
	if len(log.args) != 2 || log.args[0].Value != int64(7) || log.args[1].Value != int64(2) {
		t.Fatalf("参数必须是 (会话ID, user_id)，实际: %v", log.args)
	}
}

// 他人会话与不存在会话一律按不存在处理。
func TestConversationForUserNotFound(t *testing.T) {
	repo, _ := newTestRepo(0) // 无预置行 → sql.ErrNoRows 路径

	if _, err := repo.ConversationForUser(context.Background(), 7, 999); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("无行时期望 ErrConversationNotFound，得到 %v", err)
	}
}

// 会话列表必须按 user_id 过滤。
func TestConversationsByUserFilter(t *testing.T) {
	rows := &fakeRows{cols: []string{"id", "class_id", "user_id", "title", "created_at"}}
	repo, conn := newTestRepo(0, rows)

	out, err := repo.ConversationsByUser(context.Background(), 2)
	if err != nil {
		t.Fatalf("列出会话失败: %v", err)
	}
	if out == nil || len(out) != 0 {
		t.Fatalf("空结果应为空切片，得到 %v", out)
	}

	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "WHERE user_id = ?") {
		t.Fatalf("列表必须以 user_id 过滤，语句为: %s", log.sql)
	}
	if len(log.args) != 1 || log.args[0].Value != int64(2) {
		t.Fatalf("参数必须是 user_id，实际: %v", log.args)
	}
}

// 技能开关必须限定本班（JOIN assistants 且 class_id 条件）。
func TestSetSkillEnabledClassScoped(t *testing.T) {
	repo, conn := newTestRepo(1)

	if err := repo.SetSkillEnabled(context.Background(), 1, 5, true); err != nil {
		t.Fatalf("开关技能失败: %v", err)
	}
	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "JOIN assistants") || !strings.Contains(log.sql, "a.class_id = ?") {
		t.Fatalf("技能更新必须限定本班助手，语句为: %s", log.sql)
	}
	if len(log.args) != 3 || log.args[0].Value != true || log.args[1].Value != int64(5) || log.args[2].Value != int64(1) {
		t.Fatalf("参数必须是 (enabled, skillID, classID)，实际: %v", log.args)
	}
}

// 影响行数为 0 视为技能不存在。
func TestSetSkillEnabledNotFound(t *testing.T) {
	repo, _ := newTestRepo(0)
	if err := repo.SetSkillEnabled(context.Background(), 1, 999, false); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("期望 ErrSkillNotFound，得到 %v", err)
	}
}

// ---------- 技能 CRUD ----------

// 新建技能：INSERT...SELECT 把 assistant 解析落在 SQL 层，默认停用。
func TestCreateSkillClassScoped(t *testing.T) {
	repo, conn := newTestRepo(1)
	sk, err := repo.CreateSkill(context.Background(), 1, "英语写作", "润色术语", SkillSourceOnline)
	if err != nil {
		t.Fatalf("新建技能失败: %v", err)
	}
	if sk.ID != 101 || sk.Name != "英语写作" || sk.Source != SkillSourceOnline || sk.Enabled {
		t.Fatalf("新建技能字段不符: %+v", sk)
	}
	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "FROM assistants a WHERE a.class_id = ?") {
		t.Fatalf("新建必须按本班助手解析，语句为: %s", log.sql)
	}
	if len(log.args) != 4 || log.args[3].Value != int64(1) {
		t.Fatalf("参数必须包含 class_id，实际: %v", log.args)
	}
}

// 本班助手不存在时不写入任何行，映射为 ErrAssistantNotFound。
func TestCreateSkillAssistantMissing(t *testing.T) {
	repo, _ := newTestRepo(0)
	if _, err := repo.CreateSkill(context.Background(), 99, "n", "c", SkillSourceUpload); !errors.Is(err, ErrAssistantNotFound) {
		t.Fatalf("期望 ErrAssistantNotFound，得到 %v", err)
	}
}

// 删除自建技能：SQL 层同时限定本班与 builtin=0。
func TestDeleteSkillClassScopedBuiltinGuard(t *testing.T) {
	repo, conn := newTestRepo(1)
	if err := repo.DeleteSkill(context.Background(), 1, 5); err != nil {
		t.Fatalf("删除技能失败: %v", err)
	}
	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "a.class_id = ?") || !strings.Contains(log.sql, "s.builtin = 0") {
		t.Fatalf("删除必须同时限定本班与非内置，语句为: %s", log.sql)
	}
}

// 技能不存在（含他人班级）→ 404 哨兵。
func TestDeleteSkillNotFound(t *testing.T) {
	repo, _ := newTestRepo(0) // 删除 0 行，存在性查询返回空
	if err := repo.DeleteSkill(context.Background(), 1, 999); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("期望 ErrSkillNotFound，得到 %v", err)
	}
}

// 存在但 builtin=1 → 删除保护哨兵。
func TestDeleteSkillBuiltinProtected(t *testing.T) {
	exists := &fakeRows{cols: []string{"1"}, rows: [][]driver.Value{{int64(1)}}}
	repo, _ := newTestRepo(0, exists) // 删除 0 行；存在性查询命中
	if err := repo.DeleteSkill(context.Background(), 1, 4); !errors.Is(err, ErrSkillBuiltinProtected) {
		t.Fatalf("期望 ErrSkillBuiltinProtected，得到 %v", err)
	}
}

// ---------- citations JSON 往返 ----------

func TestCitationsJSONRoundtrip(t *testing.T) {
	want := []Citation{
		{
			DocumentID: 3,
			FileName:   "《A班·数学第一章·集合讲义》.txt",
			FileType:   "txt",
			Snippet:    "集合是由确定的、互不相同的对象构成的整体。",
			Locator:    model.Locator{Kind: model.LocatorLines, StartLine: 4, EndLine: 9},
			Score:      0.87,
		},
		{
			DocumentID: 4,
			FileName:   "《A班·数学第一章·集合补充阅读》.md",
			FileType:   "md",
			Snippet:    "## 子集与真子集",
			Locator:    model.Locator{Kind: model.LocatorHeading, Path: "第一章/子集"},
			Score:      0.72,
		},
	}

	// 写入：citations 列值必须是合法 JSON，且反序列化后与原值等价。
	repo, conn := newTestRepo(1)
	if _, err := repo.AppendMessage(context.Background(), 7, NewMessage{
		Role: RoleAssistant, Content: "先回顾定义……", Citations: want,
		PromptUsed: "p", SkillsUsed: []SkillRef{{ID: 7, Name: "解题引导"}},
	}); err != nil {
		t.Fatalf("写入消息失败: %v", err)
	}
	log := lastLog(t, conn)
	raw, ok := log.args[3].Value.(string)
	if !ok {
		t.Fatalf("citations 参数必须是 JSON 字符串，实际 %T", log.args[3].Value)
	}
	gotBack, err := unmarshalCitations([]byte(raw))
	if err != nil {
		t.Fatalf("JSON 反序列化失败: %v", err)
	}
	if len(gotBack) != 2 || gotBack[0] != want[0] || gotBack[1] != want[1] {
		t.Fatalf("JSON 往返不等价:\n got  %+v\n want %+v", gotBack, want)
	}
	// skills_used 快照同样以 JSON 写入，可反序列化还原。
	rawSkills, ok := log.args[5].Value.(string)
	if !ok {
		t.Fatalf("skills_used 参数必须是 JSON 字符串，实际 %T", log.args[5].Value)
	}
	var gotSkills []SkillRef
	if err := json.Unmarshal([]byte(rawSkills), &gotSkills); err != nil {
		t.Fatalf("技能快照反序列化失败: %v", err)
	}
	if len(gotSkills) != 1 || gotSkills[0] != (SkillRef{ID: 7, Name: "解题引导"}) {
		t.Fatalf("技能快照往返不等价: %+v", gotSkills)
	}
}

// user 轮与无命中的 assistant 轮：citations 必须为 SQL NULL，不写 "null"。
func TestAppendMessageNullCitations(t *testing.T) {
	repo, conn := newTestRepo(1)
	if _, err := repo.AppendMessage(context.Background(), 7, NewMessage{
		Role: RoleUser, Content: "这道题怎么做？",
	}); err != nil {
		t.Fatalf("写入 user 消息失败: %v", err)
	}
	log := lastLog(t, conn)
	if log.args[3].Value != nil {
		t.Fatalf("user 轮 citations 必须为 NULL，实际 %v", log.args[3].Value)
	}
	if log.args[5].Value != nil {
		t.Fatalf("user 轮 skills_used 必须为 NULL，实际 %v", log.args[5].Value)
	}

	repo2, conn2 := newTestRepo(1)
	if _, err := repo2.AppendMessage(context.Background(), 7, NewMessage{
		Role: RoleAssistant, Content: "资料中未找到。",
	}); err != nil {
		t.Fatalf("写入 assistant 消息失败: %v", err)
	}
	log2 := lastLog(t, conn2)
	if log2.args[3].Value != nil {
		t.Fatalf("无引用 assistant 轮 citations 必须为 NULL，实际 %v", log2.args[3].Value)
	}
}

// 读取路径：JSON 列与 NULL 列都能正确还原。
func TestMessagesByConversationDeserialization(t *testing.T) {
	cs := []Citation{{
		DocumentID: 3, FileName: "讲义.txt", FileType: "txt", Snippet: "内容",
		Locator: model.Locator{Kind: model.LocatorLines, StartLine: 1, EndLine: 2}, Score: 0.5,
	}}
	rawJSON, err := marshalCitationsForTest(cs)
	if err != nil {
		t.Fatal(err)
	}
	skillsJSON, err := json.Marshal([]SkillRef{{ID: 7, Name: "解题引导"}})
	if err != nil {
		t.Fatal(err)
	}
	rows := &fakeRows{
		cols: []string{"id", "conversation_id", "role", "content", "citations", "skills_used", "created_at"},
		rows: [][]driver.Value{
			{int64(1), int64(7), RoleUser, "题目？", nil, nil, time.Now()},
			{int64(2), int64(7), RoleAssistant, "思路……", rawJSON, skillsJSON, time.Now()},
			{int64(3), int64(7), RoleAssistant, "资料中未找到。", []byte("[]"), nil, time.Now()},
		},
	}
	repo, _ := newTestRepo(0, rows)

	msgs, err := repo.MessagesByConversation(context.Background(), 7)
	if err != nil {
		t.Fatalf("读取消息失败: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("应读到 3 条消息，得到 %d", len(msgs))
	}
	if msgs[0].Citations != nil {
		t.Fatalf("user 轮引用应为空，得到 %v", msgs[0].Citations)
	}
	if len(msgs[1].Citations) != 1 || msgs[1].Citations[0] != cs[0] {
		t.Fatalf("assistant 轮引用还原错误: %+v", msgs[1].Citations)
	}
	if len(msgs[1].SkillsUsed) != 1 || msgs[1].SkillsUsed[0].Name != "解题引导" {
		t.Fatalf("assistant 轮技能快照还原错误: %+v", msgs[1].SkillsUsed)
	}
	if msgs[2].Citations == nil || len(msgs[2].Citations) != 0 {
		t.Fatalf("空数组 JSON 应还原为空切片，得到 %v", msgs[2].Citations)
	}
	if msgs[2].SkillsUsed != nil {
		t.Fatalf("无快照时应为 nil，得到 %v", msgs[2].SkillsUsed)
	}
}

func marshalCitationsForTest(cs []Citation) ([]byte, error) {
	return json.Marshal(cs)
}

// ---------- 助手配置读取 ----------

func TestAssistantConfigJoin(t *testing.T) {
	rows := &fakeRows{
		cols: []string{"a.id", "a.name", "a.system_prompt", "s.id", "s.name", "s.description",
			"s.content", "s.source", "s.builtin", "s.enabled"},
		rows: [][]driver.Value{
			{int64(9), "A班·解题助手", "你是本班解题助教……", int64(4), "解题引导", "开启时……",
				"学生正在解题……", "seed", true, true},
		},
	}
	repo, conn := newTestRepo(0, rows)

	cfg, err := repo.AssistantConfig(context.Background(), 1)
	if err != nil {
		t.Fatalf("读取助手配置失败: %v", err)
	}
	if cfg.AssistantID != 9 || cfg.SystemPrompt == "" || len(cfg.Skills) != 1 || !cfg.Skills[0].Enabled {
		t.Fatalf("助手配置不符: %+v", cfg)
	}
	sk := cfg.Skills[0]
	if sk.Content != "学生正在解题……" || sk.Source != "seed" || !sk.Builtin {
		t.Fatalf("技能完整字段不符: %+v", sk)
	}
	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "a.class_id = ?") {
		t.Fatalf("助手配置必须按班级读取，语句为: %s", log.sql)
	}
}

// LEFT JOIN 无技能行时配置仍可用（技能列表为空）。
func TestAssistantConfigWithoutSkills(t *testing.T) {
	rows := &fakeRows{
		cols: []string{"a.id", "a.name", "a.system_prompt", "s.id", "s.name", "s.description",
			"s.content", "s.source", "s.builtin", "s.enabled"},
		rows: [][]driver.Value{{int64(9), "A班·解题助手", nil, nil, nil, nil, nil, nil, nil, nil}},
	}
	repo, _ := newTestRepo(0, rows)

	cfg, err := repo.AssistantConfig(context.Background(), 1)
	if err != nil {
		t.Fatalf("读取助手配置失败: %v", err)
	}
	if cfg.SystemPrompt != "" {
		t.Fatalf("NULL system_prompt 应读为空串，得到 %q", cfg.SystemPrompt)
	}
	if len(cfg.Skills) != 0 {
		t.Fatalf("无技能行时技能列表应为空，得到 %v", cfg.Skills)
	}
}

// 无助手行时报 ErrAssistantNotFound。
func TestAssistantConfigNotFound(t *testing.T) {
	repo, _ := newTestRepo(0)
	if _, err := repo.AssistantConfig(context.Background(), 1); !errors.Is(err, ErrAssistantNotFound) {
		t.Fatalf("期望 ErrAssistantNotFound，得到 %v", err)
	}
}

// ---------- 标题生成 ----------

func TestTruncateTitle(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"已知集合A={1,2,3}，求A的子集个数，并说明理由。", "已知集合A={1,2,3}，求A的子集个数，并说明理由。"}, // ≤30 字
		{strings.Repeat("题", 31), strings.Repeat("题", 30)},
		{"  两端空白应去除  ", "两端空白应去除"},
		{"", "新的解题会话"},
	}
	for _, c := range cases {
		if got := TruncateTitle(c.in); got != c.want {
			t.Fatalf("TruncateTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// 30 字边界：31 个 rune 截到 30，不产生乱码。
	long := strings.Repeat("数", 35)
	if got := TruncateTitle(long); got != strings.Repeat("数", 30) {
		t.Fatalf("rune 截断错误: %q", got)
	}
}

// 首问改名：仅在会话尚无消息时执行。
func TestRenameConversationIfEmpty(t *testing.T) {
	// 无消息 → 执行改名。
	repo, conn := newTestRepo(0, &fakeRows{cols: []string{"COUNT(*)"}, rows: [][]driver.Value{{int64(0)}}})
	if err := repo.RenameConversationIfEmpty(context.Background(), 7, "新问题"); err != nil {
		t.Fatalf("改名失败: %v", err)
	}
	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "UPDATE conversations SET title") {
		t.Fatalf("应执行改名，语句为: %s", log.sql)
	}

	// 已有消息 → 不改名。
	repo2, conn2 := newTestRepo(0, &fakeRows{cols: []string{"COUNT(*)"}, rows: [][]driver.Value{{int64(4)}}})
	if err := repo2.RenameConversationIfEmpty(context.Background(), 7, "新问题"); err != nil {
		t.Fatalf("检查消息数失败: %v", err)
	}
	log2 := lastLog(t, conn2)
	if strings.Contains(log2.sql, "UPDATE conversations") {
		t.Fatalf("已有消息时不得改名，语句为: %s", log2.sql)
	}
}

// ---------- 智能体 CRUD ----------

// allLogs 复制全部 SQL 记录，供多步操作断言。
func allLogs(t *testing.T, conn *fakeConn) []queryLog {
	t.Helper()
	conn.mu.Lock()
	defer conn.mu.Unlock()
	out := make([]queryLog, len(conn.logs))
	copy(out, conn.logs)
	return out
}

func agentRow(id, classID int64, name string, prompt any, now time.Time) *fakeRows {
	return &fakeRows{
		cols: []string{"id", "class_id", "name", "system_prompt", "created_at", "updated_at"},
		rows: [][]driver.Value{{id, classID, name, prompt, now, now}},
	}
}

// 新建智能体：班级取参数，落库后回读；班级条件必须写在 SQL 层。
func TestCreateAgentSuccess(t *testing.T) {
	now := time.Now()
	repo, conn := newTestRepo(1, agentRow(101, 1, "答疑助手", "你是本班助教……", now))

	got, err := repo.CreateAgent(context.Background(), 1, "答疑助手", "你是本班助教……")
	if err != nil {
		t.Fatalf("新建智能体失败: %v", err)
	}
	if got.ID != 101 || got.Name != "答疑助手" || got.ClassID != 1 || got.SystemPrompt != "你是本班助教……" {
		t.Fatalf("智能体字段不符: %+v", got)
	}
	logs := allLogs(t, conn)
	if !strings.Contains(logs[0].sql, "INSERT INTO agents (class_id, name, system_prompt)") {
		t.Fatalf("新建语句错误: %s", logs[0].sql)
	}
	if len(logs[0].args) != 3 || logs[0].args[0].Value != int64(1) || logs[0].args[1].Value != "答疑助手" {
		t.Fatalf("新建参数必须是 (class_id, name, prompt)，实际: %v", logs[0].args)
	}
	// 空提示词映射为 NULL。
	repo2, conn2 := newTestRepo(1, agentRow(102, 1, "空提示词", nil, now))
	if _, err := repo2.CreateAgent(context.Background(), 1, "空提示词", ""); err != nil {
		t.Fatalf("新建智能体失败: %v", err)
	}
	if args := allLogs(t, conn2)[0].args; args[2].Value != nil {
		t.Fatalf("空提示词必须写 NULL，实际 %v", args[2].Value)
	}
}

// 同班重名：唯一键冲突映射为 ErrAgentNameConflict（HTTP 400）。
func TestCreateAgentNameConflict(t *testing.T) {
	repo, conn := newTestRepo(1)
	conn.execErr = &mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}
	if _, err := repo.CreateAgent(context.Background(), 1, "重复名", "p"); !errors.Is(err, ErrAgentNameConflict) {
		t.Fatalf("期望 ErrAgentNameConflict，得到 %v", err)
	}
}

// 改名：UPDATE 限定 (id, class_id)；命中后回读。
func TestUpdateAgentSuccess(t *testing.T) {
	now := time.Now()
	repo, conn := newTestRepo(1, agentRow(101, 1, "新名", "新提示词", now))

	got, err := repo.UpdateAgent(context.Background(), 1, 101, "新名", "新提示词")
	if err != nil {
		t.Fatalf("改名失败: %v", err)
	}
	if got.Name != "新名" || got.SystemPrompt != "新提示词" {
		t.Fatalf("改名结果不符: %+v", got)
	}
	upd := allLogs(t, conn)[0]
	if !strings.Contains(upd.sql, "UPDATE agents SET name = ?") || !strings.Contains(upd.sql, "class_id = ?") {
		t.Fatalf("改名必须限定本班，语句为: %s", upd.sql)
	}
	if len(upd.args) != 4 || upd.args[2].Value != int64(101) || upd.args[3].Value != int64(1) {
		t.Fatalf("改名参数必须是 (name, prompt, id, class_id)，实际: %v", upd.args)
	}
}

// 改名冲突：唯一键冲突映射为 ErrAgentNameConflict。
func TestUpdateAgentNameConflict(t *testing.T) {
	repo, conn := newTestRepo(1)
	conn.execErr = &mysql.MySQLError{Number: 1062}
	if _, err := repo.UpdateAgent(context.Background(), 1, 101, "重复名", "p"); !errors.Is(err, ErrAgentNameConflict) {
		t.Fatalf("期望 ErrAgentNameConflict，得到 %v", err)
	}
}

// 跨班改名：UPDATE 0 行且存在性查询落空 → ErrAgentNotFound（HTTP 404）。
func TestUpdateAgentCrossClass(t *testing.T) {
	repo, conn := newTestRepo(0) // UPDATE 0 行；存在性查询无预置行 → ErrNoRows
	if _, err := repo.UpdateAgent(context.Background(), 2, 101, "n", "p"); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("期望 ErrAgentNotFound，得到 %v", err)
	}
	if logs := allLogs(t, conn); len(logs) != 2 || !strings.Contains(logs[1].sql, "SELECT 1 FROM agents") {
		t.Fatalf("应执行存在性复查，日志: %+v", logs)
	}
}

// 跨班读取：SQL 含 class_id 条件，无行 → ErrAgentNotFound。
func TestGetAgentCrossClass(t *testing.T) {
	repo, conn := newTestRepo(0)
	if _, err := repo.GetAgent(context.Background(), 2, 101); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("期望 ErrAgentNotFound，得到 %v", err)
	}
	if log := lastLog(t, conn); !strings.Contains(log.sql, "id = ? AND class_id = ?") {
		t.Fatalf("读取必须限定本班，语句为: %s", log.sql)
	}
}

// 列表：仅本班智能体，按 id 升序。
func TestListAgentsClassScoped(t *testing.T) {
	now := time.Now()
	rows := &fakeRows{
		cols: []string{"id", "class_id", "name", "system_prompt", "created_at", "updated_at"},
		rows: [][]driver.Value{
			{int64(5), int64(1), "答疑助手", "提示词一", now, now},
			{int64(6), int64(1), "作文助手", nil, now, now},
		},
	}
	repo, conn := newTestRepo(0, rows)

	out, err := repo.ListAgents(context.Background(), 1)
	if err != nil {
		t.Fatalf("列出智能体失败: %v", err)
	}
	if len(out) != 2 || out[0].ID != 5 || out[1].Name != "作文助手" || out[1].SystemPrompt != "" {
		t.Fatalf("列表内容不符: %+v", out)
	}
	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "WHERE class_id = ?") || !strings.Contains(log.sql, "ORDER BY id") {
		t.Fatalf("列表必须按本班且升序，语句为: %s", log.sql)
	}
}

// 删除：0 行 → ErrAgentNotFound；无会话引用时删除成功。
func TestDeleteAgent(t *testing.T) {
	repo, conn := newTestRepo(1)
	if err := repo.DeleteAgent(context.Background(), 1, 101); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if log := lastLog(t, conn); !strings.Contains(log.sql, "DELETE FROM agents") || !strings.Contains(log.sql, "class_id = ?") {
		t.Fatalf("删除必须限定本班，语句为: %s", log.sql)
	}

	repo2, _ := newTestRepo(0)
	if err := repo2.DeleteAgent(context.Background(), 1, 999); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("期望 ErrAgentNotFound，得到 %v", err)
	}
}

// 删除被会话引用的智能体：外键 RESTRICT 报错映射为 ErrAgentInUse。
func TestDeleteAgentInUse(t *testing.T) {
	repo, conn := newTestRepo(0)
	conn.execErr = &mysql.MySQLError{Number: 1451, Message: "Cannot delete or update a parent row"}
	if err := repo.DeleteAgent(context.Background(), 1, 101); !errors.Is(err, ErrAgentInUse) {
		t.Fatalf("期望 ErrAgentInUse，得到 %v", err)
	}
}

// ---------- 绑定替换 ----------

// 全量替换技能绑定：先校验智能体归属与技能归属，再删除旧绑定并写入去重后的入参。
func TestReplaceAgentSkillsSuccess(t *testing.T) {
	exists := &fakeRows{cols: []string{"1"}, rows: [][]driver.Value{{int64(1)}}}
	count := &fakeRows{cols: []string{"COUNT(*)"}, rows: [][]driver.Value{{int64(2)}}}
	repo, conn := newTestRepo(1, exists, count)

	if err := repo.ReplaceAgentSkills(context.Background(), 1, 101, []int64{30, 7, 7}); err != nil {
		t.Fatalf("替换技能绑定失败: %v", err)
	}
	logs := allLogs(t, conn)
	for i, want := range []string{"SELECT 1 FROM agents", "SELECT COUNT(*)", "DELETE FROM agent_skills", "INSERT INTO agent_skills"} {
		if !strings.Contains(logs[i].sql, want) {
			t.Fatalf("第 %d 步语句应为含 %q，实际: %s", i+1, want, logs[i].sql)
		}
	}
	// 技能归属校验必须先于删除执行。
	if len(logs) != 4 {
		t.Fatalf("应执行 4 条语句，实际 %d", len(logs))
	}
	ins := logs[3].args
	if len(ins) != 4 || ins[0].Value != int64(101) || ins[1].Value != int64(30) ||
		ins[2].Value != int64(101) || ins[3].Value != int64(7) {
		t.Fatalf("写入绑定必须去重且成对，实际: %v", ins)
	}
	// 去重后与入参集合精确一致。
	if len(ins) != 2*2 {
		t.Fatalf("重复入参必须被去重，实际绑定对数: %d", len(ins)/2)
	}
}

// 跨班或不存在的技能：返回 ErrSkillNotFound，且不执行删除与写入（绑定不变）。
func TestReplaceAgentSkillsCrossClass(t *testing.T) {
	exists := &fakeRows{cols: []string{"1"}, rows: [][]driver.Value{{int64(1)}}}
	count := &fakeRows{cols: []string{"COUNT(*)"}, rows: [][]driver.Value{{int64(1)}}}
	repo, conn := newTestRepo(1, exists, count)

	if err := repo.ReplaceAgentSkills(context.Background(), 1, 101, []int64{30, 999}); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("期望 ErrSkillNotFound，得到 %v", err)
	}
	for _, l := range allLogs(t, conn) {
		if strings.Contains(l.sql, "DELETE FROM agent_skills") || strings.Contains(l.sql, "INSERT INTO agent_skills") {
			t.Fatalf("校验失败时不得改动绑定，语句为: %s", l.sql)
		}
	}
}

// 跨班或不存在的讲义：返回 ErrHandoutNotFound，且不改动绑定。
func TestReplaceAgentMaterialsUnknownHandout(t *testing.T) {
	exists := &fakeRows{cols: []string{"1"}, rows: [][]driver.Value{{int64(1)}}}
	count := &fakeRows{cols: []string{"COUNT(*)"}, rows: [][]driver.Value{{int64(0)}}}
	repo, conn := newTestRepo(1, exists, count)

	if err := repo.ReplaceAgentMaterials(context.Background(), 1, 101, []int64{8}); !errors.Is(err, ErrHandoutNotFound) {
		t.Fatalf("期望 ErrHandoutNotFound，得到 %v", err)
	}
	for _, l := range allLogs(t, conn) {
		if strings.Contains(l.sql, "INSERT INTO agent_materials") {
			t.Fatalf("校验失败时不得写入绑定，语句为: %s", l.sql)
		}
	}
}

// 材料归属校验必须落在本班 handouts 上。
func TestReplaceAgentMaterialsClassScoped(t *testing.T) {
	exists := &fakeRows{cols: []string{"1"}, rows: [][]driver.Value{{int64(1)}}}
	count := &fakeRows{cols: []string{"COUNT(*)"}, rows: [][]driver.Value{{int64(1)}}}
	repo, conn := newTestRepo(1, exists, count)

	if err := repo.ReplaceAgentMaterials(context.Background(), 1, 101, []int64{8}); err != nil {
		t.Fatalf("替换材料绑定失败: %v", err)
	}
	logs := allLogs(t, conn)
	if !strings.Contains(logs[1].sql, "FROM handouts") || !strings.Contains(logs[1].sql, "class_id = ?") {
		t.Fatalf("材料归属校验必须限定本班，语句为: %s", logs[1].sql)
	}
	if len(logs[1].args) != 2 || logs[1].args[0].Value != int64(1) {
		t.Fatalf("材料校验参数必须含 class_id，实际: %v", logs[1].args)
	}
	if !strings.Contains(logs[3].sql, "INSERT INTO agent_materials") {
		t.Fatalf("应写入材料绑定，语句为: %s", logs[3].sql)
	}
}

// 空绑定集合：清空绑定，不写入任何行。
func TestReplaceAgentBindingsEmpty(t *testing.T) {
	exists := &fakeRows{cols: []string{"1"}, rows: [][]driver.Value{{int64(1)}}}
	repo, conn := newTestRepo(1, exists)
	if err := repo.ReplaceAgentSkills(context.Background(), 1, 101, nil); err != nil {
		t.Fatalf("清空技能绑定失败: %v", err)
	}
	logs := allLogs(t, conn)
	if len(logs) != 2 || !strings.Contains(logs[0].sql, "SELECT 1 FROM agents") ||
		!strings.Contains(logs[1].sql, "DELETE FROM agent_skills") {
		t.Fatalf("空集合只需存在性校验与清空，日志: %+v", logs)
	}
}

// ---------- AgentConfig 读取 ----------

// 绑定且已启用的技能（升序）+ 绑定讲义集合。
func TestAgentConfigWithBindings(t *testing.T) {
	agent := &fakeRows{cols: []string{"name", "system_prompt"}, rows: [][]driver.Value{{"答疑助手", "你是本班助教……"}}}
	skills := &fakeRows{
		cols: []string{"s.id", "s.name", "s.description", "s.content", "s.source", "s.builtin", "s.enabled"},
		rows: [][]driver.Value{
			{int64(4), "解题引导", "引导描述", "不要直接给出该题的最终答案", "seed", true, true},
			{int64(9), "英语写作", "", "润色术语", "online", false, true},
		},
	}
	handouts := &fakeRows{cols: []string{"handout_id"}, rows: [][]driver.Value{{int64(3)}, {int64(9)}}}
	repo, conn := newTestRepo(0, agent, skills, handouts)

	cfg, err := repo.AgentConfig(context.Background(), 1, 101)
	if err != nil {
		t.Fatalf("读取智能体配置失败: %v", err)
	}
	if cfg.AgentID != 101 || cfg.ClassID != 1 || cfg.Name != "答疑助手" || cfg.SystemPrompt != "你是本班助教……" {
		t.Fatalf("智能体配置不符: %+v", cfg)
	}
	if len(cfg.Skills) != 2 || cfg.Skills[0].ID != 4 || cfg.Skills[1].ID != 9 || !cfg.Skills[0].Enabled {
		t.Fatalf("绑定技能不符: %+v", cfg.Skills)
	}
	if len(cfg.HandoutIDs) != 2 || cfg.HandoutIDs[0] != 3 || cfg.HandoutIDs[1] != 9 {
		t.Fatalf("绑定材料不符: %+v", cfg.HandoutIDs)
	}
	logs := allLogs(t, conn)
	if !strings.Contains(logs[0].sql, "FROM agents WHERE id = ? AND class_id = ?") {
		t.Fatalf("智能体读取必须限定本班，语句为: %s", logs[0].sql)
	}
	if !strings.Contains(logs[1].sql, "s.enabled = 1") || !strings.Contains(logs[1].sql, "ORDER BY s.id") {
		t.Fatalf("技能读取必须只取已启用且升序，语句为: %s", logs[1].sql)
	}
}

// 无绑定：技能与材料均为空切片（非 nil）。
func TestAgentConfigWithoutBindings(t *testing.T) {
	agent := &fakeRows{cols: []string{"name", "system_prompt"}, rows: [][]driver.Value{{"空助手", nil}}}
	repo, _ := newTestRepo(0, agent, &fakeRows{cols: []string{"s.id"}}, &fakeRows{cols: []string{"handout_id"}})

	cfg, err := repo.AgentConfig(context.Background(), 1, 101)
	if err != nil {
		t.Fatalf("读取智能体配置失败: %v", err)
	}
	if cfg.SystemPrompt != "" {
		t.Fatalf("NULL 提示词应读为空串，得到 %q", cfg.SystemPrompt)
	}
	if cfg.Skills == nil || len(cfg.Skills) != 0 {
		t.Fatalf("无绑定技能应为空切片，得到 %v", cfg.Skills)
	}
	if cfg.HandoutIDs == nil || len(cfg.HandoutIDs) != 0 {
		t.Fatalf("无绑定材料应为空切片，得到 %v", cfg.HandoutIDs)
	}
}

// 智能体不存在（含跨班）→ ErrAgentNotFound。
func TestAgentConfigNotFound(t *testing.T) {
	repo, _ := newTestRepo(0)
	if _, err := repo.AgentConfig(context.Background(), 1, 999); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("期望 ErrAgentNotFound，得到 %v", err)
	}
}

// 仅停用技能：SQL 层过滤后返回空技能集。
func TestAgentConfigDisabledSkillsFiltered(t *testing.T) {
	agent := &fakeRows{cols: []string{"name", "system_prompt"}, rows: [][]driver.Value{{"a", "p"}}}
	repo, conn := newTestRepo(0, agent, &fakeRows{cols: []string{"s.id"}}, &fakeRows{cols: []string{"handout_id"}})
	cfg, err := repo.AgentConfig(context.Background(), 1, 101)
	if err != nil {
		t.Fatalf("读取智能体配置失败: %v", err)
	}
	if len(cfg.Skills) != 0 {
		t.Fatalf("停用技能不得进入候选集，得到 %+v", cfg.Skills)
	}
	if logs := allLogs(t, conn); !strings.Contains(logs[1].sql, "s.enabled = 1") {
		t.Fatalf("必须在 SQL 层过滤停用技能，语句为: %s", logs[1].sql)
	}
}

// 绑定 id 读取（教师视图回显）。
func TestAgentBindingIDsRead(t *testing.T) {
	skills := &fakeRows{cols: []string{"skill_id"}, rows: [][]driver.Value{{int64(4)}, {int64(9)}}}
	repo, _ := newTestRepo(0, skills)
	ids, err := repo.AgentSkillIDs(context.Background(), 101)
	if err != nil {
		t.Fatalf("读取绑定技能失败: %v", err)
	}
	if len(ids) != 2 || ids[0] != 4 || ids[1] != 9 {
		t.Fatalf("绑定技能不符: %v", ids)
	}

	handouts := &fakeRows{cols: []string{"handout_id"}, rows: [][]driver.Value{{int64(8)}}}
	repo2, conn2 := newTestRepo(0, handouts)
	hids, err := repo2.AgentHandoutIDs(context.Background(), 101)
	if err != nil {
		t.Fatalf("读取绑定材料失败: %v", err)
	}
	if len(hids) != 1 || hids[0] != 8 {
		t.Fatalf("绑定材料不符: %v", hids)
	}
	if log := lastLog(t, conn2); !strings.Contains(log.sql, "FROM agent_materials") {
		t.Fatalf("材料绑定读取语句错误: %s", log.sql)
	}
}

// ---------- 会话绑定智能体 ----------

// 新建会话必须落库 agent_id，且班级/用户/智能体均来自服务端上下文。
func TestCreateConversationWritesAgentID(t *testing.T) {
	repo, conn := newTestRepo(1)
	id, err := repo.CreateConversation(context.Background(), 1, 2, 5, "什么是集合？")
	if err != nil {
		t.Fatalf("新建会话失败: %v", err)
	}
	if id != 101 {
		t.Fatalf("新建会话应返回自增 id，得到 %d", id)
	}
	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "INSERT INTO conversations (class_id, user_id, agent_id, title)") {
		t.Fatalf("新建语句必须含 agent_id，实际: %s", log.sql)
	}
	if len(log.args) != 4 || log.args[0].Value != int64(1) || log.args[1].Value != int64(2) ||
		log.args[2].Value != int64(5) || log.args[3].Value != "什么是集合？" {
		t.Fatalf("参数必须是 (class_id, user_id, agent_id, title)，实际: %v", log.args)
	}
}

// 会话列表必须回显 agent_id。
func TestConversationsByUserIncludesAgent(t *testing.T) {
	now := time.Now()
	rows := &fakeRows{
		cols: []string{"id", "class_id", "user_id", "agent_id", "title", "created_at"},
		rows: [][]driver.Value{{int64(7), int64(1), int64(2), int64(5), "标题", now}},
	}
	repo, _ := newTestRepo(0, rows)
	out, err := repo.ConversationsByUser(context.Background(), 2)
	if err != nil {
		t.Fatalf("列出会话失败: %v", err)
	}
	if len(out) != 1 || out[0].AgentID != 5 {
		t.Fatalf("会话应回显 agent_id，得到 %+v", out)
	}
}
