// Package tool - edit_test.go
package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEdit_Success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("hello world\n"), 0644); err != nil {
		t.Fatal(err)
	}

	e := &Edit{Cwd: dir}
	raw, _ := json.Marshal(EditArgs{
		FilePath:  path,
		OldString: "hello",
		NewString: "hi",
	})

	res, err := e.Execute(context.Background(), raw, Context{Cwd: dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "success" {
		t.Errorf("Status = %q, Output: %s", res.Status, res.Output)
	}

	got, _ := os.ReadFile(path)
	if string(got) != "hi world\n" {
		t.Errorf("file content = %q, 期望 'hi world\\n'", string(got))
	}
}

func TestEdit_ReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("foo bar foo baz foo\n"), 0644); err != nil {
		t.Fatal(err)
	}

	e := &Edit{Cwd: dir}
	raw, _ := json.Marshal(EditArgs{
		FilePath:   path,
		OldString:  "foo",
		NewString:  "FOO",
		ReplaceAll: true,
	})

	res, err := e.Execute(context.Background(), raw, Context{Cwd: dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "success" {
		t.Errorf("Status = %q", res.Status)
	}

	got, _ := os.ReadFile(path)
	if string(got) != "FOO bar FOO baz FOO\n" {
		t.Errorf("file content = %q", string(got))
	}
}

func TestEdit_NotUnique(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("foo bar foo\n"), 0644); err != nil {
		t.Fatal(err)
	}

	e := &Edit{Cwd: dir}
	raw, _ := json.Marshal(EditArgs{
		FilePath:  path,
		OldString: "foo",
		NewString: "FOO",
		// ReplaceAll: false (默认)
	})

	res, _ := e.Execute(context.Background(), raw, Context{Cwd: dir})
	if res.Status != "error" {
		t.Errorf("Status = %q, 期望 error", res.Status)
	}
	if !strings.Contains(res.Output, "匹配 2 处") {
		t.Errorf("Output 应提示 '匹配 2 处', 实际: %q", res.Output)
	}
}

func TestEdit_OldStringNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("hello world\n"), 0644); err != nil {
		t.Fatal(err)
	}

	e := &Edit{Cwd: dir}
	raw, _ := json.Marshal(EditArgs{
		FilePath:  path,
		OldString: "nonexistent",
		NewString: "X",
	})

	res, _ := e.Execute(context.Background(), raw, Context{Cwd: dir})
	if res.Status != "error" {
		t.Errorf("Status = %q, 期望 error", res.Status)
	}
	if !strings.Contains(res.Output, "未找到") {
		t.Errorf("Output 应含 '未找到', 实际: %q", res.Output)
	}
}

func TestEdit_FileNotExist(t *testing.T) {
	dir := t.TempDir()
	e := &Edit{Cwd: dir}
	raw, _ := json.Marshal(EditArgs{
		FilePath:  filepath.Join(dir, "nope.txt"),
		OldString: "x",
		NewString: "y",
	})

	res, _ := e.Execute(context.Background(), raw, Context{Cwd: dir})
	if res.Status != "error" {
		t.Errorf("Status = %q", res.Status)
	}
	if !strings.Contains(res.Output, "不存在") {
		t.Errorf("Output = %q", res.Output)
	}
}

func TestEdit_DirectoryNotEditable(t *testing.T) {
	dir := t.TempDir()
	e := &Edit{Cwd: dir}
	raw, _ := json.Marshal(EditArgs{
		FilePath:  dir, // 是目录
		OldString: "x",
		NewString: "y",
	})

	res, _ := e.Execute(context.Background(), raw, Context{Cwd: dir})
	if res.Status != "error" {
		t.Errorf("Status = %q", res.Status)
	}
	if !strings.Contains(res.Output, "目录") {
		t.Errorf("Output = %q", res.Output)
	}
}

func TestEdit_EmptyArgs(t *testing.T) {
	e := &Edit{}
	tests := []struct {
		name string
		args EditArgs
	}{
		{"no filepath", EditArgs{OldString: "x", NewString: "y"}},
		{"no oldString", EditArgs{FilePath: "x", NewString: "y"}},
		{"no newString", EditArgs{FilePath: "x", OldString: "y"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, _ := json.Marshal(tt.args)
			res, _ := e.Execute(context.Background(), raw, Context{Cwd: t.TempDir()})
			if res.Status != "error" {
				t.Errorf("Status = %q, 期望 error", res.Status)
			}
		})
	}
}

func TestEdit_InvalidJSON(t *testing.T) {
	e := &Edit{}
	res, _ := e.Execute(context.Background(), json.RawMessage(`{bad`), Context{})
	if res.Status != "error" {
		t.Errorf("Status = %q", res.Status)
	}
}

func TestEdit_PermissionDenied(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	e := &Edit{Cwd: dir}
	raw, _ := json.Marshal(EditArgs{
		FilePath:  path,
		OldString: "hello",
		NewString: "hi",
	})

	res, _ := e.Execute(context.Background(), raw, Context{
		Cwd: dir,
		Ask: denyAllBroker{}.Ask,
	})
	if res.Status != "error" {
		t.Errorf("Status = %q, 期望 error", res.Status)
	}
	if !strings.Contains(res.Output, "permission denied") {
		t.Errorf("Output = %q", res.Output)
	}

	// 验证文件没改
	got, _ := os.ReadFile(path)
	if string(got) != "hello\n" {
		t.Errorf("文件被改: %q", string(got))
	}
}

func TestEdit_RelativePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("aaa bbb\n"), 0644); err != nil {
		t.Fatal(err)
	}

	e := &Edit{}
	raw, _ := json.Marshal(EditArgs{
		FilePath:  "f.txt", // 相对路径
		OldString: "aaa",
		NewString: "xxx",
	})

	res, _ := e.Execute(context.Background(), raw, Context{Cwd: dir})
	if res.Status != "success" {
		t.Errorf("Status = %q, Output: %s", res.Status, res.Output)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if string(got) != "xxx bbb\n" {
		t.Errorf("file = %q", string(got))
	}
}

func TestEdit_PreservesMode(t *testing.T) {
	// Windows 不支持 Unix 文件 mode 语义
	if runtime.GOOS == "windows" {
		t.Skip("Windows 文件系统不保留 Unix mode（v0.1 限制）")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "exec.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho hi\n"), 0755); err != nil {
		t.Fatal(err)
	}

	e := &Edit{Cwd: dir}
	raw, _ := json.Marshal(EditArgs{
		FilePath:  path,
		OldString: "hi",
		NewString: "hello",
	})

	res, _ := e.Execute(context.Background(), raw, Context{Cwd: dir})
	if res.Status != "success" {
		t.Fatalf("Status = %q, Output: %s", res.Status, res.Output)
	}

	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0755 {
		t.Errorf("mode = %o, 期望 0755", info.Mode().Perm())
	}
}

func TestEdit_Definition(t *testing.T) {
	e := &Edit{}
	def := e.Definition()
	if def.ID != "edit" {
		t.Errorf("ID = %q", def.ID)
	}
	if def.JSONSchema == nil {
		t.Error("JSONSchema 不能为 nil")
	}
}

func TestEdit_ImplementsTool(t *testing.T) {
	var _ Tool = (*Edit)(nil)
}
