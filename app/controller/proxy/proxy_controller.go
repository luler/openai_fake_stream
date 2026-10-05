package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"gin_base/app/appconfig"
	"gin_base/app/helper/log_helper"
	"gin_base/app/helper/sse_helper"
	"gin_base/app/logic"
	"github.com/gin-gonic/gin"
)

// 反代入口：http://ip:port/https://api.openai.com/v1/chat/completions
// 无论客户端 stream 为 true 还是 false，请求上游时一律 stream=false；
// 客户端要求流式时，将上游的完整响应转换为 SSE 流式结构返回。
func Handle(c *gin.Context) {
	cfg := appconfig.Get()
	startTime := time.Now()

	target, err := logic.ParseTargetURL(c.Request.RequestURI)
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		writeError(c, http.StatusBadRequest, "read request body failed: "+err.Error())
		return
	}
	newBody, wantStream, includeUsage := logic.RewriteBody(body)

	ctx := c.Request.Context()
	if cfg.UpstreamTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.UpstreamTimeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, c.Request.Method, target, bytes.NewReader(newBody))
	if err != nil {
		writeError(c, http.StatusBadRequest, "build upstream request failed: "+err.Error())
		return
	}
	logic.CopyRequestHeaders(req.Header, c.Request.Header)
	if wantStream {
		req.Header.Set("Accept", "application/json")
	}

	// 异步请求上游；客户端要求流式时，等待期间定时发送心跳，避免客户端首字超时
	resultCh := make(chan logic.UpstreamResult, 1)
	go func() { resultCh <- logic.DoUpstream(req) }()

	w := sse_helper.NewSSEWriter(c, cfg.ChunkDelay)
	var keepAlive <-chan time.Time
	if wantStream && cfg.KeepAlive > 0 {
		ticker := time.NewTicker(cfg.KeepAlive)
		defer ticker.Stop()
		keepAlive = ticker.C
	}
	var res logic.UpstreamResult
wait:
	for {
		select {
		case res = <-resultCh:
			break wait
		case <-keepAlive:
			w.Comment("keep-alive")
		}
	}

	responsesAPI := logic.IsResponsesAPI(target)
	if res.Err != nil {
		log_helper.Error("upstream error: ", c.Request.Method, " ", target, " stream=", wantStream, " err=", res.Err)
		if c.Request.Context().Err() != nil {
			return // 客户端已断开
		}
		code := http.StatusBadGateway
		if ctx.Err() == context.DeadlineExceeded {
			code = http.StatusGatewayTimeout
		}
		msg := "upstream request failed: " + res.Err.Error()
		if w.Started() {
			logic.StreamError(w, responsesAPI, code, sse_helper.Marshal(errorBody(msg)))
		} else {
			writeError(c, code, msg)
		}
		return
	}
	log_helper.Info(c.Request.Method, " ", target, " stream=", wantStream, " status=", res.StatusCode, " cost=", time.Since(startTime))

	// 客户端本来就是非流式：原样透传
	if !wantStream {
		passthrough(c, res)
		return
	}

	isSuccess := res.StatusCode >= 200 && res.StatusCode < 300
	upstreamIsSSE := strings.Contains(strings.ToLower(res.Header.Get("Content-Type")), "text/event-stream")
	switch {
	case !isSuccess:
		// 上游报错：尚未开始输出则原样返回状态码与错误体，否则以流的形式下发错误
		if w.Started() {
			logic.StreamError(w, responsesAPI, res.StatusCode, res.Body)
		} else {
			passthrough(c, res)
		}
	case upstreamIsSSE:
		// 上游无视 stream=false 直接返回了 SSE，原样转发
		if w.Started() {
			w.Raw(res.Body)
		} else {
			passthrough(c, res)
		}
	default:
		logic.StreamJSON(w, res.Body, cfg.ChunkSize, includeUsage)
	}
	if err := w.Err(); err != nil {
		log_helper.Warning("write stream to client failed: ", target, " err=", err)
	}
}

// 原样透传上游响应
func passthrough(c *gin.Context, res logic.UpstreamResult) {
	logic.CopyResponseHeaders(c.Writer.Header(), res.Header)
	c.Status(res.StatusCode)
	c.Writer.Write(res.Body)
}

func errorBody(msg string) gin.H {
	return gin.H{"error": gin.H{"message": msg, "type": "proxy_error"}}
}

func writeError(c *gin.Context, code int, msg string) {
	c.JSON(code, errorBody(msg))
}
