# 构建说明

本文档说明如何构建和发布 go-deploy 项目。

## 🏗️ 本地构建

### 1. 环境要求

- Go 1.24+
- Git
- bash shell (Linux/macOS)

### 2. 手动构建

```bash
# 编译当前平台
go build -o deploy .

# 交叉编译（例如Linux amd64）
GOOS=linux GOARCH=amd64 go build -o deploy-linux-amd64 .
```

### 3. 使用构建脚本

```bash
# 构建所有平台的release包
./build.sh v1.0.0

# 如果没有指定版本，脚本会提示输入
./build.sh
```

构建完成后，所有文件会在 `dist/` 目录中：

```
dist/
├── deploy-v1.0.0-linux-amd64.tar.gz
├── deploy-v1.0.0-linux-arm64.tar.gz
├── deploy-v1.0.0-darwin-amd64.tar.gz
├── deploy-v1.0.0-darwin-arm64.tar.gz
├── deploy-v1.0.0-windows-amd64.zip
├── deploy-v1.0.0-windows-arm64.zip
├── checksums.txt
└── release-notes.md
```

## 🚀 自动化发布

### 1. 使用GitHub Actions

项目已配置GitHub Actions，当推送tag时会自动构建和发布：

```bash
# 创建并推送tag
git tag v1.0.0
git push origin v1.0.0
```

### 2. 使用GitHub CLI

如果你有GitHub CLI，可以直接创建release：

```bash
# 本地构建
./build.sh v1.0.0

# 创建release
gh release create v1.0.0 dist/* \
  --title "Release v1.0.0" \
  --notes-file dist/release-notes.md
```

## 📋 构建检查清单

发布前请确保：

- [ ] 代码已提交到main分支
- [ ] 版本号符合语义化版本规范（如v1.0.0）
- [ ] 更新了CHANGELOG.md（如果有）
- [ ] 测试通过
- [ ] 文档已更新

## 🔧 构建选项

### 自定义构建

```bash
# 只构建Linux版本
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o deploy-linux-amd64 .

# 添加版本信息
VERSION=v1.0.0
go build -ldflags="-s -w -X main.version=$VERSION" -o deploy .
```

### 支持的平台

- Linux (amd64, arm64)
- macOS (amd64, arm64)
- Windows (amd64, arm64)

## 📝 版本命名规范

- 使用语义化版本：`v{major}.{minor}.{patch}`
- 预发布版本：`v1.0.0-alpha.1`
- 稳定版本：`v1.0.0`

## 🐛 故障排除

### 构建失败

1. 检查Go版本是否满足要求
2. 确保所有依赖都已下载：`go mod tidy`
3. 检查磁盘空间是否充足

### 发布失败

1. 检查GitHub token权限
2. 确保tag名称符合规范
3. 检查网络连接

## 📚 相关文档

- [快速入门](QUICK_START.md)
- [项目文档](README.md)
- [GitHub Actions文档](https://docs.github.com/en/actions) 