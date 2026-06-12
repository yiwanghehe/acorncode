package tool

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// fakeResolver 测试用 DNS 解析：按 map 返回结果，未命中返错。
// 把 example.com 映射到公网 IP，让现有测试不触真实 DNS、不依赖网络。
type fakeResolver struct {
	m map[string][]net.IPAddr
}

func (f *fakeResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	if addrs, ok := f.m[host]; ok {
		return addrs, nil
	}
	return nil, errors.New("fakeResolver: no such host " + host)
}

// useFakeResolver 安装一个把 example.com → 公网 IP 的 fake 解析器，
// 测试结束自动还原。其它域名可在 extra 里补充。
func useFakeResolver(t *testing.T, extra map[string][]net.IPAddr) {
	t.Helper()
	m := map[string][]net.IPAddr{
		"example.com": {{IP: net.ParseIP("93.184.216.34")}}, // example.com 真实公网 IP
	}
	for k, v := range extra {
		m[k] = v
	}
	old := resolver
	resolver = &fakeResolver{m: m}
	t.Cleanup(func() { resolver = old })
}

func newWebFetchArgs(t *testing.T, args WebFetchArgs) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func runWebFetch(t *testing.T, w *WebFetch, args json.RawMessage) Result {
	t.Helper()
	useFakeResolver(t, nil)
	res, err := w.Execute(context.Background(), args, Context{})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	return res
}

// mockTransport 自定义 RoundTripper，绕 SSRF 检查（用 example.com）
type mockTransport struct {
	handler func(*http.Request) (*http.Response, error)
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.handler(req)
}

// ========== 正常路径 ==========

func TestWebFetch_BasicGet(t *testing.T) {
	called := 0
	mt := &mockTransport{handler: func(req *http.Request) (*http.Response, error) {
		called++
		if req.Method != "GET" {
			t.Errorf("method = %s, 期望 GET", req.Method)
		}
		return &http.Response{
			StatusCode: 200,
			Header: http.Header{
				"Content-Type": []string{"text/html"},
				"X-Custom":     []string{"value"},
			},
			Body: io.NopCloser(strings.NewReader("hello world")),
		}, nil
	}}

	wf := &WebFetch{Client: &http.Client{Transport: mt, Timeout: 5 * time.Second}}
	res := runWebFetch(t, wf, newWebFetchArgs(t, WebFetchArgs{URL: "http://example.com/"}))

	if res.Status != "success" {
		t.Errorf("Status = %q, Output: %s", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, "200") {
		t.Errorf("Output 应含 200: %s", res.Output)
	}
	if !strings.Contains(res.Output, "hello world") {
		t.Errorf("Output 应含 body: %s", res.Output)
	}
	if !strings.Contains(res.Output, "X-Custom: value") {
		t.Errorf("Output 应含 header: %s", res.Output)
	}
	if called != 1 {
		t.Errorf("called = %d, 期望 1", called)
	}
}

func TestWebFetch_TruncateBody(t *testing.T) {
	mt := &mockTransport{handler: func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", 1000))),
		}, nil
	}}

	wf := &WebFetch{Client: &http.Client{Transport: mt}}
	res := runWebFetch(t, wf, newWebFetchArgs(t, WebFetchArgs{
		URL:     "http://example.com/",
		MaxSize: 100,
	}))

	if res.Status != "success" {
		t.Errorf("Status = %q", res.Status)
	}
	if !res.IsTruncated {
		t.Errorf("IsTruncated 应 true（超 100）")
	}
	if !strings.Contains(res.Output, "截断") {
		t.Errorf("Output 应说明截断: %s", res.Output)
	}
}

func TestWebFetch_4xxIsError(t *testing.T) {
	mt := &mockTransport{handler: func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 404,
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("not found")),
		}, nil
	}}

	wf := &WebFetch{Client: &http.Client{Transport: mt}}
	res := runWebFetch(t, wf, newWebFetchArgs(t, WebFetchArgs{URL: "http://example.com/"}))

	if res.Status != "error" {
		t.Errorf("404 应 error: %s", res.Status)
	}
	// 仍返 body（让模型看错误响应）
	if !strings.Contains(res.Output, "not found") {
		t.Errorf("Output 应含 404 body: %s", res.Output)
	}
}

func TestWebFetch_5xxIsError(t *testing.T) {
	mt := &mockTransport{handler: func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 500,
			Body:       io.NopCloser(strings.NewReader("internal error")),
		}, nil
	}}

	wf := &WebFetch{Client: &http.Client{Transport: mt}}
	res := runWebFetch(t, wf, newWebFetchArgs(t, WebFetchArgs{URL: "http://example.com/"}))

	if res.Status != "error" {
		t.Errorf("500 应 error: %s", res.Status)
	}
}

func TestWebFetch_Headers(t *testing.T) {
	var gotAuth string
	mt := &mockTransport{handler: func(req *http.Request) (*http.Response, error) {
		gotAuth = req.Header.Get("Authorization")
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	}}

	wf := &WebFetch{Client: &http.Client{Transport: mt}}
	res := runWebFetch(t, wf, newWebFetchArgs(t, WebFetchArgs{
		URL:     "http://example.com/",
		Headers: map[string]string{"Authorization": "Bearer xyz"},
	}))

	if gotAuth != "Bearer xyz" {
		t.Errorf("Authorization = %q, 期望 Bearer xyz", gotAuth)
	}
	if res.Status != "success" {
		t.Errorf("Status = %q", res.Status)
	}
}

func TestWebFetch_DefaultUA(t *testing.T) {
	var gotUA string
	mt := &mockTransport{handler: func(req *http.Request) (*http.Response, error) {
		gotUA = req.Header.Get("User-Agent")
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	}}

	wf := &WebFetch{Client: &http.Client{Transport: mt}}
	_ = runWebFetch(t, wf, newWebFetchArgs(t, WebFetchArgs{URL: "http://example.com/"}))

	if !strings.Contains(gotUA, "AcornCode") {
		t.Errorf("UA 应含 AcornCode, got %q", gotUA)
	}
}

func TestWebFetch_CustomUA(t *testing.T) {
	var gotUA string
	mt := &mockTransport{handler: func(req *http.Request) (*http.Response, error) {
		gotUA = req.Header.Get("User-Agent")
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	}}

	wf := &WebFetch{Client: &http.Client{Transport: mt}}
	_ = runWebFetch(t, wf, newWebFetchArgs(t, WebFetchArgs{
		URL:     "http://example.com/",
		Headers: map[string]string{"User-Agent": "Custom/1.0"},
	}))

	if gotUA != "Custom/1.0" {
		t.Errorf("UA = %q, 期望 Custom/1.0", gotUA)
	}
}

func TestWebFetch_PostBody(t *testing.T) {
	var gotBody string
	var gotMethod string
	mt := &mockTransport{handler: func(req *http.Request) (*http.Response, error) {
		gotMethod = req.Method
		buf := make([]byte, 100)
		n, _ := req.Body.Read(buf)
		gotBody = string(buf[:n])
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	}}

	wf := &WebFetch{Client: &http.Client{Transport: mt}}
	_ = runWebFetch(t, wf, newWebFetchArgs(t, WebFetchArgs{
		URL:    "http://example.com/",
		Method: "POST",
		Body:   "key=value",
	}))

	if gotMethod != "POST" {
		t.Errorf("method = %s, 期望 POST", gotMethod)
	}
	if gotBody != "key=value" {
		t.Errorf("body = %q, 期望 key=value", gotBody)
	}
}

func TestWebFetch_Timeout(t *testing.T) {
	mt := &mockTransport{handler: func(req *http.Request) (*http.Response, error) {
		// 等 ctx 取消
		<-req.Context().Done()
		return nil, req.Context().Err()
	}}

	wf := &WebFetch{Client: &http.Client{Transport: mt}}
	res := runWebFetch(t, wf, newWebFetchArgs(t, WebFetchArgs{
		URL:     "http://example.com/",
		Timeout: 1, // 1s
	}))

	if res.Status != "error" {
		t.Errorf("超时应 error: %s, Output: %s", res.Status, res.Output)
	}
}

func TestWebFetch_RedirectLimit(t *testing.T) {
	// 简单测试：只验证 redirect 不会 panic，SSRF 拒绝时返 err
	// 不深测 redirect 行为（依赖 client 内部状态）
	mt := &mockTransport{handler: func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 302,
			Header:     http.Header{"Location": []string{"http://127.0.0.1/blocked"}},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	}}
	wf := &WebFetch{Client: &http.Client{Transport: mt}}
	res := runWebFetch(t, wf, newWebFetchArgs(t, WebFetchArgs{URL: "http://example.com/"}))
	// 重定向到 127.0.0.1 会被 SSRF 拦
	if res.Status != "error" {
		t.Errorf("重定向到 127.0.0.1 应 error（SSRF 拦）: %s, %s", res.Status, res.Output)
	}
}

// ========== 异常路径 ==========

func TestWebFetch_EmptyURL(t *testing.T) {
	res := runWebFetch(t, &WebFetch{}, newWebFetchArgs(t, WebFetchArgs{URL: ""}))
	if res.Status != "error" {
		t.Errorf("空 URL 应 error: %s", res.Status)
	}
}

func TestWebFetch_BadScheme(t *testing.T) {
	res := runWebFetch(t, &WebFetch{}, newWebFetchArgs(t, WebFetchArgs{URL: "ftp://example.com/"}))
	if res.Status != "error" {
		t.Errorf("ftp scheme 应 error: %s", res.Status)
	}
	if !strings.Contains(res.Output, "scheme") {
		t.Errorf("Output 应说明 scheme 错: %s", res.Output)
	}
}

func TestWebFetch_InvalidURL(t *testing.T) {
	res := runWebFetch(t, &WebFetch{}, newWebFetchArgs(t, WebFetchArgs{URL: "not a url"}))
	if res.Status != "error" {
		t.Errorf("坏 URL 应 error: %s", res.Status)
	}
}

func TestWebFetch_InvalidMethod(t *testing.T) {
	res := runWebFetch(t, &WebFetch{}, newWebFetchArgs(t, WebFetchArgs{
		URL:    "http://example.com",
		Method: "DELETE",
	}))
	if res.Status != "error" {
		t.Errorf("DELETE method 应 error: %s", res.Status)
	}
}

func TestWebFetch_InvalidArgs(t *testing.T) {
	res, err := (&WebFetch{}).Execute(context.Background(), []byte("not json"), Context{})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if res.Status != "error" {
		t.Errorf("坏 JSON 应 error: %s", res.Status)
	}
}

// ========== SSRF 防护 ==========

func TestWebFetch_BlockLocalhost(t *testing.T) {
	res := runWebFetch(t, &WebFetch{}, newWebFetchArgs(t, WebFetchArgs{URL: "http://localhost/foo"}))
	if res.Status != "error" {
		t.Errorf("localhost 应 error: %s", res.Status)
	}
	if !strings.Contains(res.Output, "localhost") {
		t.Errorf("Output 应说明拒绝 localhost: %s", res.Output)
	}
}

func TestWebFetch_BlockLoopbackIP(t *testing.T) {
	res := runWebFetch(t, &WebFetch{}, newWebFetchArgs(t, WebFetchArgs{URL: "http://127.0.0.1/foo"}))
	if res.Status != "error" {
		t.Errorf("127.0.0.1 应 error: %s", res.Status)
	}
	if !strings.Contains(res.Output, "私有/回环") {
		t.Errorf("Output 应说明拒绝: %s", res.Output)
	}
}

func TestWebFetch_BlockPrivateIP(t *testing.T) {
	tests := []string{
		"http://10.0.0.1/",
		"http://192.168.1.1/",
		"http://172.16.0.1/",
		"http://169.254.169.254/", // AWS metadata
	}
	for _, u := range tests {
		t.Run(u, func(t *testing.T) {
			res := runWebFetch(t, &WebFetch{}, newWebFetchArgs(t, WebFetchArgs{URL: u}))
			if res.Status != "error" {
				t.Errorf("%s 应 error: %s", u, res.Status)
			}
		})
	}
}

func TestWebFetch_BlockUnspecifiedIP(t *testing.T) {
	res := runWebFetch(t, &WebFetch{}, newWebFetchArgs(t, WebFetchArgs{URL: "http://0.0.0.0/"}))
	if res.Status != "error" {
		t.Errorf("0.0.0.0 应 error: %s", res.Status)
	}
}

// TestWebFetch_BlockInternalDomain 验证 v1.11 新能力：
// 解析到私有 IP 的域名被拦截（堵住 v0.3 遗留的内网域名绕过）。
func TestWebFetch_BlockInternalDomain(t *testing.T) {
	// internal.corp 解析到 10.0.0.5（私有），应被拒
	useFakeResolver(t, map[string][]net.IPAddr{
		"internal.corp": {{IP: net.ParseIP("10.0.0.5")}},
	})
	res, err := (&WebFetch{}).Execute(context.Background(),
		newWebFetchArgs(t, WebFetchArgs{URL: "http://internal.corp/secret"}), Context{})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if res.Status != "error" {
		t.Errorf("内网域名应被拦截: %s", res.Output)
	}
	if !strings.Contains(res.Output, "受限地址") {
		t.Errorf("Output 应说明解析到受限地址: %s", res.Output)
	}
}

// TestWebFetch_BlockMixedResolve 验证多 IP 中任一为私有即拒绝（DNS rebinding 防护）
func TestWebFetch_BlockMixedResolve(t *testing.T) {
	useFakeResolver(t, map[string][]net.IPAddr{
		"evil.com": {
			{IP: net.ParseIP("8.8.8.8")},   // 公网
			{IP: net.ParseIP("127.0.0.1")}, // 但也含回环
		},
	})
	res, err := (&WebFetch{}).Execute(context.Background(),
		newWebFetchArgs(t, WebFetchArgs{URL: "http://evil.com/"}), Context{})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if res.Status != "error" {
		t.Errorf("含私有 IP 的多解析应被拦截: %s", res.Output)
	}
}

// TestWebFetch_ResolveFail 验证域名解析失败时保守拒绝
func TestWebFetch_ResolveFail(t *testing.T) {
	useFakeResolver(t, nil) // 只认 example.com，其它解析失败
	res, err := (&WebFetch{}).Execute(context.Background(),
		newWebFetchArgs(t, WebFetchArgs{URL: "http://nonexistent.invalid/"}), Context{})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if res.Status != "error" {
		t.Errorf("解析失败应拒绝: %s", res.Output)
	}
}

// TestCheckSSRF_PublicDomainAllowed 验证解析到公网 IP 的域名放行
func TestCheckSSRF_PublicDomainAllowed(t *testing.T) {
	useFakeResolver(t, nil) // example.com → 公网
	if err := checkSSRF(context.Background(), "example.com"); err != nil {
		t.Errorf("公网域名不应被拦: %v", err)
	}
}
