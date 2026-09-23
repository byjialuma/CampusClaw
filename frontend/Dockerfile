# 构建上下文为仓库根目录（见 docker-compose.yml）
FROM node:20-alpine AS build
WORKDIR /app
COPY frontend/package.json ./package.json
RUN npm install --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

FROM nginx:1.27-alpine
COPY deploy/nginx/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
