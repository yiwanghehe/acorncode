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
		Description: "Fetch content from a URL via HTTP/HTTPS. Returns status code, response headers, and body (truncated to max_size). Blocks private/loopback IPs and domains that resolve to them (SSRF prevention, incl. DNS resolution check). Use to read documentation, fetch latest releases, check upstream issues.",
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
	if err := checkSSRF(ctx, host); err != nil {
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
				// 重定向也走 SSRF 检查（含域名解析）
				return checkSSRF(req.Context(), req.URL.Hostname())
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

// resolver 供测试注入自定义 DNS 解析（默认用系统解析器）
var resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
} = net.DefaultResolver

// checkSSRF 检查 host 是否指向私有 / 回环 / 链路本地 IP。
//
// 安全模型（v1.11 强化）：
//   - host 是字面 IP：直接判定。
//   - host 是域名：解析 DNS，**所有**解析出的 IP 都必须是公网，
//     任一为私有/回环/链路本地即拒绝。这堵住了 v0.3 遗留的「内网域名绕过」
//     与部分 DNS rebinding（请求前再校验一次解析结果）。
//   - 解析失败：保守拒绝（宁可错杀，避免把不可控目标放进来）。
//
// DNS 解析派生 5s 超时子 ctx，避免恶意/不可达域名长时间阻塞。
func checkSSRF(ctx context.Context, host string) error {
	// localhost 字符串
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("拒绝访问 localhost")
	}

	// 字面 IP：直接判定
	if ip := net.ParseIP(host); ip != nil {
		return checkIPBlocked(ip)
	}

	// 域名：解析后逐一校验
	dnsCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := resolver.LookupIPAddr(dnsCtx, host)
	if err != nil {
		return fmt.Errorf("拒绝访问：域名解析失败 %s: %v", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("拒绝访问：域名 %s 无解析结果", host)
	}
	for _, a := range addrs {
		if err := checkIPBlocked(a.IP); err != nil {
			return fmt.Errorf("拒绝访问：域名 %s 解析到受限地址 %s", host, a.IP)
		}
	}
	return nil
}

// checkIPBlocked 判断单个 IP 是否落在受限网段（私有/回环/链路本地/未指定）
func checkIPBlocked(ip net.IP) error {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("拒绝访问私有/回环 IP: %s", ip)
	}
	if ip.IsUnspecified() {
		return fmt.Errorf("拒绝访问未指定 IP: %s", ip)
	}
	return nil
}
