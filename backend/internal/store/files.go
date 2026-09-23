// Package store 负责材料文件的校验、受控存储，以及讲义/票据的数据访问。
package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxUploadSize 限制单次上传 32MB。
const MaxUploadSize = 32 << 20

// 允许的材料类型。
const (
	TypeTXT = "txt"
	TypeMD  = "md"
	TypePDF = "pdf"
)

// ErrInvalidFile 表示文件未通过类型/内容校验。
var ErrInvalidFile = errors.New("仅允许 txt、md、pdf 文件")

// allowedTypes 是扩展名白名单（小写、不含点）。
var allowedTypes = map[string]bool{TypeTXT: true, TypeMD: true, TypePDF: true}

// ContentType 返回某类材料在 HTTP 响应中使用的 Content-Type。
func ContentType(fileType string) string {
	switch fileType {
	case TypeMD:
		return "text/markdown; charset=utf-8"
	case TypePDF:
		return "application/pdf"
	default:
		return "text/plain; charset=utf-8"
	}
}

// Validate 校验扩展名与文件内容的一致性：
// pdf 必须以 %PDF- 魔数开头；txt/md 必须是不含 NUL 的合法 UTF-8 文本。
// 成功返回规范化后的小写扩展名。
func Validate(originalName string, data []byte) (string, error) {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(originalName), "."))
	if !allowedTypes[ext] {
		return "", ErrInvalidFile
	}
	if len(data) == 0 {
		return "", fmt.Errorf("%w（文件为空）", ErrInvalidFile)
	}
	switch ext {
	case TypePDF:
		if !hasPDFMagic(data) {
			return "", fmt.Errorf("%w（文件头不是合法 PDF）", ErrInvalidFile)
		}
	case TypeTXT, TypeMD:
		if !utf8.Valid(data) || bytesContainsNUL(data) {
			return "", fmt.Errorf("%w（不是合法的文本文件）", ErrInvalidFile)
		}
	}
	return ext, nil
}

func hasPDFMagic(data []byte) bool {
	const magic = "%PDF-"
	if len(data) < len(magic) {
		return false
	}
	return string(data[:len(magic)]) == magic
}

func bytesContainsNUL(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}

// Storage 把文件写入受控目录，原始文件名不参与磁盘路径。
type Storage struct {
	// Root 是材料根目录，如 /app/data/materials。
	Root string
}

// NewStorage 创建存储并确保根目录存在。
func NewStorage(root string) (*Storage, error) {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, err
	}
	return &Storage{Root: root}, nil
}

// Save 生成随机存储键 <yyyymm>/<32字节hex>.<ext> 并写入文件，
// 返回相对 Root 的路径（入库用）与绝对路径。
func (s *Storage) Save(data []byte, ext string) (relPath string, absPath string, err error) {
	month := time.Now().Format("200601")
	dir := filepath.Join(s.Root, month)
	if err = os.MkdirAll(dir, 0o750); err != nil {
		return "", "", err
	}

	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", err
	}
	name := hex.EncodeToString(buf) + "." + ext
	absPath = filepath.Join(dir, name)
	relPath = filepath.ToSlash(filepath.Join(month, name))

	tmp := absPath + ".tmp"
	if err = os.WriteFile(tmp, data, 0o640); err != nil {
		return "", "", err
	}
	if err = os.Rename(tmp, absPath); err != nil {
		_ = os.Remove(tmp)
		return "", "", err
	}
	return relPath, absPath, nil
}

// AbsPath 把入库的相对路径还原为绝对路径。
func (s *Storage) AbsPath(relPath string) string {
	return filepath.Join(s.Root, filepath.FromSlash(relPath))
}

// Remove 按相对路径删除文件（种子/回滚使用），不存在不报错。
func (s *Storage) Remove(relPath string) error {
	err := os.Remove(s.AbsPath(relPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
