package logic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"gin_base/app/helper/sse_helper"
)

// 逐跳头，不应转发
var hopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

var upstreamClient = &http.Client{
	Transport: http.DefaultTransport.(*http.Transport).Clone(),
}

// 上游请求结果
type UpstreamResult struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	Err        error
}

// 从请求 URI 中解析上游地址
// 例如 /https://api.openai.com/v1/chat/completions?a=1 -> https://api.openai.com/v1/chat/completions?a=1
// 兼容被中间层合并了双斜杠的情况：/https:/api.openai.com/v1/...
func ParseTargetURL(requestURI string) (string, error) {
	raw := strings.TrimLeft(requestURI, "/")
	lower := strings.ToLower(raw)
	for _, scheme := range []string{"https:", "http:"} {
		if strings.HasPrefix(lower, scheme) {
			raw = scheme + "//" + strings.TrimLeft(raw[len(scheme):], "/")
			break
		}
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("invalid upstream url %q, usage: http://ip:port/https://api.openai.com/v1/chat/completions", raw)
	}
	return raw, nil
}

// 是否为 Responses API
func IsResponsesAPI(target string) bool {
	u, err := url.Parse(target)
	if err != nil {
		return false
	}
	return strings.Contains(u.Path, "/responses")
}

// 改写请求体：若存在 stream 字段，则强制改为 false，并移除 stream_options（非流式请求带上它上游会报错）
// 返回新请求体、客户端是否要求流式、客户端是否要求在流中返回 usage
func RewriteBody(body []byte) (newBody []byte, wantStream bool, includeUsage bool) {
	var m map[string]json.RawMessage
	if len(bytes.TrimSpace(body)) == 0 || json.Unmarshal(body, &m) != nil || m == nil {
		return body, false, false
	}
	streamRaw, ok := m["stream"]
	if !ok {
		return body, false, false
	}
	_ = json.Unmarshal(streamRaw, &wantStream)
	if so, ok := m["stream_options"]; ok {
		var opts struct {
			IncludeUsage bool `json:"include_usage"`
		}
		_ = json.Unmarshal(so, &opts)
		includeUsage = opts.IncludeUsage
		delete(m, "stream_options")
	}
	m["stream"] = json.RawMessage("false")
	return sse_helper.Marshal(m), wantStream, includeUsage
}

// 复制客户端请求头到上游请求
func CopyRequestHeaders(dst, src http.Header) {
	for k, vv := range src {
		dst[k] = append([]string(nil), vv...)
	}
	for _, h := range hopHeaders {
		dst.Del(h)
	}
	dst.Del("Host")
	dst.Del("Content-Length")
	// 去掉 Accept-Encoding，交给 Go 的 Transport 自动处理 gzip 并解压，便于解析 JSON
	dst.Del("Accept-Encoding")
}

// 复制上游响应头到客户端
func CopyResponseHeaders(dst, src http.Header) {
	for k, vv := range src {
		dst[k] = append([]string(nil), vv...)
	}
	for _, h := range hopHeaders {
		dst.Del(h)
	}
	dst.Del("Content-Length")
}

// 请求上游并读取完整响应体
func DoUpstream(req *http.Request) UpstreamResult {
	resp, err := upstreamClient.Do(req)
	if err != nil {
		return UpstreamResult{Err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return UpstreamResult{Err: err}
	}
	return UpstreamResult{StatusCode: resp.StatusCode, Header: resp.Header, Body: body}
}
