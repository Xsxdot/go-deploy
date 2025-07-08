#!/bin/bash

# 自动构建release脚本
# 用法: ./build.sh [version]

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 打印带颜色的消息
print_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# 检查必要的工具
check_dependencies() {
    print_info "检查依赖工具..."
    
    if ! command -v go &> /dev/null; then
        print_error "Go 未安装或不在PATH中"
        exit 1
    fi
    
    if ! command -v git &> /dev/null; then
        print_error "Git 未安装或不在PATH中"
        exit 1
    fi
    
    print_success "依赖检查通过"
}

# 获取版本号
get_version() {
    if [ -n "$1" ]; then
        VERSION=$1
    else
        # 尝试从git tag获取最新版本
        VERSION=$(git describe --tags --abbrev=0 2>/dev/null || echo "")
        if [ -z "$VERSION" ]; then
            print_warning "未找到git tag，请输入版本号:"
            read -p "版本号 (例如: v1.0.0): " VERSION
        fi
    fi
    
    # 确保版本号以v开头
    if [[ ! $VERSION =~ ^v[0-9]+\.[0-9]+\.[0-9]+.*$ ]]; then
        print_error "版本号格式不正确，应该是 v1.0.0 格式"
        exit 1
    fi
    
    print_info "构建版本: $VERSION"
}

# 清理旧的构建文件
cleanup() {
    print_info "清理旧的构建文件..."
    rm -rf dist/
    mkdir -p dist/
    print_success "清理完成"
}

# 构建二进制文件
build_binary() {
    local os=$1
    local arch=$2
    local output_name=$3
    
    print_info "构建 ${os}/${arch}..."
    
    # 设置输出文件名
    if [ "$os" = "windows" ]; then
        output_name="${output_name}.exe"
    fi
    
    # 构建
    env GOOS=$os GOARCH=$arch go build -ldflags="-s -w -X main.version=$VERSION" -o "dist/${output_name}" .
    
    if [ $? -eq 0 ]; then
        print_success "构建成功: ${os}/${arch}"
    else
        print_error "构建失败: ${os}/${arch}"
        exit 1
    fi
}

# 创建发布包
create_package() {
    local os=$1
    local arch=$2
    local binary_name=$3
    
    print_info "创建 ${os}/${arch} 发布包..."
    
    # 创建临时目录
    pkg_dir="dist/package-${os}-${arch}"
    final_dir="deploy-${os}-${arch}"
    mkdir -p "$pkg_dir/$final_dir"
    
    # 复制二进制文件
    if [ "$os" = "windows" ]; then
        cp "dist/${binary_name}.exe" "$pkg_dir/$final_dir/"
        # 重命名为统一的可执行文件名
        mv "$pkg_dir/$final_dir/${binary_name}.exe" "$pkg_dir/$final_dir/deploy.exe"
    else
        cp "dist/${binary_name}" "$pkg_dir/$final_dir/"
        # 重命名为统一的可执行文件名
        mv "$pkg_dir/$final_dir/${binary_name}" "$pkg_dir/$final_dir/deploy"
    fi
    
    # 复制配置文件和文档（只复制示例文件）
    cp -r conf/ "$pkg_dir/$final_dir/"
    cp -r services/ "$pkg_dir/$final_dir/"
    cp README.md "$pkg_dir/$final_dir/"
    cp QUICK_START.md "$pkg_dir/$final_dir/"
    cp LICENSE "$pkg_dir/$final_dir/"
    
    # 创建压缩包
    cd dist/
    if [ "$os" = "windows" ]; then
        zip -r "deploy-${VERSION}-${os}-${arch}.zip" "package-${os}-${arch}/$final_dir/"
    else
        tar -czf "deploy-${VERSION}-${os}-${arch}.tar.gz" "package-${os}-${arch}/$final_dir/"
    fi
    cd ..
    
    # 清理临时目录
    rm -rf "$pkg_dir"
    
    print_success "发布包创建完成: deploy-${VERSION}-${os}-${arch}"
}

# 生成校验和
generate_checksums() {
    print_info "生成校验和文件..."
    cd dist/
    if command -v sha256sum &> /dev/null; then
        sha256sum *.tar.gz *.zip 2>/dev/null > checksums.txt || true
    elif command -v shasum &> /dev/null; then
        shasum -a 256 *.tar.gz *.zip 2>/dev/null > checksums.txt || true
    else
        print_warning "无法生成校验和文件，sha256sum 或 shasum 命令不可用"
    fi
    cd ..
    
    if [ -f "dist/checksums.txt" ]; then
        print_success "校验和文件生成完成"
    fi
}

# 生成release notes
generate_release_notes() {
    print_info "生成release notes..."
    
    # 获取上一个版本
    PREV_VERSION=$(git describe --tags --abbrev=0 HEAD^ 2>/dev/null || echo "")
    
    cat > "dist/release-notes.md" << EOF
# Release $VERSION

## 🚀 新增功能

<!-- 在此添加新增功能说明 -->

## 🐛 Bug修复

<!-- 在此添加Bug修复说明 -->

## 📦 下载

选择适合你系统的版本下载：

EOF

    # 添加下载链接
    for file in dist/*.tar.gz dist/*.zip; do
        if [ -f "$file" ]; then
            filename=$(basename "$file")
            echo "- [$filename](https://github.com/xsxdot/go-deploy/releases/download/$VERSION/$filename)" >> "dist/release-notes.md"
        fi
    done
    
    cat >> "dist/release-notes.md" << EOF

## 🔧 安装方法

### Linux/macOS
\`\`\`bash
# 下载并解压
wget https://github.com/xsxdot/go-deploy/releases/download/$VERSION/deploy-$VERSION-linux-amd64.tar.gz
tar -xzf deploy-$VERSION-linux-amd64.tar.gz
cd deploy-linux-amd64

# 配置并运行
cp conf/conf.yaml.example conf/conf.yaml
cp services/factory.yaml.example services/my-app.yaml
# 编辑配置文件...
./deploy
\`\`\`

### Windows
1. 下载 \`deploy-$VERSION-windows-amd64.zip\`
2. 解压到目标目录
3. 复制并编辑配置文件
4. 双击运行 \`deploy.exe\`

## 📋 校验和

\`\`\`
EOF

    if [ -f "dist/checksums.txt" ]; then
        cat "dist/checksums.txt" >> "dist/release-notes.md"
    fi
    
    echo '```' >> "dist/release-notes.md"
    
    # 如果有上一个版本，添加变更日志
    if [ -n "$PREV_VERSION" ]; then
        echo "" >> "dist/release-notes.md"
        echo "## 📝 完整变更日志" >> "dist/release-notes.md"
        echo "" >> "dist/release-notes.md"
        echo "**完整变更**: https://github.com/xsxdot/go-deploy/compare/$PREV_VERSION...$VERSION" >> "dist/release-notes.md"
    fi
    
    print_success "Release notes生成完成"
}

# 主函数
main() {
    print_info "开始构建 go-deploy release..."
    
    # 检查依赖
    check_dependencies
    
    # 获取版本号
    get_version "$1"
    
    # 清理
    cleanup
    
    # 支持的平台
    platforms=(
        "linux/amd64"
        "linux/arm64"
        "darwin/amd64"
        "darwin/arm64"
        "windows/amd64"
        "windows/arm64"
    )
    
    # 构建所有平台
    for platform in "${platforms[@]}"; do
        os=$(echo $platform | cut -d'/' -f1)
        arch=$(echo $platform | cut -d'/' -f2)
        binary_name="deploy-${os}-${arch}"
        
        build_binary "$os" "$arch" "$binary_name"
        create_package "$os" "$arch" "$binary_name"
    done
    
    # 生成校验和
    generate_checksums
    
    # 生成release notes
    generate_release_notes
    
    print_success "🎉 构建完成！"
    print_info "构建文件位于 dist/ 目录"
    print_info "Release notes: dist/release-notes.md"
    
    # 显示构建结果
    echo
    print_info "构建文件列表:"
    ls -la dist/
    
    echo
    print_info "下一步操作:"
    echo "1. 检查 dist/release-notes.md 并完善发布说明"
    echo "2. 创建git tag: git tag $VERSION && git push origin $VERSION"
    echo "3. 在GitHub上创建release并上传dist/目录中的文件"
    echo "4. 或者使用 GitHub CLI: gh release create $VERSION dist/* --title \"Release $VERSION\" --notes-file dist/release-notes.md"
}

# 运行主函数
main "$@" 