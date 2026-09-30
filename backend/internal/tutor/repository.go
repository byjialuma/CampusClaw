// Package tutor 编排解题助手：提示词/技能加载 → 历史组装 → 班级检索 → 流式生成。
package tutor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"campusclaw/internal/model"
)

// 错误哨兵：服务层与路由层据此映射 404/400。
var (
	ErrAssistantNotFound    = errors.New("本班助手不存在")
	ErrSkillNotFound        = errors.New("技能不存在")
	ErrConversationNotFound = errors.New("会话不存在")
)

// 角色取值（与 messages.role ENUM 一致）。
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Skill 是助手的一个技能（本课仅内置「解题引导」）。
type Skill struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

// AssistantConfig 是本班助手的配置：提示词与技能列表。
type AssistantConfig struct {
	AssistantID  int64
	Name         string
	SystemPrompt string // 可能为空（教师未保存过且播种缺失）
	Skills       []Skill
}

// Conversation 是一次多轮解题会话，归属唯一学生。
type Conversation struct {
	ID        int64     `json:"id"`
	ClassID   int64     `json:"-"`
	UserID    int64     `json:"-"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
}

// Citation 是回答中引用的一条本班材料来源（与知识问答的 Citation 同形，独立定义避免包耦合）。
type Citation struct {
	DocumentID int64         `json:"documentId"`
	FileName   string        `json:"fileName"`
	FileType   string        `json:"fileType"`
	Snippet    string        `json:"snippet"`
	Locator    model.Locator `json:"locator"`
	Score      float32       `json:"score"`
}

// Message 是会话中的一轮消息；assistant 轮持久化引用与当时的提示词/技能快照。
type Message struct {
	ID             int64      `json:"id"`
	ConversationID int64      `json:"conversationId"`
	Role           string     `json:"role"`
	Content        string     `json:"content"`
	Citations      []Citation `json:"citations,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
}

// NewMessage 是待写入的消息（assistant 轮需附快照字段）。
type NewMessage struct {
	Role         string
	Content      string
	Citations    []Citation
	PromptUsed   string
	SkillEnabled bool
}

// TutorRepository 助手配置、会话与消息的 MySQL 访问。
// 会话与消息的可见性一律以 user_id 属主条件落在 SQL 层，杜绝水平越权。
type TutorRepository struct {
	DB *sql.DB
}

// assistantConfig 是 AssistantConfig 查询的行载体。
type assistantConfigRow struct {
	AssistantID  int64
	Name         string
	SystemPrompt sql.NullString
	SkillID      sql.NullInt64
	SkillName    sql.NullString
	SkillDesc    sql.NullString
	SkillEnabled sql.NullBool
}

// AssistantConfig 读取本班助手提示词与技能列表；班级只信参数（来自 JWT claims）。
func (r *TutorRepository) AssistantConfig(ctx context.Context, classID int64) (*AssistantConfig, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT a.id, a.name, a.system_prompt,
		        s.id, s.name, s.description, s.enabled
		 FROM assistants a
		 LEFT JOIN skills s ON s.assistant_id = a.id
		 WHERE a.class_id = ?
		 ORDER BY s.id`,
		classID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cfg *AssistantConfig
	for rows.Next() {
		var row assistantConfigRow
		if err := rows.Scan(&row.AssistantID, &row.Name, &row.SystemPrompt,
			&row.SkillID, &row.SkillName, &row.SkillDesc, &row.SkillEnabled); err != nil {
			return nil, err
		}
		if cfg == nil {
			cfg = &AssistantConfig{
				AssistantID:  row.AssistantID,
				Name:         row.Name,
				SystemPrompt: row.SystemPrompt.String,
			}
		}
		if row.SkillID.Valid {
			cfg.Skills = append(cfg.Skills, Skill{
				ID:          row.SkillID.Int64,
				Name:        row.SkillName.String,
				Description: row.SkillDesc.String,
				Enabled:     row.SkillEnabled.Bool,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, ErrAssistantNotFound
	}
	return cfg, nil
}

// SavePrompt 教师保存本班助手提示词；从下一轮提问起生效。
func (r *TutorRepository) SavePrompt(ctx context.Context, classID int64, prompt string) error {
	res, err := r.DB.ExecContext(ctx,
		`UPDATE assistants SET system_prompt = ? WHERE class_id = ?`, prompt, classID)
	if err != nil {
		return err
	}
	// 提示词与现值相同时 MySQL 报告 RowsAffected=0，这不代表助手不存在；
	// 必须单独做存在性检查，保证幂等保存不会被误判为 404。
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		var one int
		if qerr := r.DB.QueryRowContext(ctx,
			`SELECT 1 FROM assistants WHERE class_id = ?`, classID).Scan(&one); qerr != nil {
			if errors.Is(qerr, sql.ErrNoRows) {
				return ErrAssistantNotFound
			}
			return qerr
		}
	}
	return nil
}

// SetSkillEnabled 教师启用/停用技能；技能必须属于本班助手。
func (r *TutorRepository) SetSkillEnabled(ctx context.Context, classID, skillID int64, enabled bool) error {
	res, err := r.DB.ExecContext(ctx,
		`UPDATE skills s
		 JOIN assistants a ON a.id = s.assistant_id
		 SET s.enabled = ?
		 WHERE s.id = ? AND a.class_id = ?`,
		enabled, skillID, classID)
	if err != nil {
		return err
	}
	// 目标状态与现值相同时 RowsAffected=0，用存在性查询区分技能是否真的不存在。
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		var one int
		if qerr := r.DB.QueryRowContext(ctx,
			`SELECT 1 FROM skills s
			 JOIN assistants a ON a.id = s.assistant_id
			 WHERE s.id = ? AND a.class_id = ?`, skillID, classID).Scan(&one); qerr != nil {
			if errors.Is(qerr, sql.ErrNoRows) {
				return ErrSkillNotFound
			}
			return qerr
		}
	}
	return nil
}

// CreateConversation 创建会话；class_id 与 user_id 均来自 JWT claims，不接受客户端传入。
func (r *TutorRepository) CreateConversation(ctx context.Context, classID, userID int64, title string) (int64, error) {
	res, err := r.DB.ExecContext(ctx,
		`INSERT INTO conversations (class_id, user_id, title) VALUES (?, ?, ?)`,
		classID, userID, title)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ConversationsByUser 返回本人全部会话（新→旧）。
func (r *TutorRepository) ConversationsByUser(ctx context.Context, userID int64) ([]Conversation, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT id, class_id, user_id, title, created_at
		 FROM conversations
		 WHERE user_id = ?
		 ORDER BY id DESC`,
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Conversation{}
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.ID, &c.ClassID, &c.UserID, &c.Title, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ConversationForUser 按 ID 取会话；属主条件在 SQL 层（WHERE id = ? AND user_id = ?），
// 他人会话与不存在会话一律按不存在处理。
func (r *TutorRepository) ConversationForUser(ctx context.Context, convID, userID int64) (*Conversation, error) {
	row := r.DB.QueryRowContext(ctx,
		`SELECT id, class_id, user_id, title, created_at
		 FROM conversations
		 WHERE id = ? AND user_id = ?`,
		convID, userID)
	var c Conversation
	err := row.Scan(&c.ID, &c.ClassID, &c.UserID, &c.Title, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrConversationNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// AppendMessage 写入一轮消息；assistant 轮同时持久化引用、提示词快照与技能状态。
func (r *TutorRepository) AppendMessage(ctx context.Context, convID int64, m NewMessage) (int64, error) {
	var citations any // user 轮保持 NULL
	if m.Role == RoleAssistant && m.Citations != nil {
		raw, err := json.Marshal(m.Citations)
		if err != nil {
			return 0, fmt.Errorf("序列化引用失败: %w", err)
		}
		citations = string(raw)
	}
	res, err := r.DB.ExecContext(ctx,
		`INSERT INTO messages (conversation_id, role, content, citations, prompt_used, skill_enabled)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		convID, m.Role, m.Content, citations, m.PromptUsed, m.SkillEnabled)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// MessagesByConversation 按时间升序读取会话全部消息（仅消息本身，不含提示词快照）。
func (r *TutorRepository) MessagesByConversation(ctx context.Context, convID int64) ([]Message, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT id, conversation_id, role, content, citations, created_at
		 FROM messages
		 WHERE conversation_id = ?
		 ORDER BY id`,
		convID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Message{}
	for rows.Next() {
		var m Message
		var rawCitations []byte
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &rawCitations, &m.CreatedAt); err != nil {
			return nil, err
		}
		if len(rawCitations) > 0 {
			cs, err := unmarshalCitations(rawCitations)
			if err != nil {
				return nil, err
			}
			m.Citations = cs
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RenameConversationIfEmpty 会话尚无消息时以首问生成标题（问题前 30 字）。
func (r *TutorRepository) RenameConversationIfEmpty(ctx context.Context, convID int64, question string) error {
	var n int
	if err := r.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE conversation_id = ?`, convID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := r.DB.ExecContext(ctx,
		`UPDATE conversations SET title = ? WHERE id = ?`, TruncateTitle(question), convID)
	return err
}

// titleMaxRunes 是会话标题的最大长度（rune）。
const titleMaxRunes = 30

// TruncateTitle 以问题前 30 字生成会话标题（按 rune 截断，不产生乱码）。
func TruncateTitle(question string) string {
	runes := []rune(strings.TrimSpace(question))
	if len(runes) > titleMaxRunes {
		runes = runes[:titleMaxRunes]
	}
	title := string(runes)
	if title == "" {
		title = "新的解题会话"
	}
	return title
}

// unmarshalCitations 反序列化 citations JSON 列。
func unmarshalCitations(raw []byte) ([]Citation, error) {
	var cs []Citation
	if err := json.Unmarshal(raw, &cs); err != nil {
		return nil, fmt.Errorf("解析引用 JSON 失败: %w", err)
	}
	if cs == nil {
		cs = []Citation{}
	}
	return cs, nil
}
