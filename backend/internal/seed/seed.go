// Package seed 在后端启动时幂等写入预置班级、账号、讲义、作业、助手与技能。
package seed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"campusclaw/internal/auth"
	"campusclaw/internal/config"
	"campusclaw/internal/store"
)

const (
	classAName = "A班"
	classBName = "B班"

	assignmentTitle = "第一次作业（种子示例，暂不开放提交）"

	// 每班一个解题助手，教师可在助教设置页改写提示词与开关解题引导。
	assistantName       = "解题助手"
	defaultSystemPrompt = "你是本班解题助教，结合班级材料引导学生自主解题，用中文，简洁准确。"
	tutorSkillName      = "解题引导"
	tutorSkillDesc      = "开启时给出解题思路与步骤引导，引用本班材料的标题与位置，不直接给出最终答案。"
)

// seedMaterial 描述一个预置讲义文件与所属班级。
type seedMaterial struct {
	className string
	fileName  string
}

var seedMaterials = []seedMaterial{
	{classAName, "《A班·数学第一章·集合讲义》.txt"},
	{classAName, "《A班·数学第一章·集合补充阅读》.md"},
	{classBName, "《B班·数学第一章·集合讲义》.pdf"},
}

// Run 执行幂等播种；重复启动不会产生重复数据。
func Run(ctx context.Context, db *sql.DB, storage *store.Storage, cfg *config.Config) error {
	classA, err := ensureClass(ctx, db, classAName)
	if err != nil {
		return err
	}
	classB, err := ensureClass(ctx, db, classBName)
	if err != nil {
		return err
	}

	if _, err := ensureUser(ctx, db, cfg.SeedTeacherA.Username, cfg.SeedTeacherA.Password,
		"教师A", "teacher", classA); err != nil {
		return err
	}
	if _, err := ensureUser(ctx, db, cfg.SeedStudentA1.Username, cfg.SeedStudentA1.Password,
		"学生A1", "student", classA); err != nil {
		return err
	}
	if _, err := ensureUser(ctx, db, cfg.SeedStudentB1.Username, cfg.SeedStudentB1.Password,
		"学生B1", "student", classB); err != nil {
		return err
	}

	mats := store.MaterialRepository{DB: db, Storage: storage}
	classID := map[string]int64{classAName: classA, classBName: classB}
	for _, sm := range seedMaterials {
		if err := ensureMaterial(ctx, db, &mats, storage, cfg.SeedDir, classID[sm.className], sm.fileName); err != nil {
			return err
		}
	}

	for _, c := range []struct {
		id   int64
		name string
	}{{classA, classAName}, {classB, classBName}} {
		if err := ensureAssignment(ctx, db, c.id, c.name); err != nil {
			return err
		}
		aid, err := ensureAssistant(ctx, db, c.id, c.name)
		if err != nil {
			return err
		}
		if err := ensureSkill(ctx, db, aid); err != nil {
			return err
		}
	}

	log.Printf("种子数据检查/写入完成")
	return nil
}

func ensureClass(ctx context.Context, db *sql.DB, name string) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx, `SELECT id FROM classes WHERE name = ?`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		res, insErr := db.ExecContext(ctx, `INSERT INTO classes (name) VALUES (?)`, name)
		if insErr != nil {
			return 0, insErr
		}
		return res.LastInsertId()
	}
	return id, err
}

func ensureUser(ctx context.Context, db *sql.DB, username, password, displayName, role string, classID int64) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE username = ?`, username).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		hash, hashErr := auth.HashPassword(password)
		if hashErr != nil {
			return 0, hashErr
		}
		res, insErr := db.ExecContext(ctx,
			`INSERT INTO users (username, password_hash, display_name, role, class_id)
			 VALUES (?, ?, ?, ?, ?)`,
			username, hash, displayName, role, classID)
		if insErr != nil {
			return 0, insErr
		}
		log.Printf("写入种子用户: %s (%s)", username, role)
		return res.LastInsertId()
	}
	return id, err
}

func ensureMaterial(ctx context.Context, db *sql.DB, mats *store.MaterialRepository,
	storage *store.Storage, seedDir string, classID int64, fileName string) error {
	exists, err := mats.ExistsInClass(ctx, classID, fileName)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	path := filepath.Join(seedDir, filepath.FromSlash(fileName))
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取种子材料 %s 失败: %w", fileName, err)
	}
	fileType, err := store.Validate(fileName, data)
	if err != nil {
		return fmt.Errorf("种子材料 %s 未通过校验: %w", fileName, err)
	}
	relPath, _, err := storage.Save(data, fileType)
	if err != nil {
		return err
	}
	if _, err := mats.CreateWithStoredFile(ctx, classID, nil, fileName, relPath, fileType, int64(len(data))); err != nil {
		_ = storage.Remove(relPath)
		return err
	}
	log.Printf("写入种子讲义: %s", fileName)
	return nil
}

func ensureAssignment(ctx context.Context, db *sql.DB, classID int64, className string) error {
	title := className + "·" + assignmentTitle
	var id int64
	err := db.QueryRowContext(ctx,
		`SELECT id FROM assignments WHERE class_id = ? AND title = ?`,
		classID, title).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = db.ExecContext(ctx,
			`INSERT INTO assignments (class_id, title, description) VALUES (?, ?, ?)`,
			classID, title,
			"这是预置的作业数据结构示例，本课程变更不实现提交与批改。")
	}
	return err
}

func ensureAssistant(ctx context.Context, db *sql.DB, classID int64, className string) (int64, error) {
	name := className + "·" + assistantName
	var id int64
	err := db.QueryRowContext(ctx,
		`SELECT id FROM assistants WHERE class_id = ? AND name = ?`,
		classID, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		res, insErr := db.ExecContext(ctx,
			`INSERT INTO assistants (class_id, name, description, system_prompt) VALUES (?, ?, ?, ?)`,
			classID, name,
			"班级解题助手：结合提示词、解题引导技能与本班知识库回答学生提问。",
			defaultSystemPrompt)
		if insErr != nil {
			return 0, insErr
		}
		return res.LastInsertId()
	}
	return id, err
}

func ensureSkill(ctx context.Context, db *sql.DB, assistantID int64) error {
	var id int64
	err := db.QueryRowContext(ctx,
		`SELECT id FROM skills WHERE assistant_id = ? AND name = ?`,
		assistantID, tutorSkillName).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = db.ExecContext(ctx,
			`INSERT INTO skills (assistant_id, name, description, enabled) VALUES (?, ?, ?, 1)`,
			assistantID, tutorSkillName, tutorSkillDesc)
	}
	return err
}
