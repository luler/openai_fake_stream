package app

import (
	"gin_base/app/helper/log_helper"
	"github.com/joho/godotenv"
)

const (
	InitTypeBase string = "base"
)

// 项目启动初始化
func InitApp(initTypes ...string) {
	for _, s := range initTypes {
		switch s {
		case InitTypeBase:
			//加载.env配置
			godotenv.Load()
			//初始化日志记录方式
			log_helper.InitlogHelper()
		}
	}
}
