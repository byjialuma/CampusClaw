# 构建上下文为仓库根目录（见 docker-compose.yml）
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY backend/ ./
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.20
RUN adduser -D -u 10001 app && mkdir -p /app/data /app/seed/materials && chown -R app:app /app
COPY --from=build /out/server /app/server
# 预置讲义（仅两班可区分的种子材料）
COPY deploy/seed/materials/ /app/seed/materials/
USER app
WORKDIR /app
EXPOSE 8080
ENTRYPOINT ["/app/server"]
