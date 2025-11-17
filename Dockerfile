# Многоступенчатая сборка для минимального размера образа

# Этап 1: Сборка приложения
FROM golang:1.23-alpine AS builder

# Устанавливаем необходимые пакеты для сборки
RUN apk add --no-cache git ca-certificates

# Устанавливаем рабочую директорию
WORKDIR /app

# Копируем все файлы проекта
COPY . .

# Инициализируем модуль и загружаем зависимости
RUN go mod download && go mod verify

# Собираем приложение
# CGO_ENABLED=0 для статической линковки
# -ldflags="-w -s" для уменьшения размера бинарника
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -a -installsuffix cgo \
    -ldflags="-w -s" \
    -o rtsconns-api \
    main.go

# Этап 2: Финальный образ
FROM alpine:latest

# Устанавливаем ca-certificates для HTTPS запросов и wget для healthcheck
RUN apk --no-cache add ca-certificates tzdata wget && \
    update-ca-certificates

# Создаем непривилегированного пользователя
RUN addgroup -g 1000 appuser && \
    adduser -D -u 1000 -G appuser appuser

# Устанавливаем рабочую директорию
WORKDIR /app

# Копируем бинарник из builder
COPY --from=builder --chown=appuser:appuser /app/rtsconns-api .

# Переключаемся на непривилегированного пользователя
USER appuser

# Открываем порт
EXPOSE 8080

# Healthcheck
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

# Запускаем приложение
ENTRYPOINT ["./rtsconns-api"]