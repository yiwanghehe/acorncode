// Package tool - read_test.go
// 参考: docs/acorncode-architect.md §16.4.3
package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// =============================================================================
// 测试辅助
// =============================================================================

// newTestRead 创建 Read 工具实例 + 测试目录
func newTestRead(t *testing.T, maxBytes int) (*Read, string) {
	t.Helper()
	dir := t.TempDir()
	return &Read{MaxBytes: maxBytes, Cwd: dir}, dir
}

// writeFile 在 dir 下写文件，返回绝对路径
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	return path
}

// runExecute 跑 Execute 并返回 Result
func runExecute(t *testing.T, r *Read, tc Context, args ReadArgs) Result {
	t.Helper()
	raw, _ := json.Marshal(args)
	res, err := r.Execute(context.Background(), json.RawMessage(raw), tc)
	if err != nil {
		t.Fatalf("Execute 返 err: %v", err)
	}
	return res
}

// =============================================================================
// 基础功能
// =============================================================================

// TestRead_WholeFile 验证 offset=0, limit=0 读整文件
func TestRead_WholeFile(t *testing.T) {
	r, dir := newTestRead(t, 1000)
	path := writeFile(t, dir, "test.txt", "line1\nline2\nline3\n")

	res := runExecute(t, r, Context{Cwd: dir}, ReadArgs{Path: path})

	if res.Status != "success" {
		t.Fatalf("Status = %q, 期望 success. Output: %s", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, "line1") || !strings.Contains(res.Output, "line3") {
		t.Errorf("Output 缺内容: %q", res.Output)
	}
	if res.IsTruncated {
		t.Error("不应被截断")
	}
	if !strings.Contains(res.Title, "test.txt") {
		t.Errorf("Title = %q, 应含文件名", res.Title)
	}
}

// TestRead_OffsetLimit 验证 offset 和 limit
func TestRead_OffsetLimit(t *testing.T) {
	r, dir := newTestRead(t, 1000)
	path := writeFile(t, dir, "test.txt", "line1\nline2\nline3\nline4\n")

	tests := []struct {
		name   string
		offset int
		limit  int
		want   string
	}{
		{"skip 1", 1, 0, "line2\nline3\nline4"},
		{"skip 1 limit 1", 1, 1, "line2"},
		{"skip 2 limit 1", 2, 1, "line3"},
		{"skip 0 limit 2", 0, 2, "line1\nline2"},
		{"skip 0 limit 0 (whole)", 0, 0, "line1\nline2\nline3\nline4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := runExecute(t, r, Context{Cwd: dir}, ReadArgs{
				Path:   path,
				Offset: tt.offset,
				Limit:  tt.limit,
			})
			if res.Status != "success" {
				t.Fatalf("Status = %q, Output: %s", res.Status, res.Output)
			}
			if res.Output != tt.want {
				t.Errorf("Output = %q, 期望 %q", res.Output, tt.want)
			}
		})
	}
}

// TestRead_Truncated 验证超出 MaxBytes 时被截断
func TestRead_Truncated(t *testing.T) {
	// MaxBytes=50
	r, dir := newTestRead(t, 50)
	// 写 200 字节文件
	big := strings.Repeat("a", 200)
	path := writeFile(t, dir, "big.txt", big)

	res := runExecute(t, r, Context{Cwd: dir}, ReadArgs{Path: path})

	if res.Status != "success" {
		t.Fatalf("Status = %q", res.Status)
	}
	if !res.IsTruncated {
		t.Error("应被截断")
	}
	if len(res.Output) <= 50 {
		t.Errorf("Output 长度 = %d，应 > 50（被截断时）", len(res.Output))
	}
}

// TestRead_NotFound 验证文件不存在
func TestRead_NotFound(t *testing.T) {
	r, dir := newTestRead(t, 1000)
	missing := filepath.Join(dir, "nope.txt")

	res := runExecute(t, r, Context{Cwd: dir}, ReadArgs{Path: missing})

	if res.Status != "error" {
		t.Fatalf("Status = %q, 期望 error", res.Status)
	}
	if !strings.Contains(res.Output, "不存在") {
		t.Errorf("Output = %q, 应含 '不存在'", res.Output)
	}
}

// TestRead_IsDirectory 验证目录报错（不是文件）
func TestRead_IsDirectory(t *testing.T) {
	r, dir := newTestRead(t, 1000)

	res := runExecute(t, r, Context{Cwd: dir}, ReadArgs{Path: dir})

	if res.Status != "error" {
		t.Fatalf("Status = %q, 期望 error", res.Status)
	}
	if !strings.Contains(res.Output, "目录") {
		t.Errorf("Output = %q, 应提示是目录", res.Output)
	}
}

// TestRead_EmptyPath 验证空 path
func TestRead_EmptyPath(t *testing.T) {
	r, dir := newTestRead(t, 1000)

	res := runExecute(t, r, Context{Cwd: dir}, ReadArgs{Path: ""})

	if res.Status != "error" {
		t.Fatalf("Status = %q, 期望 error", res.Status)
	}
	if !strings.Contains(res.Output, "不能为空") {
		t.Errorf("Output = %q", res.Output)
	}
}

// TestRead_OffsetBeyondFile 验证 offset 超出文件行数
func TestRead_OffsetBeyondFile(t *testing.T) {
	r, dir := newTestRead(t, 1000)
	path := writeFile(t, dir, "short.txt", "line1\nline2\n")

	res := runExecute(t, r, Context{Cwd: dir}, ReadArgs{
		Path:   path,
		Offset: 100,
	})

	if res.Status != "error" {
		t.Fatalf("Status = %q, 期望 error. Output: %s", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, "offset") {
		t.Errorf("Output = %q, 应提 offset", res.Output)
	}
}

// =============================================================================
// 路径处理
// =============================================================================

// TestRead_RelativePath 验证相对路径用 tc.Cwd 解析
func TestRead_RelativePath(t *testing.T) {
	r, dir := newTestRead(t, 1000)
	_ = writeFile(t, dir, "sub.txt", "content")

	// 不传 Cwd（应回退到 r.Cwd）
	res := runExecute(t, r, Context{Cwd: ""}, ReadArgs{Path: "sub.txt"})
	if res.Status != "success" {
		t.Errorf("Status = %q, Output: %s", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, "content") {
		t.Errorf("Output = %q", res.Output)
	}
}

// TestRead_RelativePathWithContextCwd 验证 tc.Cwd 优先于 r.Cwd
func TestRead_RelativePathWithContextCwd(t *testing.T) {
	r, dir := newTestRead(t, 1000)
	subdir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	_ = writeFile(t, subdir, "data.txt", "hello")

	// 用 tc.Cwd 指向子目录
	res := runExecute(t, r, Context{Cwd: subdir}, ReadArgs{Path: "data.txt"})
	if res.Status != "success" {
		t.Errorf("Status = %q", res.Status)
	}
	if !strings.Contains(res.Output, "hello") {
		t.Errorf("Output = %q", res.Output)
	}
}

// TestRead_AbsolutePath 验证绝对路径直接用
func TestRead_AbsolutePath(t *testing.T) {
	r, dir := newTestRead(t, 1000)
	path := writeFile(t, dir, "abs.txt", "abs content")

	// 绝对路径不需要 Cwd
	res := runExecute(t, r, Context{Cwd: ""}, ReadArgs{Path: path})
	if res.Status != "success" {
		t.Errorf("Status = %q", res.Status)
	}
	if !strings.Contains(res.Output, "abs content") {
		t.Errorf("Output = %q", res.Output)
	}
}

// TestRead_PathWithDotDot 验证 path 中含 .. 时被 Clean 处理
func TestRead_PathWithDotDot(t *testing.T) {
	r, dir := newTestRead(t, 1000)
	_ = writeFile(t, dir, "target.txt", "found")

	// 构造 path：dir/sub/../target.txt → 应解析到 dir/target.txt
	sub := filepath.Join(dir, "sub")
	_ = os.Mkdir(sub, 0755)
	messy := filepath.Join(sub, "..", "target.txt")
	// filepath.Clean 会处理 ..

	res := runExecute(t, r, Context{Cwd: ""}, ReadArgs{Path: messy})
	if res.Status != "success" {
		t.Errorf("Status = %q, 期望成功（filepath.Clean 应处理 ..）. Output: %s", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, "found") {
		t.Errorf("Output = %q", res.Output)
	}
}

// =============================================================================
// Metadata 通知
// =============================================================================

// TestRead_MetadataNotification 验证 tc.Metadata 被正确调用
func TestRead_MetadataNotification(t *testing.T) {
	r, dir := newTestRead(t, 1000)
	path := writeFile(t, dir, "meta.txt", "a\nb\nc\n")

	var gotTitle string
	var gotMeta map[string]any
	tc := Context{
		Cwd: dir,
		Metadata: func(title string, meta map[string]any) {
			gotTitle = title
			gotMeta = meta
		},
	}

	raw, _ := json.Marshal(ReadArgs{Path: path})
	_, err := r.Execute(context.Background(), raw, tc)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(gotTitle, "meta.txt") {
		t.Errorf("title = %q, 应含文件名", gotTitle)
	}
	if gotMeta == nil {
		t.Fatal("metadata 未传")
	}
	if gotMeta["path"] != path {
		t.Errorf("path = %v", gotMeta["path"])
	}
	if gotMeta["line_count"] != 3 {
		t.Errorf("line_count = %v, 期望 3", gotMeta["line_count"])
	}
	if gotMeta["truncated"] != false {
		t.Errorf("truncated = %v, 期望 false", gotMeta["truncated"])
	}
}

// =============================================================================
// 参数解析
// =============================================================================

// TestRead_InvalidJSON 验证 args 不是合法 JSON 时返错
func TestRead_InvalidJSON(t *testing.T) {
	r, dir := newTestRead(t, 1000)

	raw := json.RawMessage(`{not valid json`)
	res, err := r.Execute(context.Background(), raw, Context{Cwd: dir})
	if err != nil {
		t.Fatalf("Execute 返 err: %v", err)
	}
	if res.Status != "error" {
		t.Errorf("Status = %q", res.Status)
	}
	if !strings.Contains(res.Output, "参数解析失败") {
		t.Errorf("Output = %q", res.Output)
	}
}

// TestRead_Definition 验证 Definition 返回正确元数据
func TestRead_Definition(t *testing.T) {
	r := &Read{}
	def := r.Definition()

	if def.ID != "read" {
		t.Errorf("ID = %q, 期望 read", def.ID)
	}
	if def.Description == "" {
		t.Error("Description 不能为空")
	}
	if len(def.Keywords) == 0 {
		t.Error("Keywords 不能为空")
	}
	if def.JSONSchema == nil {
		t.Error("JSONSchema 不能为 nil")
	}
	// Schema 必须是合法 JSON
	var schema struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal(def.JSONSchema, &schema); err != nil {
		t.Fatalf("Schema 不是合法 JSON: %v", err)
	}
	if schema.Type != "object" {
		t.Errorf("Schema type = %q", schema.Type)
	}
	if len(schema.Required) == 0 || schema.Required[0] != "path" {
		t.Errorf("Schema required = %v, 期望 [path]", schema.Required)
	}
}

// TestRead_ImplementsTool 编译时断言 Read 实现 Tool 接口
func TestRead_ImplementsTool(t *testing.T) {
	var _ Tool = (*Read)(nil)
}

// =============================================================================
// Context 行为
// =============================================================================

// TestRead_ContextCancelled 验证 ctx 取消时返错
func TestRead_ContextCancelled(t *testing.T) {
	r, dir := newTestRead(t, 1000)
	path := writeFile(t, dir, "cancel.txt", strings.Repeat("x\n", 1000))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	raw, _ := json.Marshal(ReadArgs{Path: path})
	res, err := r.Execute(ctx, raw, Context{Cwd: dir})
	if err != nil {
		t.Fatalf("Execute 返 err: %v", err)
	}
	if res.Status != "error" {
		t.Errorf("Status = %q, 期望 error", res.Status)
	}
	if !strings.Contains(res.Output, "取消") {
		t.Errorf("Output = %q", res.Output)
	}
}

// ========== truncateUTF8（v1.11 UTF-8 安全截断）==========

func TestTruncateUTF8_ASCII(t *testing.T) {
	if got := truncateUTF8("hello", 3); got != "hel" {
		t.Errorf("got %q, 期望 hel", got)
	}
}

func TestTruncateUTF8_NoTruncateNeeded(t *testing.T) {
	if got := truncateUTF8("hi", 10); got != "hi" {
		t.Errorf("got %q, 期望 hi（无需截断）", got)
	}
}

func TestTruncateUTF8_Zero(t *testing.T) {
	if got := truncateUTF8("hello", 0); got != "" {
		t.Errorf("got %q, 期望空串", got)
	}
}

// TestTruncateUTF8_ChineseBoundary 核心用例：中文每字 3 字节，
// 在字符中间截断时必须回退到完整字符边界，不能产生半个汉字。
func TestTruncateUTF8_ChineseBoundary(t *testing.T) {
	s := "你好世界" // 每字 3 字节，共 12 字节
	// maxBytes=7 落在第 3 个字「世」中间（6 字节是「你好」边界，7 在「世」内）
	got := truncateUTF8(s, 7)
	if !utf8.ValidString(got) {
		t.Errorf("截断结果非合法 UTF-8: %q", got)
	}
	if got != "你好" {
		t.Errorf("got %q, 期望「你好」（回退到字符边界）", got)
	}
}

// TestTruncateUTF8_ExactBoundary maxBytes 正好落在字符边界
func TestTruncateUTF8_ExactBoundary(t *testing.T) {
	s := "你好世界"
	got := truncateUTF8(s, 6) // 正好「你好」2 字 6 字节
	if got != "你好" {
		t.Errorf("got %q, 期望「你好」", got)
	}
}

// TestTruncateUTF8_Emoji emoji 4 字节，截断中间应回退
func TestTruncateUTF8_Emoji(t *testing.T) {
	s := "a😀b"                // a(1) + 😀(4) + b(1) = 6 字节
	got := truncateUTF8(s, 3) // 落在 😀 中间
	if !utf8.ValidString(got) {
		t.Errorf("非合法 UTF-8: %q", got)
	}
	if got != "a" {
		t.Errorf("got %q, 期望 a（回退掉半个 emoji）", got)
	}
}

// TestRead_TruncatedChinese 端到端：截断中文文件不产生乱码
func TestRead_TruncatedChinese(t *testing.T) {
	r, dir := newTestRead(t, 10)       // 10 字节预算
	content := strings.Repeat("中", 20) // 60 字节
	path := writeFile(t, dir, "cn.txt", content)

	res := runExecute(t, r, Context{Cwd: dir}, ReadArgs{Path: path})
	if res.Status != "success" {
		t.Fatalf("Status = %q", res.Status)
	}
	if !res.IsTruncated {
		t.Error("应被截断")
	}
	if !utf8.ValidString(res.Output) {
		t.Errorf("截断输出含半个汉字（非法 UTF-8）: %q", res.Output)
	}
}
