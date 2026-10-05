package sse_helper

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// SSEWriter 负责向客户端输出 text/event-stream 数据
// 内部记录第一次写失败的错误（如客户端断开），之后的写操作全部忽略
type SSEWriter struct {
	c       *gin.Context
	delay   time.Duration
	started bool
	err     error
}

func NewSSEWriter(c *gin.Context, delay time.Duration) *SSEWriter {
	return &SSEWriter{c: c, delay: delay}
}

// 是否已经向客户端发送了 SSE 响应头
func (w *SSEWriter) Started() bool { return w.started }

// 第一次写失败的错误
func (w *SSEWriter) Err() error { return w.err }

// 发送 SSE 响应头（只会发送一次）
func (w *SSEWriter) Start() {
	if w.started {
		return
	}
	w.started = true
	h := w.c.Writer.Header()
	h.Del("Content-Length")
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.c.Status(200)
	w.c.Writer.WriteHeaderNow()
	w.c.Writer.Flush()
}

func (w *SSEWriter) write(s string, pause bool) {
	if w.err != nil {
		return
	}
	w.Start()
	if _, err := io.WriteString(w.c.Writer, s); err != nil {
		w.err = err
		return
	}
	w.c.Writer.Flush()
	ctx := w.c.Request.Context()
	if err := ctx.Err(); err != nil {
		w.err = err
		return
	}
	if pause && w.delay > 0 {
		select {
		case <-time.After(w.delay):
		case <-ctx.Done():
			w.err = ctx.Err()
		}
	}
}

// 注释行，用于心跳保活
func (w *SSEWriter) Comment(text string) {
	w.write(": "+text+"\n\n", false)
}

// data: {json}
func (w *SSEWriter) Data(v any) {
	w.write("data: "+string(Marshal(v))+"\n\n", true)
}

// event: name \n data: {json}
func (w *SSEWriter) Event(name string, v any) {
	w.write("event: "+name+"\ndata: "+string(Marshal(v))+"\n\n", true)
}

// 原样输出一段 JSON（会压缩成单行，保证符合 SSE 格式）
func (w *SSEWriter) DataRaw(b []byte) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err == nil {
		w.write("data: "+buf.String()+"\n\n", true)
		return
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	w.write("data: "+strings.Join(lines, "\ndata: ")+"\n\n", true)
}

// data: [DONE]
func (w *SSEWriter) Done() {
	w.write("data: [DONE]\n\n", false)
}

// 原样写入字节（上游本身就返回了 SSE 时使用）
func (w *SSEWriter) Raw(b []byte) {
	w.write(string(b), false)
}

// JSON 序列化，不转义 HTML 字符
func Marshal(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return []byte("null")
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}
