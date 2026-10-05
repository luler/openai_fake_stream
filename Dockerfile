# 构建阶段
FROM golang:1.21.0-alpine3.18 AS builder

WORKDIR /app

# 安装CA证书和upx
RUN apk add --no-cache ca-certificates upx && \
    go env -w GOPROXY=https://goproxy.cn,direct

# 先复制依赖文件并下载，利用 Docker 层缓存
COPY go.mod go.sum ./
RUN go mod download

# 复制源码
COPY . .

# -tags timetzdata: 内嵌时区数据，使 scratch 运行镜像能正确解析 TZ 环境变量
# -ldflags="-s -w": 去除符号表和调试信息减小体积
# CGO_ENABLED=0: 纯静态编译
RUN CGO_ENABLED=0 go build -tags timetzdata -ldflags="-s -w" -o /app/main . && \
    upx --best --lzma /app/main

# 运行阶段（极简 scratch 镜像）
FROM scratch
WORKDIR /app

# 复制CA证书和编译出的可执行文件
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /app/main .

EXPOSE 3000

CMD ["./main", "serve"]
