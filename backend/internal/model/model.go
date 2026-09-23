// Package model 定义跨包共享的领域结构。
package model

import "time"

const (
	RoleTeacher = "teacher"
	RoleStudent = "student"
)

// User 是会话解析后得到的当前用户（班级信息一并载入）。
type User struct {
	ID          int64
	Username    string
	DisplayName string
	Role        string
	ClassID     int64
	ClassName   string
}

// IsTeacher 判断用户是否为教师。
func (u *User) IsTeacher() bool { return u.Role == RoleTeacher }

// 索引状态取值。
const (
	IndexPending = "pending"
	IndexReady   = "ready"
	IndexFailed  = "failed"
)

// Material 是讲义（知识库材料）记录。
type Material struct {
	ID             int64     `json:"id"`
	ClassID        int64     `json:"-"`
	UploaderUserID *int64    `json:"-"`
	OriginalName   string    `json:"name"`
	StoredPath     string    `json:"-"`
	FileType       string    `json:"fileType"`
	SizeBytes      int64     `json:"sizeBytes"`
	UploaderName   string    `json:"uploaderName"`
	CreatedAt      time.Time `json:"createdAt"`
	// IndexStatus 是向量索引状态；无 material_index 行时按 pending 处理。
	IndexStatus string `json:"indexStatus"`
}

// 分片来源位置类型。
const (
	LocatorPage    = "page"    // PDF：页码（1 基）
	LocatorHeading = "heading" // Markdown：章节标题路径
	LocatorLines   = "lines"   // TXT：起止行号（1 基）
)

// Locator 描述分片在原文档中的可定位位置，按 Kind 使用不同字段。
type Locator struct {
	Kind      string `json:"kind"`
	Page      int    `json:"page,omitempty"`
	Path      string `json:"path,omitempty"`
	StartLine int    `json:"startLine,omitempty"`
	EndLine   int    `json:"endLine,omitempty"`
}

// Ticket 是一次性下载票据。
type Ticket struct {
	Token     string
	HandoutID int64
	UserID    int64
	ClassID   int64
	ExpiresAt time.Time
	UsedAt    *time.Time
}
