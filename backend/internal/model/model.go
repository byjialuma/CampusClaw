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
