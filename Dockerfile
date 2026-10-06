# ============================================
# Этап 1: сборка бинарника
# ============================================
FROM golang:1.27-alpine AS builder

# Устанавливаем нужные утилиты (для CGO, если понадобится)
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

# Копируем файлы зависимостей ОТДЕЛЬНО — для кэширования слоёв.
# Если go.mod/go.sum не менялись, docker не будет скачивать зависимости заново.
COPY go.mod go.sum ./
RUN go mod download

# Копируем исходники
COPY . .

# Собираем статический бинарник
# CGO_ENABLED=0 — статическая линковка, чтобы работало на scratch/alpine
# -ldflags="-s -w" — уменьшает размер
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /app/auth ./cmd/auth

# ============================================
# Этап 2: минимальный runtime-образ
# ============================================
FROM alpine:3.20

# Устанавливаем корневые сертификаты (для HTTPS-запросов к БД/внешним API)
# и tzdata для корректной работы с часовыми поясами
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -g '' appuser

WORKDIR /app

# Копируем бинарник из builder-этапа
COPY --from=builder --chmod=755 /app/auth .

# Копируем миграции (если приложение их применяет при старте)
COPY --from=builder --chmod=755 /app/migrations ./migrations

# Работаем от непривилегированного пользователя
USER appuser

# Порт, который слушает сервис
EXPOSE 8080

# Запуск
CMD ["./auth"]