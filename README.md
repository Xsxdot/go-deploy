# 自动化部署工具 (Deploy Tool)

一个基于Go语言开发的自动化部署工具，支持前后端应用的快速部署、服务管理、回滚等功能。

## 🚀 功能特性

- **多应用类型支持**：支持纯前端应用和前后端混合应用部署
- **多环境管理**：支持测试环境和生产环境的独立配置
- **多服务器部署**：支持一键部署到多台服务器
- **版本回滚**：支持快速回滚到任意历史版本
- **服务管理**：支持服务的启动、停止、重启操作
- **自动化配置**：自动创建和管理Nginx配置文件
- **DNS管理**：自动创建和更新DNS记录（支持阿里云DNS）
- **健康检查**：部署后自动进行健康检查
- **日志记录**：详细的部署日志和进度显示

## 📋 系统要求

- Go 1.24+
- Linux/macOS 系统
- 目标服务器需要支持SSH连接和systemd服务管理

## 🛠 安装使用

> 💡 **快速开始**：查看 [快速入门指南](QUICK_START.md) 了解5分钟快速上手方法

### 1. 下载构建包

从[Releases](https://github.com/xsxdot/go-deploy/releases)页面下载最新的构建包。

### 2. 解压并运行

```bash
# 解压构建包
tar -xzf deploy-linux-amd64.tar.gz

# 进入目录
cd deploy

# 直接运行
./deploy
```

### 3. 首次运行配置

首次运行前需要配置以下文件：

1. **主配置文件** `conf/conf.yaml` （参考 `conf/conf.yaml.example`）
2. **服务配置文件** `services/你的服务名.yaml` （参考 `services/factory.yaml.example` 和 `services/machine.yaml.example`）

## ⚙️ 配置文件示例

### 主配置文件 (conf/conf.yaml)

> 参考示例文件：`conf/conf.yaml.example`

```yaml
# Nginx服务器列表
nginxServers:
  - 192.168.1.100

# Nginx配置文件目录
nginxConfDir: /etc/nginx/conf.d

# 服务器列表配置
servers:
  - host: 192.168.1.101
    env: test
  - host: 192.168.1.102
    env: prod
  - host: 192.168.1.103
    env: prod
  - host: 192.168.1.104
    env: prod
  - host: 192.168.1.105
    env: prod

# 阿里云DNS配置 (可选，如不使用DNS功能可以留空)
dnsKey: "your-dns-access-key"
dnsSecret: "your-dns-access-secret"
```

### 前后端混合应用配置 (services/backend-app.yaml)

> 参考示例文件：`services/factory.yaml.example`

```yaml
# 服务名称
serviceName: backend-app

# SSL配置
ssl: true
sslKeyPath: /opt/cert/your-domain.key
sslCertPath: /opt/cert/your-domain.crt

# 服务启动命令
startCommand: ${installPath}/backend-app -env=${env} -config=${installPath}/config/app.yaml

# 测试环境配置
testEnv:
  domain: backend-test.yourdomain.com
  port: 8080
  installPath: /opt/backend-test
  frontendBuildCommands:
    - "npm install"
    - "npm run build"
  backendBuildCommands:
    - "CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -installsuffix cgo -o backend-app ."
  healthUrl: http://${host}:8080/health
  servers:
    - 192.168.1.101

# 生产环境配置
prodEnv:
  domain: backend.yourdomain.com
  port: 8080
  installPath: /opt/backend
  frontendBuildCommands:
    - "npm install"
    - "npm run build"
  backendBuildCommands:
    - "CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -installsuffix cgo -o backend-app ."
  healthUrl: http://${host}:8080/health
  servers:
    - 192.168.1.102
    - 192.168.1.103
    - 192.168.1.104
    - 192.168.1.105

# 前端项目目录
frontendWorkDir: /path/to/your/frontend/project

# 后端项目目录
backendWorkDir: /path/to/your/backend/project

# 文件复制配置
copyFiles:
  - source: /path/to/your/frontend/project/dist
    target: ${workDir}/web
    mode: move
    type: front
  - source: /path/to/your/backend/project/backend-app
    target: ${workDir}/backend-app
    mode: move
    type: back
  - target: ${workDir}/config
    mode: mkdir
    type: back
  - source: /path/to/your/backend/project/config/${env}.yaml
    target: ${workDir}/config/app.yaml
    mode: copy
    type: back
```

### 纯前端应用配置 (services/frontend-app.yaml)

> 参考示例文件：`services/machine.yaml.example`

```yaml
# 服务名称
serviceName: frontend-app

# SSL配置
ssl: true
sslKeyPath: /opt/cert/your-domain.key
sslCertPath: /opt/cert/your-domain.crt

# 测试环境配置
testEnv:
  domain: frontend-test.yourdomain.com
  installPath: /opt/frontend-test
  frontendBuildCommands:
    - "npm install"
    - "npm run build:test"
  servers:
    - 192.168.1.101

# 生产环境配置
prodEnv:
  domain: frontend.yourdomain.com
  installPath: /opt/frontend
  frontendBuildCommands:
    - "npm install"
    - "npm run build"
  servers:
    - 192.168.1.102

# 前端项目目录
frontendWorkDir: /path/to/your/frontend/project

# 文件复制配置
copyFiles:
  - source: /path/to/your/frontend/project/dist/*
    target: ${workDir}/
    mode: move
    type: front
```

## 🎯 使用方法

### 1. 交互式部署

运行程序后，会出现交互式菜单：

```bash
./deploy
```

依次选择：
- 服务名称
- 操作类型（部署/回滚/启动/停止/重启/卸载等）
- 环境（测试/生产）
- 目标服务器
- 构建类型（前端/后端/全部）

### 2. 支持的操作类型

- **部署**：构建并部署新版本
- **回滚**：回滚到历史版本
- **启动**：启动服务
- **停止**：停止服务
- **重启**：重启服务
- **卸载**：完全卸载服务
- **重建Nginx**：重新生成Nginx配置
- **重建DNS**：重新创建DNS记录

### 3. 配置说明

#### 文件复制模式

- `move`：移动文件
- `copy`：复制文件
- `mkdir`：创建目录

#### 文件类型

- `front`：前端相关文件
- `back`：后端相关文件

#### 变量替换

配置文件中支持以下变量：
- `${workDir}`：工作目录
- `${env}`：环境标识（test/prod）
- `${installPath}`：安装路径
- `${host}`：服务器地址

## 📁 目录结构

```
deploy/
├── .github/                     # GitHub Actions配置
│   └── workflows/
│       └── release.yml         # 自动发布工作流
├── conf/                        # 主配置文件目录
│   └── conf.yaml.example       # 主配置文件示例
├── services/                    # 服务配置文件目录
│   ├── factory.yaml.example    # 前后端混合应用配置示例
│   └── machine.yaml.example    # 纯前端应用配置示例
├── deploy                       # 主程序文件
├── build.sh                     # 构建脚本
├── README.md                    # 项目说明文档
├── QUICK_START.md              # 快速入门指南
├── BUILD.md                     # 构建说明文档
├── RELEASE_DEMO.md             # 发布流程演示
├── LICENSE                      # 开源许可证
└── .gitignore                   # Git忽略文件
```

## 🔧 开发

### 编译项目

```bash
# 克隆项目
git clone https://github.com/xsxdot/go-deploy.git
cd go-deploy

# 安装依赖
go mod tidy

# 编译
go build -o deploy .

# 运行
./deploy
```

### 构建Release

```bash
# 使用构建脚本
./build.sh v1.0.0

# 或者推送tag自动构建
git tag v1.0.0
git push origin v1.0.0
```

详细构建说明请查看 [BUILD.md](BUILD.md)

发布流程演示请查看 [RELEASE_DEMO.md](RELEASE_DEMO.md)

### 依赖包

- `github.com/AlecAivazis/survey/v2` - 交互式命令行界面
- `golang.org/x/crypto` - SSH连接支持
- `gopkg.in/yaml.v3` - YAML配置文件解析
- `go.uber.org/zap` - 日志记录

## 🤝 贡献

欢迎提交Issue和Pull Request来改进这个项目。

## 📄 许可证

本项目采用 MIT 许可证 - 查看 [LICENSE](LICENSE) 文件了解详情。

## 📞 支持

如果你遇到任何问题或有建议，请提交Issue或联系项目维护者。

---

**注意**：使用前请确保配置文件中的敏感信息（如密钥、域名等）已正确配置，并且目标服务器已正确配置SSH密钥认证。 