# syntax=docker/dockerfile:1

# ---------- 前端构建：生成 Next.js standalone 产物 ----------
FROM node:20-alpine AS frontend-builder
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY frontend/ ./
RUN npm run build

# ---------- 后端构建 ----------
FROM golang:1.23-alpine AS backend-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . ./
# -trimpath + -s -w 减小二进制体积；CGO_ENABLED=0 保证跨架构静态编译
RUN --mount=type=cache,target=/go/pkg/mod CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/nvidia-api-gateway ./main.go

# ---------- 运行时：精简镜像，只带 standalone 运行产物 ----------
FROM node:20-alpine
WORKDIR /app
ENV NODE_ENV=production \
    PORT=18080 \
    BACKEND_PORT=18080 \
    FRONTEND_PORT=14000 \
    HOSTNAME=0.0.0.0 \
    API_BASE_URL=http://127.0.0.1:18080 \
    GATEWAY_DATA_DIR=/app/var/data
RUN apk add --no-cache tini ca-certificates curl
COPY --from=backend-builder /out/nvidia-api-gateway /app/nvidia-api-gateway
# Next.js standalone：自包含运行依赖，省掉整棵 node_modules，大幅减小镜像体积
COPY --from=frontend-builder /app/frontend/.next/standalone/ /app/frontend/.next/standalone/
COPY --from=frontend-builder /app/frontend/.next/static /app/frontend/.next/standalone/.next/static
COPY --from=frontend-builder /app/frontend/public /app/frontend/.next/standalone/public
COPY docker/entrypoint.sh /app/docker/entrypoint.sh
RUN sed -i 's/\r$//' /app/docker/entrypoint.sh \
    && mkdir -p /app/var/data \
    && chmod +x /app/nvidia-api-gateway /app/docker/entrypoint.sh
EXPOSE 18080 14000
VOLUME ["/app/var/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD curl -fsS http://127.0.0.1:${BACKEND_PORT}/health >/dev/null || exit 1
ENTRYPOINT ["/sbin/tini", "--"]
CMD ["/app/docker/entrypoint.sh"]
