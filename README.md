# NVIDIA API Gateway

基于 Go + Next.js 的 NVIDIA API 网关，提供多账号密钥管理、流式兼容、用量统计等功能。出站流量统一走**系统默认出口**（不内置任何代理节点/Xray），适合在系统层面已配置代理的环境直接部署。

## 架构

- **后端**：Go（Fiber），网关核心、密钥管理、调度、协议转换（OpenAI / Claude / Gemini / Responses / Embeddings）
- **前端**：Next.js 管理面板
- **网络出口**：统一使用系统默认出口（继承环境变量 `HTTP_PROXY` / `HTTPS_PROXY` 或直连），不再内置 Xray / 代理池 / 代理节点管理

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
- 版本号：`/health` 与 `/admin/version` 接口返回当前构建版本（管理后台侧边栏同步显示）

## 更新版本

```bash
git pull            # 拉取最新编排
docker compose pull # 拉取最新镜像
docker compose up -d
```
