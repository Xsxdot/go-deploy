# 发布流程演示

本文档演示如何使用构建脚本和GitHub Actions发布新版本。

## 🎯 发布流程示例

### 1. 本地构建和测试

```bash
# 1. 确保代码是最新的
git pull origin main

# 2. 测试构建
go build -o deploy .
./deploy --version

# 3. 运行基本测试
# 确保程序能正常启动并显示菜单

# 4. 清理测试文件
rm -f deploy
```

### 2. 使用构建脚本

```bash
# 构建特定版本
./build.sh v1.0.0

# 检查构建结果
ls -la dist/
```

期望输出：
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

### 3. 验证构建包

```bash
# 解压测试（以Linux为例）
cd dist
tar -xzf deploy-v1.0.0-linux-amd64.tar.gz
cd deploy-linux-amd64

# 检查文件结构
ls -la
# 应该看到: deploy, conf/, services/, README.md, LICENSE等

# 测试版本信息
./deploy --version
# 应该显示: go-deploy v1.0.0

# 测试配置文件
ls conf/ services/
# 应该看到示例配置文件

cd ../..
```

### 4. 自动发布（推荐）

```bash
# 1. 提交所有更改
git add .
git commit -m "prepare for release v1.0.0"
git push origin main

# 2. 创建并推送标签
git tag v1.0.0
git push origin v1.0.0

# 3. 等待GitHub Actions完成构建
# 访问：https://github.com/xsxdot/go-deploy/actions

# 4. 检查release页面
# 访问：https://github.com/xsxdot/go-deploy/releases
```

### 5. 手动发布（GitHub CLI）

```bash
# 1. 本地构建
./build.sh v1.0.0

# 2. 编辑release notes
vim dist/release-notes.md

# 3. 创建GitHub release
gh release create v1.0.0 dist/* \
  --title "Release v1.0.0" \
  --notes-file dist/release-notes.md

# 4. 查看发布结果
gh release view v1.0.0
```

## 🔍 质量检查

### 构建验证

```bash
# 检查所有平台的二进制文件
for file in dist/deploy-*-*; do
    if [[ -f "$file" && ! "$file" =~ \.(tar\.gz|zip)$ ]]; then
        echo "检查: $file"
        file "$file"
    fi
done
```

### 版本一致性

```bash
# 检查版本号是否正确注入
for archive in dist/*.tar.gz; do
    echo "检查: $archive"
    tar -tzf "$archive" | head -5
done
```

### 校验和验证

```bash
# 验证校验和
cd dist/
if command -v sha256sum &> /dev/null; then
    sha256sum -c checksums.txt
else
    shasum -a 256 -c checksums.txt
fi
cd ..
```

## 📋 发布检查清单

发布前确保：

- [ ] 版本号符合语义化版本规范
- [ ] 代码已合并到main分支
- [ ] 所有测试通过
- [ ] 文档已更新
- [ ] 配置文件示例已脱敏
- [ ] 构建脚本测试通过
- [ ] 校验和文件生成正确
- [ ] Release notes已完善

## 🚨 常见问题

### 构建失败

```bash
# 检查Go版本
go version

# 清理并重新构建
rm -rf dist/
go mod tidy
./build.sh v1.0.0
```

### 版本冲突

```bash
# 删除错误的标签
git tag -d v1.0.0
git push origin :refs/tags/v1.0.0

# 重新创建标签
git tag v1.0.0
git push origin v1.0.0
```

### GitHub Actions失败

1. 检查`.github/workflows/release.yml`配置
2. 确认GitHub token权限
3. 检查仓库设置中的Actions权限

## 📈 版本计划

### 版本策略

- **主版本号**：重大功能变更或不兼容更改
- **次版本号**：新功能添加，向后兼容
- **修订号**：Bug修复和小改进

### 示例版本路线图

- `v1.0.0` - 初始发布版本
- `v1.1.0` - 添加新的部署策略
- `v1.1.1` - 修复配置文件解析问题
- `v2.0.0` - 重构配置格式（不兼容变更）

---

**提示**：首次发布建议使用`v1.0.0`作为起始版本，避免使用`v0.x.x`，因为这通常表示不稳定版本。 