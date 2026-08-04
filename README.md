# NVIDIA API Gateway

基于 Go + Next.js + Xray 的 NVIDIA API 网关，提供多账号密钥管理、代理池、流式兼容、用量统计等功能。

## 架构

- **后端**：Go（Fiber），网关核心、密钥管理、代理池、调度
- **前端**：Next.js 管理面板
- **Xray**：内置代理核心，启动时按平台自动就绪

## 自动构建

推送到 `main` 分支（或手动触发 `Build & Push Docker Image (arm64)` 工作流）后，GitHub Actions 会自动构建 **arm64** 架构镜像并推送到 GHCR：

```
ghcr.io/zengqt1125/nvi:latest
ghcr.io/zengqt1125/nvi:<commit-sha>
```

> 敏感数据（`data/`、`.env`、密钥文件）已通过 `.gitignore` / `.dockerignore` 排除，不会进入仓库或镜像。

## VPS 部署（arm64）

在 VPS 上只需三步：

```bash
# 1. 克隆编排文件
git clone https://github.com/ZengQT1125/nvi.git
cd nvi

# 2. 创建配置（按需修改密钥与 Token）
cp .env.example .env
nano .env

# 3. 拉取镜像并启动
docker compose pull
docker compose up -d
```

- 后端端口：`18080`（默认，可用 `BACKEND_PORT` 修改）
- 前端端口：`14000`（默认，可用 `FRONTEND_PORT` 修改）
- 数据持久化在 Docker 卷 `gateway_data` 中

## 更新版本

```bash
git pull            # 拉取最新编排
docker compose pull # 拉取最新镜像
docker compose up -d
```