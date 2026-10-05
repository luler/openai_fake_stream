# openai_fake_stream

#### 介绍

OpenAI 接口「假流式」反向代理，用于兼容**只支持非流式**的上游。

- 访问方式：`http://ip:port/` + 上游完整地址，例如 `http://127.0.0.1:3000/https://api.openai.com/v1/chat/completions`
- 无论客户端 `stream` 是 `true` 还是 `false`，请求上游时一律改为 `stream=false`（并去掉 `stream_options`）
- 客户端 `stream=false`：上游响应原样返回
- 客户端 `stream=true`：把上游的完整 JSON 转成 SSE 流式结构返回
  - `/v1/chat/completions` → `chat.completion.chunk`（支持 `content`、`reasoning_content`、`tool_calls`，`stream_options.include_usage` 时返回 usage）
  - `/v1/completions` → `text_completion` 流
  - `/v1/responses` → Responses API 事件流（`response.created` … `response.completed`）
  - 其他结构：整体作为一个 `data:` 事件 + `data: [DONE]`
- 请求头（含 `Authorization`）、查询参数原样转发；无 `stream` 字段的请求（如 `GET /v1/models`）透明转发
- 上游报错时，原样返回上游状态码和错误体

#### 配置（.env）

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PORT` | 3000 | 监听端口 |
| `UPSTREAM_TIMEOUT_SECONDS` | 600 | 请求上游超时 |
| `FAKE_STREAM_CHUNK_SIZE` | 10 | 每个分片的字符数，`<=0` 不切分 |
| `FAKE_STREAM_DELAY_MS` | 0 | 分片之间的间隔，用于模拟打字效果 |
| `FAKE_STREAM_KEEPALIVE_SECONDS` | 15 | 流式请求等待上游期间发送 SSE 心跳（`: keep-alive`）的间隔，`0` 关闭；心跳开始后若上游报错，以 SSE 事件形式返回错误 |

#### 安装

```
go mod tidy
```

#### 运行

```
go run main.go serve
```

#### 示例

```
curl -N http://127.0.0.1:3000/https://api.openai.com/v1/chat/completions \
  -H "Authorization: Bearer sk-xxx" -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"hi"}]}'
```