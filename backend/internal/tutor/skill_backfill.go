package tutor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// BackfillSkillPackages 把尚无技能包目录的存量技能转换为标准技能包：
// 合成 SKILL.md 落盘并回填 slug/frontmatter/dir_path/content_hash。
// 逐条容错，单条失败只记录日志并继续，该技能继续按纯文本工作；重复启动幂等。
func BackfillSkillPackages(ctx context.Context, db *sql.DB, storage *SkillStorage) error {
	rows, err := db.QueryContext(ctx,
		`SELECT s.id, a.class_id, s.name, s.description, s.content,
		        s.dir_path, s.slug, s.frontmatter
		 FROM skills s
		 JOIN assistants a ON a.id = s.assistant_id
		 ORDER BY s.id`)
	if err != nil {
		return fmt.Errorf("查询待回填技能失败: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id, classID                int64
			name, content, description sql.NullString
			dirPath, slug              sql.NullString
			frontmatter                []byte
		)
		if err := rows.Scan(&id, &classID, &name, &description, &content,
			&dirPath, &slug, &frontmatter); err != nil {
			return fmt.Errorf("读取技能行失败: %w", err)
		}
		if dirPath.Valid && manifestExists(storage, dirPath.String) {
			continue
		}
		if err := backfillOne(ctx, db, storage, id, classID, name.String,
			description.String, content.String, dirPath.String, slug.String, frontmatter); err != nil {
			log.Printf("技能 %d 转换为技能包失败（继续按纯文本工作）: %v", id, err)
		}
	}
	return rows.Err()
}

// backfillOne 处理单个技能的包生成与字段回填。
func backfillOne(ctx context.Context, db *sql.DB, storage *SkillStorage,
	id, classID int64, name, description, content, dirPath, slug string, rawFrontmatter []byte) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("技能名称为空")
	}
	desc := deriveDescription(name, description, content)

	fm := map[string]any{}
	if len(rawFrontmatter) > 0 {
		_ = json.Unmarshal(rawFrontmatter, &fm)
	}
	if _, ok := fm[skillFrontmatterNameKey]; !ok {
		fm[skillFrontmatterNameKey] = name
	}
	if _, ok := fm[skillFrontmatterDescKey]; !ok {
		fm[skillFrontmatterDescKey] = desc
	}
	pkg, err := buildSkillPackage(fm, content, nil)
	if err != nil {
		return err
	}
	manifest := MarshalSkillMD(pkg.Name, desc, toStrings(pkg.Frontmatter[skillFrontmatterCondKey]),
		toolRawList(pkg.ToolRefs), toStrings(pkg.Frontmatter[skillFrontmatterResourceKey]), content)

	rel := storage.DirPath(classID, id)
	if _, err := storage.WritePackage(classID, id, map[string][]byte{SkillManifestName: manifest}); err != nil {
		return err
	}

	if slug == "" {
		slug = SkillSlug(name)
	}
	hash := ContentHash(manifest)
	update := func(slugValue string) error {
		_, err := db.ExecContext(ctx,
			`UPDATE skills
			 SET slug = ?, frontmatter = ?, dir_path = ?, content_hash = ?, description = ?
			 WHERE id = ?`,
			slugValue, FrontmatterJSON(pkg.Frontmatter), rel, hash, desc, id)
		return err
	}
	if err := update(slug); err != nil {
		if isDuplicateKey(err) {
			// 同班同名：追加技能 id 保证本班唯一。
			if err2 := update(fmt.Sprintf("%s-%d", slug, id)); err2 != nil {
				return err2
			}
			return nil
		}
		return err
	}
	return nil
}

// manifestExists 判断包目录下的 SKILL.md 是否仍存在。
func manifestExists(storage *SkillStorage, dirPath string) bool {
	abs, err := storage.absDir(dirPath)
	if err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(abs, SkillManifestName))
	return err == nil && !info.IsDir()
}

// deriveDescription 生成技能描述：优先原有描述，其次正文首个有效行（≤120 字），最后兜底。
func deriveDescription(name, description, content string) string {
	desc := strings.TrimSpace(description)
	if desc == "" {
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if _, ok := cutLabel(line, "工具"); ok {
				continue
			}
			if _, ok := cutLabel(line, "适用条件"); ok {
				continue
			}
			line = strings.TrimLeft(line, "#>*- ")
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			desc = truncateRunes(line, 120)
			break
		}
	}
	if desc == "" {
		desc = truncateRunes(name+" 技能", 120)
	}
	return truncateRunes(desc, 255)
}

// toolRawList 提取工具引用的原始文本。
func toolRawList(refs []ToolRef) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.Raw)
	}
	return out
}

// toStrings 把 frontmatter 值归一为字符串切片（无法识别时返回空）。
func toStrings(v any) []string {
	switch val := v.(type) {
	case string:
		if strings.TrimSpace(val) == "" {
			return nil
		}
		return []string{strings.TrimSpace(val)}
	case []string:
		return val
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
