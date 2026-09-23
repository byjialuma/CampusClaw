package store

import (
	"os"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	pdfHead := []byte("%PDF-1.7\n%...")
	cases := []struct {
		name    string
		file    string
		data    []byte
		wantErr bool
		wantExt string
	}{
		{"txt 合法", "讲义.txt", []byte("hello 你好"), false, "txt"},
		{"md 合法", "readme.MD", []byte("# 标题"), false, "md"},
		{"pdf 合法", "slides.pdf", pdfHead, false, "pdf"},
		{"扩展名禁止 docx", "a.docx", []byte("x"), true, ""},
		{"扩展名禁止 exe", "a.exe", []byte("x"), true, ""},
		{"扩展名禁止 png", "a.png", []byte("x"), true, ""},
		{"伪造扩展名的 pdf", "a.pdf", []byte("not a pdf"), true, ""},
		{"txt 内含 NUL", "a.txt", []byte{'a', 0, 'b'}, true, ""},
		{"txt 非法 UTF-8", "a.txt", []byte{0xff, 0xfe, 0xfd}, true, ""},
		{"空文件", "a.txt", []byte{}, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ext, err := Validate(c.file, c.data)
			if c.wantErr {
				if err == nil {
					t.Fatalf("期望错误，实际通过 ext=%s", ext)
				}
				if !strings.Contains(err.Error(), "仅允许 txt、md、pdf") {
					t.Fatalf("错误信息应包含统一中文提示，实际: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("未期望错误: %v", err)
			}
			if ext != c.wantExt {
				t.Fatalf("扩展名=%q，期望 %q", ext, c.wantExt)
			}
		})
	}
}

func TestStorageSaveUsesRandomName(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	rel1, abs1, err := s.Save([]byte("aaa"), "txt")
	if err != nil {
		t.Fatal(err)
	}
	rel2, _, err := s.Save([]byte("bbb"), "txt")
	if err != nil {
		t.Fatal(err)
	}
	if rel1 == rel2 {
		t.Fatal("两次保存的相对路径必须不同")
	}
	if strings.Contains(rel1, "aaa") || strings.Contains(rel1, "..") {
		t.Fatalf("存储路径不得包含原始内容/越权片段: %s", rel1)
	}
	if err := s.Remove(rel1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(abs1); err == nil {
		t.Fatal("删除后文件不应存在")
	}
}
