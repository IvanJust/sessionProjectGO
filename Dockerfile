# ============================================
# Этап 1: сборка бинарника
# ============================================
FROM golang:1.27-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata
WORKDIR /app

# Если go.mod/go.sum не менялись, docker не будет скачивать зависимости заново.
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 \
    go build -ldflags="-s -w" -o /app/auth ./cmd/auth

# ============================================
# Этап 2: минимальный runtime-образ
# ============================================
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -g '' appuser

WORKDIR /app

COPY --from=builder --chmod=755 /app/auth .
USER appuser

# Порт, который слушает сервис
EXPOSE 8080

# Запуск
CMD ["./auth"]
