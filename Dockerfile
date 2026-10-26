# 两阶段构建：lite 版没有 protoc / 生成代码步骤，clone 后可直接编译。
FROM --platform=$BUILDPLATFORM golang:1.26.3-alpine AS builder

ARG TARGETARCH

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/devin-2api ./cmd/devin-2api

FROM alpine:3.22

RUN apk add --no-cache ca-certificates \
    && adduser -D -H -u 10001 app \
    && mkdir -p /app/logs \
    && chown -R app:app /app

WORKDIR /app
COPY --from=builder /out/devin-2api /app/devin-2api

# 以非 root 运行；debug.enabled=true 时日志写入 /app/logs。
USER 10001

EXPOSE 18089

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:18089/healthz >/dev/null || exit 1

ENTRYPOINT ["/app/devin-2api"]
