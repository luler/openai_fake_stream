package route

import (
	"gin_base/app/controller/proxy"
	"github.com/gin-gonic/gin"
)

func InitRouter(e *gin.Engine) {
	// 所有请求都走反代：http://ip:port/https://api.openai.com/v1/chat/completions
	e.Any("/*path", proxy.Handle)
}
