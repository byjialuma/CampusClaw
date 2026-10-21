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

	"github.com/go-sql-driver/mysql"

	"campusclaw/internal/mcp"
	"campusclaw/internal/model"
)

// 错误哨兵：服务层与路由层据此映射 404/400/409。
var (
	ErrAssistantNotFound     = errors.New("本班助手不存在")
	ErrSkillNotFound         = errors.New("技能不存在")
	ErrSkillBuiltinProtected = errors.New("内置技能不可删除")
	ErrConversationNotFound  = errors.New("会话不存在")
	ErrMCPServerNotFound     = errors.New("MCP 服务器不存在")
	ErrMCPServerNameConflict = errors.New("MCP 服务器名称已存在")
	ErrAgentNotFound         = errors.New("智能体不存在")
	ErrAgentNameConflict     = errors.New("本班智能体名称已存在")
	ErrAgentInUse            = errors.New("智能体仍被会话引用，无法删除")
	ErrHandoutNotFound       = errors.New("材料不存在")
	ErrSkillPackageConflict  = errors.New("本班已存在同名技能包")
)

// 技能来源取值（与 skills.source 约定一致）。
const (
	SkillSourceSeed    = "seed"    // 内置播种（如解题引导，builtin=1）
	SkillSourceUpload  = "upload"  // 教师上传 md/txt 文件创建
	SkillSourceOnline  = "online"  // 教师在线新建
	SkillSourcePackage = "package" // 教师上传 zip 技能包创建
)

// 角色取值（与 messages.role ENUM 一致）。
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Skill 是助手的一条技能（指令文档：名称 + 内容）；builtin=1 的内置技能不可删除。
// 技能包技能额外携带 slug、声明资源清单与包目录；纯文本技能这些字段为空。
type Skill struct {
	ID          int64           `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Content     string          `json:"content"`
	Source      string          `json:"source"`
	Builtin     bool            `json:"builtin"`
	Enabled     bool            `json:"enabled"`
	Slug        string          `json:"slug,omitempty"`
	Resources   []string        `json:"resources"`
	Files       []SkillResource `json:"files,omitempty"`
	DirPath     string          `json:"-"`
	Frontmatter map[string]any  `json:"-"`
	Conditions  []string        `json:"-"`
	ToolRefs    []ToolRef       `json:"-"`
}

// SkillRef 是某轮回答启用技能的快照（仅存 id+name，删除技能后历史仍可展示）。
type SkillRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// AssistantConfig 是本班助手的配置：提示词与技能列表。
type AssistantConfig struct {
	AssistantID  int64
	Name         string
	SystemPrompt string // 可能为空（教师未保存过且播种缺失）
	Skills       []Skill
}

// Conversation 是一次多轮解题会话，归属唯一学生；绑定一个本班智能体（会话内不切换）。
type Conversation struct {
	ID        int64     `json:"id"`
	ClassID   int64     `json:"-"`
	UserID    int64     `json:"-"`
	AgentID   int64     `json:"agentId"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
}

// Agent 是班级内的一个智能体：独立持有提示词，绑定自己的技能集与材料集。
// SystemPrompt 仅教师视图返回；学生视图不回传提示词（由 server 层裁剪）。
type Agent struct {
	ID           int64     `json:"id"`
	ClassID      int64     `json:"-"`
	Name         string    `json:"name"`
	SystemPrompt string    `json:"systemPrompt,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// AgentConfig 是某会话智能体在一次提问时所需的配置：
// 提示词、绑定且已启用的技能（按 skills.id 升序）与绑定的讲义 id 集合。
type AgentConfig struct {
	AgentID      int64
	ClassID      int64
	Name         string
	SystemPrompt string
	Skills       []Skill
	HandoutIDs   []int64
}

// Citation 是回答中引用的一条本班材料来源（与知识问答的 Citation 同形，独立定义避免包耦合）。
// Index 是引用列表序号（自 1 递增），与回答文本中的 [n] 标号一致。
type Citation struct {
	DocumentID int64         `json:"documentId"`
	FileName   string        `json:"fileName"`
	FileType   string        `json:"fileType"`
	Snippet    string        `json:"snippet"`
	Locator    model.Locator `json:"locator"`
	Score      float32       `json:"score"`
	Index      int           `json:"index"`
}

// Message 是会话中的一轮消息；assistant 轮持久化引用与当时的提示词/技能快照。
type Message struct {
	ID             int64      `json:"id"`
	ConversationID int64      `json:"conversationId"`
	Role           string     `json:"role"`
	Content        string     `json:"content"`
	Citations      []Citation `json:"citations,omitempty"`
	SkillsUsed     []SkillRef `json:"skillsUsed,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
}

// NewMessage 是待写入的消息（assistant 轮需附快照字段）。
type NewMessage struct {
	Role       string
	Content    string
	Citations  []Citation
	PromptUsed string
	SkillsUsed []SkillRef
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
	SkillContent sql.NullString
	SkillSource  sql.NullString
	SkillBuiltin sql.NullBool
	SkillEnabled sql.NullBool
	SkillSlug    sql.NullString
	Frontmatter  []byte
	DirPath      sql.NullString
}

// applySkillMeta 从 slug/frontmatter/dir_path 三列还原技能包元信息；
// frontmatter 缺失或非法时按纯文本技能处理（条件与工具回退正文解析）。
func applySkillMeta(sk *Skill, slug, dirPath sql.NullString, rawFM []byte) {
	sk.Slug = slug.String
	sk.DirPath = dirPath.String
	sk.Resources = []string{}
	sk.Conditions = []string{}
	sk.ToolRefs = []ToolRef{}
	if len(rawFM) == 0 {
		return
	}
	var fm map[string]any
	if err := json.Unmarshal(rawFM, &fm); err != nil {
		return
	}
	sk.Frontmatter = fm
	if v, ok := fm[skillFrontmatterCondKey]; ok {
		if conds, err := asStringList(skillFrontmatterCondKey, v); err == nil {
			sk.Conditions = conds
		}
	}
	if v, ok := fm[skillFrontmatterResourceKey]; ok {
		if res, err := asStringList(skillFrontmatterResourceKey, v); err == nil {
			sk.Resources = res
		}
	}
	if v, ok := fm[skillFrontmatterToolsKey]; ok {
		if items, err := asStringList(skillFrontmatterToolsKey, v); err == nil {
			for _, item := range items {
				if ref, errMsg := parseToolRef(strings.TrimSpace(item)); errMsg == "" {
					sk.ToolRefs = append(sk.ToolRefs, ref)
				}
			}
		}
	}
}

// AssistantConfig 读取本班助手提示词与全部技能（含内容/来源/内置标记）；班级只信参数（来自 JWT claims）。
func (r *TutorRepository) AssistantConfig(ctx context.Context, classID int64) (*AssistantConfig, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT a.id, a.name, a.system_prompt,
		        s.id, s.name, s.description, s.content, s.source, s.builtin, s.enabled,
		        s.slug, s.frontmatter, s.dir_path
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
			&row.SkillID, &row.SkillName, &row.SkillDesc, &row.SkillContent,
			&row.SkillSource, &row.SkillBuiltin, &row.SkillEnabled,
			&row.SkillSlug, &row.Frontmatter, &row.DirPath); err != nil {
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
			sk := Skill{
				ID:          row.SkillID.Int64,
				Name:        row.SkillName.String,
				Description: row.SkillDesc.String,
				Content:     row.SkillContent.String,
				Source:      row.SkillSource.String,
				Builtin:     row.SkillBuiltin.Bool,
				Enabled:     row.SkillEnabled.Bool,
			}
			applySkillMeta(&sk, row.SkillSlug, row.DirPath, row.Frontmatter)
			cfg.Skills = append(cfg.Skills, sk)
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

// CreateSkill 在本班助手下新建技能（来源 upload/online），默认停用，由教师显式启用。
func (r *TutorRepository) CreateSkill(ctx context.Context, classID int64, name, content, source string) (*Skill, error) {
	res, err := r.DB.ExecContext(ctx,
		`INSERT INTO skills (assistant_id, name, content, source, enabled)
		 SELECT a.id, ?, ?, ?, 0 FROM assistants a WHERE a.class_id = ?`,
		name, content, source, classID)
	if err != nil {
		return nil, err
	}
	// 本班助手不存在时 INSERT...SELECT 不写入任何行。
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return nil, ErrAssistantNotFound
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &Skill{ID: id, Name: name, Content: content, Source: source}, nil
}

// DeleteSkill 删除本班助手的自建技能；内置技能（builtin=1）拒绝删除，他人班级技能按不存在处理。
func (r *TutorRepository) DeleteSkill(ctx context.Context, classID, skillID int64) error {
	res, err := r.DB.ExecContext(ctx,
		`DELETE s FROM skills s
		 JOIN assistants a ON a.id = s.assistant_id
		 WHERE s.id = ? AND a.class_id = ? AND s.builtin = 0`,
		skillID, classID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return nil
	}
	// 未删除任何行：区分技能不存在（404）与内置技能删除保护（409）。
	var one int
	qerr := r.DB.QueryRowContext(ctx,
		`SELECT 1 FROM skills s
		 JOIN assistants a ON a.id = s.assistant_id
		 WHERE s.id = ? AND a.class_id = ?`, skillID, classID).Scan(&one)
	if errors.Is(qerr, sql.ErrNoRows) {
		return ErrSkillNotFound
	}
	if qerr != nil {
		return qerr
	}
	return ErrSkillBuiltinProtected
}

// SlugExists 判断本班是否已存在同名技能包标识（slug 同班唯一）。
func (r *TutorRepository) SlugExists(ctx context.Context, classID int64, slug string) (bool, error) {
	var one int
	err := r.DB.QueryRowContext(ctx,
		`SELECT 1 FROM skills s
		 JOIN assistants a ON a.id = s.assistant_id
		 WHERE a.class_id = ? AND s.slug = ?
		 LIMIT 1`, classID, slug).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// CreateSkillPackage 在本班助手下写入技能包技能（默认停用），frontmatter 与 slug 一并入库。
// 同班 slug 冲突映射为 ErrSkillPackageConflict，本班助手不存在映射为 ErrAssistantNotFound。
func (r *TutorRepository) CreateSkillPackage(ctx context.Context, classID int64, pkg *SkillPackage, slug string) (*Skill, error) {
	res, err := r.DB.ExecContext(ctx,
		`INSERT INTO skills (assistant_id, name, description, content, source, enabled, slug, frontmatter)
		 SELECT a.id, ?, ?, ?, ?, 0, ?, ? FROM assistants a WHERE a.class_id = ?`,
		pkg.Name, pkg.Description, pkg.Body, SkillSourcePackage, slug,
		FrontmatterJSON(pkg.Frontmatter), classID)
	if err != nil {
		if isDuplicateKey(err) {
			return nil, ErrSkillPackageConflict
		}
		return nil, err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return nil, ErrAssistantNotFound
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	sk := &Skill{
		ID:          id,
		Name:        pkg.Name,
		Description: pkg.Description,
		Content:     pkg.Body,
		Source:      SkillSourcePackage,
		Slug:        slug,
		Resources:   pkg.Resources,
		Frontmatter: pkg.Frontmatter,
		Conditions:  pkg.Conditions,
		ToolRefs:    pkg.ToolRefs,
	}
	return sk, nil
}

// SetSkillPackagePath 回填技能包目录与内容哈希；技能必须属于本班助手。
func (r *TutorRepository) SetSkillPackagePath(ctx context.Context, classID, skillID int64, dirPath, contentHash string) error {
	res, err := r.DB.ExecContext(ctx,
		`UPDATE skills s
		 JOIN assistants a ON a.id = s.assistant_id
		 SET s.dir_path = ?, s.content_hash = ?
		 WHERE s.id = ? AND a.class_id = ?`,
		dirPath, contentHash, skillID, classID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		if err := r.requireSkill(ctx, classID, skillID); err != nil {
			return err
		}
	}
	return nil
}

// GetSkillPackage 按 id 取本班技能（含包目录、声明资源与工具引用）；跨班或不存在按不存在处理。
func (r *TutorRepository) GetSkillPackage(ctx context.Context, classID, skillID int64) (*Skill, error) {
	var sk Skill
	var desc, content, slug, dirPath sql.NullString
	var rawFM []byte
	err := r.DB.QueryRowContext(ctx,
		`SELECT s.id, s.name, s.description, s.content, s.source, s.builtin, s.enabled,
		        s.slug, s.frontmatter, s.dir_path
		 FROM skills s
		 JOIN assistants a ON a.id = s.assistant_id
		 WHERE s.id = ? AND a.class_id = ?`, skillID, classID).
		Scan(&sk.ID, &sk.Name, &desc, &content, &sk.Source, &sk.Builtin, &sk.Enabled,
			&slug, &rawFM, &dirPath)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSkillNotFound
	}
	if err != nil {
		return nil, err
	}
	sk.Description = desc.String
	sk.Content = content.String
	applySkillMeta(&sk, slug, dirPath, rawFM)
	return &sk, nil
}

// requireSkill 校验技能属于本班助手；跨班或不存在一律按不存在处理。
func (r *TutorRepository) requireSkill(ctx context.Context, classID, skillID int64) error {
	var one int
	err := r.DB.QueryRowContext(ctx,
		`SELECT 1 FROM skills s
		 JOIN assistants a ON a.id = s.assistant_id
		 WHERE s.id = ? AND a.class_id = ?`, skillID, classID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSkillNotFound
	}
	return err
}

// ---------- 智能体（agents）与技能/材料绑定 ----------

// requireAgent 校验智能体属于本班；跨班或不存在一律按不存在处理。
func (r *TutorRepository) requireAgent(ctx context.Context, classID, agentID int64) error {
	var one int
	err := r.DB.QueryRowContext(ctx,
		`SELECT 1 FROM agents WHERE id = ? AND class_id = ?`, agentID, classID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAgentNotFound
	}
	return err
}

// GetAgent 按 id 取本班智能体；班级只信参数（来自 JWT claims）。
func (r *TutorRepository) GetAgent(ctx context.Context, classID, id int64) (*Agent, error) {
	var a Agent
	var prompt sql.NullString
	err := r.DB.QueryRowContext(ctx,
		`SELECT id, class_id, name, system_prompt, created_at, updated_at
		 FROM agents WHERE id = ? AND class_id = ?`, id, classID).
		Scan(&a.ID, &a.ClassID, &a.Name, &prompt, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAgentNotFound
	}
	if err != nil {
		return nil, err
	}
	a.SystemPrompt = prompt.String
	return &a, nil
}

// ListAgents 返回本班全部智能体（按 id 升序）；学生默认选中由前端按 updated_at 取最新。
func (r *TutorRepository) ListAgents(ctx context.Context, classID int64) ([]Agent, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT id, class_id, name, system_prompt, created_at, updated_at
		 FROM agents WHERE class_id = ? ORDER BY id`, classID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Agent{}
	for rows.Next() {
		var a Agent
		var prompt sql.NullString
		if err := rows.Scan(&a.ID, &a.ClassID, &a.Name, &prompt, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		a.SystemPrompt = prompt.String
		out = append(out, a)
	}
	return out, rows.Err()
}

// CreateAgent 在本班新建智能体；同班重名映射为哨兵错误。
func (r *TutorRepository) CreateAgent(ctx context.Context, classID int64, name, systemPrompt string) (*Agent, error) {
	res, err := r.DB.ExecContext(ctx,
		`INSERT INTO agents (class_id, name, system_prompt) VALUES (?, ?, ?)`,
		classID, name, nullString(systemPrompt))
	if err != nil {
		if isDuplicateKey(err) {
			return nil, ErrAgentNameConflict
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return r.GetAgent(ctx, classID, id)
}

// UpdateAgent 修改本班智能体名称与提示词；同班重名映射为哨兵错误，跨班或不存在按不存在处理。
func (r *TutorRepository) UpdateAgent(ctx context.Context, classID, id int64, name, systemPrompt string) (*Agent, error) {
	res, err := r.DB.ExecContext(ctx,
		`UPDATE agents SET name = ?, system_prompt = ? WHERE id = ? AND class_id = ?`,
		name, nullString(systemPrompt), id, classID)
	if err != nil {
		if isDuplicateKey(err) {
			return nil, ErrAgentNameConflict
		}
		return nil, err
	}
	// 名称与提示词均未变化时 RowsAffected=0，用存在性查询区分是否真的不存在。
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		if err := r.requireAgent(ctx, classID, id); err != nil {
			return nil, err
		}
	}
	return r.GetAgent(ctx, classID, id)
}

// DeleteAgent 删除本班智能体；技能/材料绑定随外键级联解除。
// 仍有会话引用该智能体时外键（ON DELETE RESTRICT）拒绝删除，映射为哨兵错误。
func (r *TutorRepository) DeleteAgent(ctx context.Context, classID, id int64) error {
	res, err := r.DB.ExecContext(ctx,
		`DELETE FROM agents WHERE id = ? AND class_id = ?`, id, classID)
	if err != nil {
		if isForeignKeyErr(err) {
			return ErrAgentInUse
		}
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrAgentNotFound
	}
	return nil
}

// AgentSkillIDs 返回智能体当前绑定的技能 id（升序），用于教师视图回显。
func (r *TutorRepository) AgentSkillIDs(ctx context.Context, agentID int64) ([]int64, error) {
	return r.idsByQuery(ctx,
		`SELECT skill_id FROM agent_skills WHERE agent_id = ? ORDER BY skill_id`, agentID)
}

// AgentHandoutIDs 返回智能体当前绑定的讲义 id（升序），用于教师视图回显与检索白名单。
func (r *TutorRepository) AgentHandoutIDs(ctx context.Context, agentID int64) ([]int64, error) {
	return r.idsByQuery(ctx,
		`SELECT handout_id FROM agent_materials WHERE agent_id = ? ORDER BY handout_id`, agentID)
}

// idsByQuery 执行单列 id 查询并返回切片（空结果返回空切片，非 nil）。
func (r *TutorRepository) idsByQuery(ctx context.Context, query string, agentID int64) ([]int64, error) {
	rows, err := r.DB.QueryContext(ctx, query, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ReplaceAgentSkills 全量替换智能体的技能绑定（幂等）；智能体须属本班，
// 任一技能不存在、跨班或不属本班技能库时返回 ErrSkillNotFound 且不改变绑定。
func (r *TutorRepository) ReplaceAgentSkills(ctx context.Context, classID, agentID int64, skillIDs []int64) error {
	if err := r.requireAgent(ctx, classID, agentID); err != nil {
		return err
	}
	ids := dedupeIDs(skillIDs)
	if len(ids) > 0 {
		args := make([]any, 0, len(ids)+1)
		args = append(args, classID)
		for _, id := range ids {
			args = append(args, id)
		}
		var n int64
		if err := r.DB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM skills s JOIN assistants a ON a.id = s.assistant_id
			 WHERE a.class_id = ? AND s.id IN (`+placeholders(len(ids))+`)`, args...).Scan(&n); err != nil {
			return err
		}
		if n != int64(len(ids)) {
			return ErrSkillNotFound
		}
	}
	if _, err := r.DB.ExecContext(ctx,
		`DELETE FROM agent_skills WHERE agent_id = ?`, agentID); err != nil {
		return err
	}
	return r.insertBindings(ctx, `INSERT INTO agent_skills (agent_id, skill_id) VALUES `, agentID, ids)
}

// ReplaceAgentMaterials 全量替换智能体的材料绑定（幂等）；智能体须属本班，
// 任一讲义不属于本班时返回 ErrHandoutNotFound 且不改变绑定。
func (r *TutorRepository) ReplaceAgentMaterials(ctx context.Context, classID, agentID int64, handoutIDs []int64) error {
	if err := r.requireAgent(ctx, classID, agentID); err != nil {
		return err
	}
	ids := dedupeIDs(handoutIDs)
	if len(ids) > 0 {
		args := make([]any, 0, len(ids)+1)
		args = append(args, classID)
		for _, id := range ids {
			args = append(args, id)
		}
		var n int64
		if err := r.DB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM handouts
			 WHERE class_id = ? AND id IN (`+placeholders(len(ids))+`)`, args...).Scan(&n); err != nil {
			return err
		}
		if n != int64(len(ids)) {
			return ErrHandoutNotFound
		}
	}
	if _, err := r.DB.ExecContext(ctx,
		`DELETE FROM agent_materials WHERE agent_id = ?`, agentID); err != nil {
		return err
	}
	return r.insertBindings(ctx, `INSERT INTO agent_materials (agent_id, handout_id) VALUES `, agentID, ids)
}

// insertBindings 把 (agent_id, 绑定id) 成对写入绑定表；空集合不写入。
func (r *TutorRepository) insertBindings(ctx context.Context, insertSQL string, agentID int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteString(insertSQL)
	args := make([]any, 0, len(ids)*2)
	for i, id := range ids {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("(?, ?)")
		args = append(args, agentID, id)
	}
	_, err := r.DB.ExecContext(ctx, sb.String(), args...)
	return err
}

// AgentConfig 读取一次提问所需的智能体配置：提示词、绑定且已启用的技能（skills.id 升序）
// 与绑定的 handout_id 集合。班级只信参数（来自 JWT claims）。
func (r *TutorRepository) AgentConfig(ctx context.Context, classID, agentID int64) (*AgentConfig, error) {
	cfg := &AgentConfig{AgentID: agentID, ClassID: classID, Skills: []Skill{}, HandoutIDs: []int64{}}
	var prompt sql.NullString
	err := r.DB.QueryRowContext(ctx,
		`SELECT name, system_prompt FROM agents WHERE id = ? AND class_id = ?`, agentID, classID).
		Scan(&cfg.Name, &prompt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAgentNotFound
	}
	if err != nil {
		return nil, err
	}
	cfg.SystemPrompt = prompt.String

	rows, err := r.DB.QueryContext(ctx,
		`SELECT s.id, s.name, s.description, s.content, s.source, s.builtin, s.enabled,
		        s.slug, s.frontmatter, s.dir_path
		 FROM agent_skills ags
		 JOIN skills s ON s.id = ags.skill_id
		 WHERE ags.agent_id = ? AND s.enabled = 1
		 ORDER BY s.id`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sk Skill
		var desc, content, slug, dirPath sql.NullString
		var rawFM []byte
		if err := rows.Scan(&sk.ID, &sk.Name, &desc, &content,
			&sk.Source, &sk.Builtin, &sk.Enabled, &slug, &rawFM, &dirPath); err != nil {
			return nil, err
		}
		sk.Description = desc.String
		sk.Content = content.String
		applySkillMeta(&sk, slug, dirPath, rawFM)
		cfg.Skills = append(cfg.Skills, sk)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if cfg.HandoutIDs, err = r.AgentHandoutIDs(ctx, agentID); err != nil {
		return nil, err
	}
	return cfg, nil
}

// dedupeIDs 对 id 切片按首次出现顺序去重（绑定表 PK 天然去重，这里避免无谓的多行写入）。
func dedupeIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// placeholders 生成 n 个逗号分隔的 ? 占位符。
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// CreateConversation 创建会话；class_id/user_id/agent_id 均来自 JWT claims 与会话选择的智能体，不接受客户端直接传入班级。
func (r *TutorRepository) CreateConversation(ctx context.Context, classID, userID, agentID int64, title string) (int64, error) {
	res, err := r.DB.ExecContext(ctx,
		`INSERT INTO conversations (class_id, user_id, agent_id, title) VALUES (?, ?, ?, ?)`,
		classID, userID, agentID, title)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ConversationsByUser 返回本人全部会话（新→旧）。
func (r *TutorRepository) ConversationsByUser(ctx context.Context, userID int64) ([]Conversation, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT id, class_id, user_id, agent_id, title, created_at
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
		if err := rows.Scan(&c.ID, &c.ClassID, &c.UserID, &c.AgentID, &c.Title, &c.CreatedAt); err != nil {
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
		`SELECT id, class_id, user_id, agent_id, title, created_at
		 FROM conversations
		 WHERE id = ? AND user_id = ?`,
		convID, userID)
	var c Conversation
	err := row.Scan(&c.ID, &c.ClassID, &c.UserID, &c.AgentID, &c.Title, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrConversationNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// AppendMessage 写入一轮消息；assistant 轮同时持久化引用、提示词快照与启用技能快照。
// 旧 skill_enabled 列不再写入（保留在库中仅为历史兼容）。
func (r *TutorRepository) AppendMessage(ctx context.Context, convID int64, m NewMessage) (int64, error) {
	var citations, skillsUsed any // user 轮保持 NULL
	if m.Role == RoleAssistant && m.Citations != nil {
		raw, err := json.Marshal(m.Citations)
		if err != nil {
			return 0, fmt.Errorf("序列化引用失败: %w", err)
		}
		citations = string(raw)
	}
	if m.Role == RoleAssistant && m.SkillsUsed != nil {
		raw, err := json.Marshal(m.SkillsUsed)
		if err != nil {
			return 0, fmt.Errorf("序列化技能快照失败: %w", err)
		}
		skillsUsed = string(raw)
	}
	res, err := r.DB.ExecContext(ctx,
		`INSERT INTO messages (conversation_id, role, content, citations, prompt_used, skills_used)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		convID, m.Role, m.Content, citations, m.PromptUsed, skillsUsed)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// MessagesByConversation 按时间升序读取会话全部消息（含引用与启用技能快照，不含提示词快照）。
func (r *TutorRepository) MessagesByConversation(ctx context.Context, convID int64) ([]Message, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT id, conversation_id, role, content, citations, skills_used, created_at
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
		var rawCitations, rawSkills []byte
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content,
			&rawCitations, &rawSkills, &m.CreatedAt); err != nil {
			return nil, err
		}
		if len(rawCitations) > 0 {
			cs, err := unmarshalCitations(rawCitations)
			if err != nil {
				return nil, err
			}
			m.Citations = cs
		}
		if len(rawSkills) > 0 {
			var sks []SkillRef
			if err := json.Unmarshal(rawSkills, &sks); err != nil {
				return nil, fmt.Errorf("解析技能快照 JSON 失败: %w", err)
			}
			m.SkillsUsed = sks
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

// ---------- MCP 服务器与调用审计 ----------

// MCP 调用状态取值（与 mcp_calls.status ENUM 一致）。
const (
	MCPStatusSuccess = "success"
	MCPStatusDenied  = "denied"
	MCPStatusError   = "error"
)

// MCPToolCacheLimit 是缓存工具清单的最大条目数（超出截断，避免缓存无限膨胀）。
const MCPToolCacheLimit = 50

// MCPServer 是本班登记的一台远端 MCP 服务器。
// AuthToken 只服务端可见（json:"-"），接口响应只以 HasAuth 布尔表示是否已配置令牌。
type MCPServer struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	BaseURL   string     `json:"baseUrl"`
	HasAuth   bool       `json:"hasAuth"`
	Connected bool       `json:"connected"`
	Tools     []mcp.Tool `json:"tools"`
	LastError string     `json:"lastError,omitempty"`

	AuthToken string `json:"-"`
}

// MCPCall 是一次远端工具调用的审计记录；server_id 可为 NULL，server_name 为写入时的快照。
type MCPCall struct {
	ID             int64     `json:"id"`
	ServerID       *int64    `json:"serverId,omitempty"`
	ServerName     string    `json:"serverName"`
	ToolRef        string    `json:"toolRef"`
	Status         string    `json:"status"`
	Error          string    `json:"error,omitempty"`
	DurationMS     int       `json:"durationMs"`
	UserID         *int64    `json:"userId,omitempty"`
	Username       string    `json:"username,omitempty"`
	ConversationID *int64    `json:"conversationId,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

// MCPCallInput 是待写入的审计记录（0 值表示对应列为 NULL）。
type MCPCallInput struct {
	ClassID        int64
	UserID         int64
	ServerID       int64
	ServerName     string
	ConversationID int64
	ToolRef        string
	Status         string
	Error          string
	DurationMS     int
}

// rowScanner 抽象 *sql.Row 与 *sql.Rows 的 Scan。
type rowScanner interface{ Scan(dest ...any) error }

// ListMCPServers 返回本班全部 MCP 服务器（按 id 升序）。
func (r *TutorRepository) ListMCPServers(ctx context.Context, classID int64) ([]MCPServer, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT id, name, base_url, auth_token, connected, tool_cache, last_error
		 FROM mcp_servers
		 WHERE class_id = ?
		 ORDER BY id`,
		classID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []MCPServer{}
	for rows.Next() {
		s, err := scanMCPServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// GetMCPServer 按 id 取本班服务器；他人班级与不存在一律按不存在处理。
func (r *TutorRepository) GetMCPServer(ctx context.Context, classID, id int64) (*MCPServer, error) {
	row := r.DB.QueryRowContext(ctx,
		`SELECT id, name, base_url, auth_token, connected, tool_cache, last_error
		 FROM mcp_servers
		 WHERE id = ? AND class_id = ?`,
		id, classID)
	s, err := scanMCPServer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMCPServerNotFound
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// GetMCPServerByName 按名称取本班服务器（技能引用校验与编排调用用）。
func (r *TutorRepository) GetMCPServerByName(ctx context.Context, classID int64, name string) (*MCPServer, error) {
	row := r.DB.QueryRowContext(ctx,
		`SELECT id, name, base_url, auth_token, connected, tool_cache, last_error
		 FROM mcp_servers
		 WHERE name = ? AND class_id = ?`,
		name, classID)
	s, err := scanMCPServer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMCPServerNotFound
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// CreateMCPServer 在本班登记服务器；班级取自 JWT claims；同班重名映射为错误哨兵。
func (r *TutorRepository) CreateMCPServer(ctx context.Context, classID int64, name, baseURL, token string) (*MCPServer, error) {
	res, err := r.DB.ExecContext(ctx,
		`INSERT INTO mcp_servers (class_id, name, base_url, auth_token) VALUES (?, ?, ?, ?)`,
		classID, name, baseURL, nullString(token))
	if err != nil {
		if isDuplicateKey(err) {
			return nil, ErrMCPServerNameConflict
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &MCPServer{
		ID:        id,
		Name:      name,
		BaseURL:   baseURL,
		HasAuth:   token != "",
		AuthToken: token,
		Tools:     []mcp.Tool{},
	}, nil
}

// UpdateMCPServer 修改本班服务器。token 为 nil 表示保留原令牌；非 nil 时以给定值覆盖（空串清除）。
// 地址或令牌变更后缓存清单可能失效，因此统一重置为未连接并清空清单。
func (r *TutorRepository) UpdateMCPServer(ctx context.Context, classID, id int64, name, baseURL string, token *string) (*MCPServer, error) {
	var err error
	if token != nil {
		_, err = r.DB.ExecContext(ctx,
			`UPDATE mcp_servers
			 SET name = ?, base_url = ?, auth_token = ?, connected = 0, tool_cache = NULL, last_error = NULL
			 WHERE id = ? AND class_id = ?`,
			name, baseURL, nullString(*token), id, classID)
	} else {
		_, err = r.DB.ExecContext(ctx,
			`UPDATE mcp_servers
			 SET name = ?, base_url = ?, connected = 0, tool_cache = NULL, last_error = NULL
			 WHERE id = ? AND class_id = ?`,
			name, baseURL, id, classID)
	}
	if err != nil {
		if isDuplicateKey(err) {
			return nil, ErrMCPServerNameConflict
		}
		return nil, err
	}
	return r.GetMCPServer(ctx, classID, id)
}

// DeleteMCPServer 删除本班服务器；不存在（含他人班级）返回错误哨兵。
func (r *TutorRepository) DeleteMCPServer(ctx context.Context, classID, id int64) error {
	res, err := r.DB.ExecContext(ctx,
		`DELETE FROM mcp_servers WHERE id = ? AND class_id = ?`, id, classID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrMCPServerNotFound
	}
	return nil
}

// MarkMCPServerConnected 记录连接成功：置为已连接并缓存工具清单、清除上次错误。
func (r *TutorRepository) MarkMCPServerConnected(ctx context.Context, classID, id int64, tools []mcp.Tool) error {
	if len(tools) > MCPToolCacheLimit {
		tools = tools[:MCPToolCacheLimit]
	}
	if tools == nil {
		tools = []mcp.Tool{}
	}
	raw, err := json.Marshal(tools)
	if err != nil {
		return fmt.Errorf("序列化工具清单失败: %w", err)
	}
	return r.updateMCPConnection(ctx, `connected = 1, tool_cache = ?, last_error = NULL`, string(raw), classID, id)
}

// MarkMCPServerConnectFailed 记录连接失败：保持未连接并清空清单，写入可读错误摘要。
func (r *TutorRepository) MarkMCPServerConnectFailed(ctx context.Context, classID, id int64, lastError string) error {
	return r.updateMCPConnection(ctx,
		`connected = 0, tool_cache = NULL, last_error = ?`, truncateColumn(lastError), classID, id)
}

// DisconnectMCPServer 断开：置为未连接并清空工具清单。
func (r *TutorRepository) DisconnectMCPServer(ctx context.Context, classID, id int64) error {
	return r.updateMCPConnection(ctx,
		`connected = 0, tool_cache = NULL, last_error = NULL`, nil, classID, id)
}

// updateMCPConnection 执行连接状态更新；影响行数为 0 时区分「值未变化」与「行不存在」。
func (r *TutorRepository) updateMCPConnection(ctx context.Context, setClause string, value any, classID, id int64) error {
	query := `UPDATE mcp_servers SET ` + setClause + ` WHERE id = ? AND class_id = ?`
	var res sql.Result
	var err error
	if value == nil {
		res, err = r.DB.ExecContext(ctx, query, id, classID)
	} else {
		res, err = r.DB.ExecContext(ctx, query, value, id, classID)
	}
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		var one int
		if qerr := r.DB.QueryRowContext(ctx,
			`SELECT 1 FROM mcp_servers WHERE id = ? AND class_id = ?`, id, classID).Scan(&one); qerr != nil {
			if errors.Is(qerr, sql.ErrNoRows) {
				return ErrMCPServerNotFound
			}
			return qerr
		}
	}
	return nil
}

// RecordMCPCall 写入一条调用审计；已删除服务器的场景不调用本方法（避免产生无归属审计）。
func (r *TutorRepository) RecordMCPCall(ctx context.Context, in MCPCallInput) error {
	_, err := r.DB.ExecContext(ctx,
		`INSERT INTO mcp_calls
		   (class_id, user_id, server_id, server_name, conversation_id, tool_ref, status, error, duration_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ClassID, nullID(in.UserID), nullID(in.ServerID), in.ServerName,
		nullID(in.ConversationID), in.ToolRef, in.Status, nullString(in.Error), in.DurationMS)
	return err
}

// RecentMCPCalls 返回本班最近调用记录（新→旧）；班级只信参数，跨班不返回他班记录。
func (r *TutorRepository) RecentMCPCalls(ctx context.Context, classID int64, limit int) ([]MCPCall, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.DB.QueryContext(ctx,
		`SELECT c.id, c.server_id, c.server_name, c.tool_ref, c.status, c.error, c.duration_ms,
		        c.user_id, u.display_name, c.conversation_id, c.created_at
		 FROM mcp_calls c
		 LEFT JOIN users u ON u.id = c.user_id
		 WHERE c.class_id = ?
		 ORDER BY c.id DESC
		 LIMIT ?`,
		classID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []MCPCall{}
	for rows.Next() {
		var c MCPCall
		var serverID, userID, convID sql.NullInt64
		var errText, username sql.NullString
		if err := rows.Scan(&c.ID, &serverID, &c.ServerName, &c.ToolRef, &c.Status, &errText,
			&c.DurationMS, &userID, &username, &convID, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.ServerID = nullInt64Ptr(serverID)
		c.UserID = nullInt64Ptr(userID)
		c.ConversationID = nullInt64Ptr(convID)
		c.Error = errText.String
		c.Username = username.String
		out = append(out, c)
	}
	return out, rows.Err()
}

// scanMCPServer 读取一行 MCP 服务器并还原工具清单 JSON。
func scanMCPServer(sc rowScanner) (*MCPServer, error) {
	var s MCPServer
	var token, lastError sql.NullString
	var cache []byte
	if err := sc.Scan(&s.ID, &s.Name, &s.BaseURL, &token, &s.Connected, &cache, &lastError); err != nil {
		return nil, err
	}
	s.AuthToken = token.String
	s.HasAuth = token.Valid && token.String != ""
	s.LastError = lastError.String
	if len(cache) > 0 {
		var tools []mcp.Tool
		if err := json.Unmarshal(cache, &tools); err != nil {
			return nil, fmt.Errorf("解析工具清单 JSON 失败: %w", err)
		}
		s.Tools = tools
	}
	if s.Tools == nil {
		s.Tools = []mcp.Tool{}
	}
	return &s, nil
}

// isDuplicateKey 判断是否为 MySQL 唯一键冲突（错误码 1062）。
func isDuplicateKey(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}

// isForeignKeyErr 判断是否为 MySQL 外键约束失败（1451：父行仍被子行引用）。
func isForeignKeyErr(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1451
}

// nullString 空串映射为 SQL NULL。
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullID 0 映射为 SQL NULL。
func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// nullInt64Ptr 把 NULL 安全转换为指针。
func nullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

// truncateColumn 按 rune 截断，避免超出 VARCHAR(512) 报错。
func truncateColumn(s string) string {
	const max = 512
	runes := []rune(s)
	if len(runes) > max {
		return string(runes[:max])
	}
	return s
}
