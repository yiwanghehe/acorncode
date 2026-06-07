// Package tool - webfetch.go HTTP 抓取工具
package tool

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

//go:embed schemas/webfetch.json
var webfetchSchema []byte

// WebFetchArgs 是 webfetch 工具的入参
type WebFetchArgs struct {
	URL     string            `json:"url"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	MaxSize int               `json:"max_size,omitempty"`
	Timeout int               `json:"timeout,omitempty"`
}

// WebFetch 是 webfetch 工具的实现
type WebFetch struct {
	// Client 用于测试时注入（默认新建）
	Client *http.Client
}

// Definition 返回 webfetch 工具元数据
func (w *WebFetch) Definition() Definition {
	return Definition{
		ID:          "webfetch",
		Description: "Fetch content from a URL via HTTP/HTTPS. Returns status code, response headers, and body (truncated to max_size). Blocks private/loopback IPs by default (SSRF prevention). Use to read documentation, fetch latest releases, check upstream issues.",
		Keywords:    []string{"webfetch", "http", "fetch", "download", "url", "curl", "网络", "抓取", "下载"},
		JSONSchema:  webfetchSchema,
	}
}

// Execute 是 webfetch 工具入口
func (w *WebFetch) Execute(ctx context.Context, args json.RawMessage, tc Context) (Result, error) {
	var a WebFetchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{Status: "error", Title: "webfetch", Output: "参数解析失败: " + err.Error()}, nil
	}

	if a.URL == "" {
		return Result{Status: "error", Title: "webfetch", Output: "url 不能为空"}, nil
	}

	// 1. 解析 URL
	u, err := url.Parse(a.URL)
	if err != nil {
		return Result{Status: "error", Title: "webfetch", Output: "URL 解析失败: " + err.Error()}, nil
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Result{Status: "error", Title: "webfetch", Output: "URL scheme 必须是 http 或 https，got: " + u.Scheme}, nil
	}

	// 2. SSRF 防护：解析 host IP，禁私有 / 回环
	host := u.Hostname()
	if host == "" {
		return Result{Status: "error", Title: "webfetch", Output: "URL 缺 host"}, nil
	}
	if err := checkSSRF(host); err != nil {
		return Result{Status: "error", Title: "webfetch", Output: err.Error()}, nil
	}

	// 3. method 默认 GET
	method := strings.ToUpper(a.Method)
	if method == "" {
		method = "GET"
	}
	if method != "GET" && method != "POST" && method != "HEAD" {
		return Result{Status: "error", Title: "webfetch", Output: "method 必须是 GET/POST/HEAD，got: " + method}, nil
	}

	// 4. max_size 默认 1MB
	maxSize := a.MaxSize
	if maxSize <= 0 {
		maxSize = 1024 * 1024
	}

	// 5. timeout 默认 30s
	timeout := time.Duration(a.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	// 6. 构造请求（ctx 控制总超时；client 内部 timeout 二选一）
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var bodyReader io.Reader
	if a.Body != "" {
		bodyReader = strings.NewReader(a.Body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, a.URL, bodyReader)
	if err != nil {
		return Result{Status: "error", Title: "webfetch", Output: "构造请求失败: " + err.Error()}, nil
	}
	// 自定义 header
	for k, v := range a.Headers {
		req.Header.Set(k, v)
	}
	// 默认 UA
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "AcornCode/0.3 (+https://github.com/yiwanghehe/acorncode)")
	}

	// 7. 发送请求
	client := w.Client
	if client == nil {
		client = &http.Client{
			Timeout: timeout,
			// 限制重定向次数（防 SSRF 链）
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("stopped after 5 redirects")
				}
				// 重定向也走 SSRF 检查
				return checkSSRF(req.URL.Hostname())
			},
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		if reqCtx.Err() != nil {
			return Result{Status: "error", Title: "webfetch", Output: "ctx 已取消: " + reqCtx.Err().Error()}, nil
		}
		return Result{Status: "error", Title: "webfetch", Output: "请求失败: " + err.Error()}, nil
	}
	defer resp.Body.Close()

	// 8. 限流读 body
	limited := io.LimitReader(resp.Body, int64(maxSize+1))
	body, err := io.ReadAll(limited)
	if err != nil {
		return Result{Status: "error", Title: "webfetch", Output: "读 body 失败: " + err.Error()}, nil
	}
	truncated := len(body) > maxSize
	if truncated {
		body = body[:maxSize]
	}

	// 9. metadata
	if tc.Metadata != nil {
		tc.Metadata(fmt.Sprintf("webfetch %s (%d)", a.URL, resp.StatusCode), map[string]any{
			"url":          a.URL,
			"status":       resp.StatusCode,
			"body_size":    len(body),
			"truncated":    truncated,
			"content_type": resp.Header.Get("Content-Type"),
		})
	}

	// 10. 拼输出
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== STATUS ===\n%d %s\n", resp.StatusCode, http.StatusText(resp.StatusCode)))
	sb.WriteString("\n=== HEADERS ===\n")
	for k, v := range resp.Header {
		sb.WriteString(fmt.Sprintf("%s: %s\n", k, strings.Join(v, ", ")))
	}
	sb.WriteString("\n=== BODY ===\n")
	sb.Write(body)
	if truncated {
		sb.WriteString(fmt.Sprintf("\n\n[... 已截断，超出 %d 上限]", maxSize))
	}

	status := "success"
	if resp.StatusCode >= 400 {
		// HTTP 4xx/5xx 视作业务错，但仍返 body（让模型看错误响应）
		status = "error"
	}

	return Result{
		Status:      status,
		Title:       fmt.Sprintf("webfetch %d", resp.StatusCode),
		Output:      sb.String(),
		IsTruncated: truncated,
	}, nil
}

// checkSSRF 检查 host 是否为私有 / 回环 IP
func checkSSRF(host string) error {
	// localhost 字符串
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("拒绝访问 localhost")
	}

	// 解析 IP
	ip := net.ParseIP(host)
	if ip == nil {
		// 不是 IP，可能是域名：解析后检查（但会触发 DNS 解析 → v0.3 简化：跳过）
		// 安全模型：要求用户用 IP 访问内网（不常见）；或者禁用私有 IP 后让用户显式 allow
		// v0.3 简化：只检查字面 IP，域名不解析（防止额外 DNS 依赖）
		return nil
	}

	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("拒绝访问私有/回环 IP: %s", ip)
	}
	if ip.IsUnspecified() {
		return fmt.Errorf("拒绝访问未指定 IP: %s", ip)
	}
	return nil
}
