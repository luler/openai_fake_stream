package middleware

import (
	"fmt"
	"gin_base/app/helper/log_helper"
	"github.com/gin-gonic/gin"
	"net/http"
)

// 初始化全局中间件
func InitMiddleware(e *gin.Engine) {
	//异常捕获中间件
	e.Use(Exception())
}

// 异常捕获中间件，返回 OpenAI 风格的错误结构
func Exception() gin.HandlerFunc {
	return func(context *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				if r == http.ErrAbortHandler {
					panic(r)
				}
				msg := fmt.Sprintf("%v", r)
				log_helper.Error("panic: ", msg)
				if !context.Writer.Written() {
					context.JSON(http.StatusInternalServerError, gin.H{
						"error": gin.H{"message": msg, "type": "proxy_error"},
					})
				}
				context.Abort() //终止请求
			}
		}()

		context.Next()
	}
}
