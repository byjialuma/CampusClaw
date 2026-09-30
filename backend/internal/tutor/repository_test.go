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
		cols: []string{"id", "class_id", "user_id", "title", "created_at"},
		rows: [][]driver.Value{{int64(7), int64(1), int64(2), "什么是集合？", now}},
	}
	repo, conn := newTestRepo(0, rows)

	got, err := repo.ConversationForUser(context.Background(), 7, 2)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if got.ID != 7 || got.UserID != 2 || got.ClassID != 1 || got.Title != "什么是集合？" {
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
		PromptUsed: "p", SkillEnabled: true,
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
	rows := &fakeRows{
		cols: []string{"id", "conversation_id", "role", "content", "citations", "created_at"},
		rows: [][]driver.Value{
			{int64(1), int64(7), RoleUser, "题目？", nil, time.Now()},
			{int64(2), int64(7), RoleAssistant, "思路……", rawJSON, time.Now()},
			{int64(3), int64(7), RoleAssistant, "资料中未找到。", []byte("[]"), time.Now()},
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
	if msgs[2].Citations == nil || len(msgs[2].Citations) != 0 {
		t.Fatalf("空数组 JSON 应还原为空切片，得到 %v", msgs[2].Citations)
	}
}

func marshalCitationsForTest(cs []Citation) ([]byte, error) {
	return json.Marshal(cs)
}

// ---------- 助手配置读取 ----------

func TestAssistantConfigJoin(t *testing.T) {
	rows := &fakeRows{
		cols: []string{"a.id", "a.name", "a.system_prompt", "s.id", "s.name", "s.description", "s.enabled"},
		rows: [][]driver.Value{
			{int64(9), "A班·解题助手", "你是本班解题助教……", int64(4), "解题引导", "开启时……", true},
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
	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "a.class_id = ?") {
		t.Fatalf("助手配置必须按班级读取，语句为: %s", log.sql)
	}
}

// LEFT JOIN 无技能行时配置仍可用（技能列表为空）。
func TestAssistantConfigWithoutSkills(t *testing.T) {
	rows := &fakeRows{
		cols: []string{"a.id", "a.name", "a.system_prompt", "s.id", "s.name", "s.description", "s.enabled"},
		rows: [][]driver.Value{{int64(9), "A班·解题助手", nil, nil, nil, nil, nil}},
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
