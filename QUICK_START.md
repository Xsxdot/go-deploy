# 快速入门指南

本指南将帮助你快速上手使用自动化部署工具。

## 🚀 5分钟快速开始

### 1. 准备工作

确保你有以下环境：
- 一台或多台Linux服务器（作为部署目标）
- 服务器已配置SSH密钥登录
- 本地已安装Go环境（用于编译）或直接使用预编译的二进制文件

### 2. 下载和配置

```bash
# 下载项目
git clone https://github.com/xsxdot/go-deploy.git
cd go-deploy

# 复制配置文件模板
cp conf/conf.yaml.example conf/conf.yaml
cp services/factory.yaml.example services/my-app.yaml

# 编辑配置文件
vim conf/conf.yaml
vim services/my-app.yaml
```

### 3. 配置说明

#### 主配置文件 `conf/conf.yaml`

```yaml
# 修改为你的实际服务器IP
servers:
  - host: 192.168.1.101  # 替换为你的测试服务器
    env: test
  - host: 192.168.1.102  # 替换为你的生产服务器
    env: prod
```

#### 服务配置文件 `services/my-app.yaml`

```yaml
# 修改服务名称
serviceName: my-app

# 修改域名
testEnv:
  domain: my-app-test.yourdomain.com  # 替换为你的域名

# 修改项目路径
frontendWorkDir: /path/to/your/frontend/project  # 替换为你的前端项目路径
backendWorkDir: /path/to/your/backend/project    # 替换为你的后端项目路径
```

### 4. 编译和运行

```bash
# 编译
go build -o deploy .

# 运行
./deploy
```

### 5. 第一次部署

1. 选择你刚才配置的服务（如 `my-app`）
2. 选择 `部署`
3. 选择 `测试环境`
4. 选择目标服务器
5. 选择构建类型（前端/后端/全部）
6. 输入版本号（如 `1.0.0`）
7. 输入部署描述（如 `初始部署`）

## 📋 配置检查清单

部署前请确保：

- [ ] 服务器SSH密钥已配置
- [ ] 服务器有足够的磁盘空间
- [ ] 防火墙已开放相应端口
- [ ] 如使用SSL，证书文件已上传到服务器
- [ ] 如使用Nginx，已安装并配置基本设置
- [ ] 项目代码已提交到本地仓库

## 🔧 常见问题

### Q: SSH连接失败
A: 确保服务器已配置SSH密钥登录，并且本地private key已添加到ssh-agent

### Q: 构建失败
A: 检查项目路径是否正确，依赖是否已安装

### Q: 服务启动失败
A: 检查服务配置文件中的启动命令是否正确

### Q: 健康检查失败
A: 确保健康检查URL可访问，或者在配置中注释掉healthUrl

## 📚 高级功能

### 版本回滚
```bash
./deploy
# 选择服务 -> 回滚 -> 选择版本
```

### 服务管理
```bash
./deploy
# 选择服务 -> 启动/停止/重启
```

### 批量部署
选择多个服务器进行同时部署

### 自动化配置
- 自动创建Nginx配置
- 自动创建DNS记录
- 自动创建systemd服务文件

## 🎯 最佳实践

1. **版本管理**：使用语义化版本号（如 1.0.0, 1.1.0）
2. **环境隔离**：严格区分测试环境和生产环境
3. **备份策略**：定期备份重要数据
4. **日志监控**：关注部署日志，及时发现问题
5. **健康检查**：为应用配置健康检查端点

## 🔗 相关链接

- [完整文档](README.md)
- [配置文件参考](services/factory.yaml.example)
- [故障排除指南](TROUBLESHOOTING.md)

---

如果遇到问题，请查看完整的[README文档](README.md)或提交Issue。 