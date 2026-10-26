# 两阶段构建：lite 版没有 protoc / 生成代码步骤，clone 后可直接编译。
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS builder

ARG TARGETARCH

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/devin-2api ./cmd/devin-2api

FROM alpine:3.22

RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=builder /out/devin-2api /app/devin-2api

EXPOSE 8080

ENTRYPOINT ["/app/devin-2api"]
