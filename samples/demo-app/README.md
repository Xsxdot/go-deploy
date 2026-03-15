# Demo App: Vue + Go

Vue 3 前端 + Go 后端示例应用，支持同机部署（Go 转发静态）与前后端分离部署。

## 应用说明

- **前端**: Vue 3 + Vite，单页应用，调用 `/api/health` 展示状态
- **后端**: Go HTTP 服务
  - `/api/health` 返回 `{"status":"ok"}`
  - 可选 `-static-dir` 从根路径 `/` 转发前端静态文件（同机模式）
  - `-port` 指定监听端口，默认 8080

## 本地运行

```bash
# 前端
cd frontend && npm install && npm run dev

# 后端（独立运行，不托管前端）
cd backend && go run . -port 8080

# 同机模式（后端托管前端）
cd frontend && npm run build
cd ../backend && go run . -static-dir ../frontend/dist -port 8080
```

## 构建

```bash
# 前端
cd frontend && npm ci && npm run build

# 后端（需先构建前端并复制 dist 到 backend/static）
cp -r frontend/dist backend/static
cd backend && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o demo-server .

# Docker（多阶段构建，包含前后端）
cd samples/demo-app && docker build -t demo-app:v1.0.0 .
```

## DeployFlow 流水线使用

工作目录为项目根目录 `nova_depoly/`。

### 1. 加载基础设施

```bash
deploy infra load -f resources/infra.yaml
```

请先修改 `resources/infra.yaml` 中的 `addr`、`lanAddr`、`auth.keyPath` 等为实际环境值。

### 2. 注册项目并部署

**二进制同机部署**（前后端同机，systemd_check → http_check）:

```bash
deploy project load -n demo-app-same -f samples/pipelines/pipeline-binary-same.yaml -d .
deploy apply demo-app-same -m "首次部署"
```

**二进制前后端分离**（前端与后端分别部署到不同主机）:

```bash
deploy project load -n demo-app-separate -f samples/pipelines/pipeline-binary-separate.yaml -d .
deploy apply demo-app-separate -m "分离部署"
```

**Docker 部署**（docker_check → http_check）:

```bash
deploy project load -n demo-app-docker -f samples/pipelines/pipeline-docker.yaml -d .
deploy apply demo-app-docker -m "Docker 部署"
```

### 3. 检查顺序

所有流水线统一：**先 systemd_check 或 docker_check，再 http_check**。
