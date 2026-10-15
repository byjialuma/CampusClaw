# 构建上下文为 mcp-server/ 目录本身（见 docker-compose.yml 的 context: ./mcp-server）。
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY . ./
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mcp-server .

FROM alpine:3.20
RUN adduser -D -u 10001 app
COPY --from=build /out/mcp-server /app/mcp-server
USER app
WORKDIR /app
EXPOSE 8090
ENTRYPOINT ["/app/mcp-server"]
