# Lcoder Docker 镜像
# 多阶段构建：编译 Go 二进制 → 最小运行镜像

FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /lcoder ./cmd/lcoder

FROM alpine:3.21
RUN apk add --no-cache ca-certificates git bash curl
COPY --from=builder /lcoder /usr/local/bin/lcoder
WORKDIR /workspace
ENV TERM=xterm-256color
ENV LC_ALL=C.UTF-8
ENTRYPOINT ["lcoder"]
CMD ["tui"]
