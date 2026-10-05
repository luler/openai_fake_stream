package appconfig

import (
	"os"
	"strconv"
	"sync"
	"time"
)

type Config struct {
	// 监听端口
	Port string
	// 请求上游的超时时间（整个请求，含读取响应体）
	UpstreamTimeout time.Duration
	// 假流式时每个分片包含的字符数（按 rune 计算），<=0 表示不切分，整段一次性下发
	ChunkSize int
	// 假流式时每个分片之间的间隔
	ChunkDelay time.Duration
	// 客户端要求流式时，等待上游期间每隔多久发送一次 SSE 心跳注释，0 表示不发送
	KeepAlive time.Duration
}

var (
	config     Config
	configOnce sync.Once
)

// 获取配置（从环境变量读取，.env 已在 app.InitApp 中加载）
func Get() Config {
	configOnce.Do(func() {
		config = Config{
			Port:            getEnv("PORT", "3000"),
			UpstreamTimeout: time.Duration(getEnvInt("UPSTREAM_TIMEOUT_SECONDS", 600)) * time.Second,
			ChunkSize:       getEnvInt("FAKE_STREAM_CHUNK_SIZE", 10),
			ChunkDelay:      time.Duration(getEnvInt("FAKE_STREAM_DELAY_MS", 0)) * time.Millisecond,
			KeepAlive:       time.Duration(getEnvInt("FAKE_STREAM_KEEPALIVE_SECONDS", 15)) * time.Second,
		}
	})
	return config
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}
