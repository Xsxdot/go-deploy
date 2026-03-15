# DeployFlow E2E 测试命令列表
# 使用方法：在项目根目录逐条复制执行。不包含自动化逻辑，每条命令独立运行。
# 注意：需先执行 go build -o deploy ./cmd/deploy

# =============================================================================
# 1. 前置准备
# =============================================================================

# 在项目根目录执行以下命令（若在 samples/ 等子目录，请先 cd 到项目根）

# 构建 deploy 可执行文件
go build -o deploy ./cmd/deploy

# 可选：清理旧数据库，实现完全重新初始化
rm -f ~/.deploy/deploy.db

# =============================================================================
# 2. 环境初始化（infra load & project load）
# =============================================================================

# 首次加载全局基础设施（若已存在则用 infra reload）
./deploy infra load -f resources/infra.yaml

# 注册 5 个测试流水线（workspace 必须为 samples，否则 ../templates/ 和 demo-app/ 路径无法解析）
./deploy project load -n demo-app-binary-same -f samples/pipelines/pipeline-binary-same.yaml -d samples
./deploy project load -n demo-app-separate -f samples/pipelines/pipeline-binary-separate.yaml -d samples
./deploy project load -n demo-app-docker-compose -f samples/pipelines/pipeline-docker-compose.yaml -d samples
./deploy project load -n demo-app-docker-container -f samples/pipelines/pipeline-docker-container.yaml -d samples
./deploy project load -n demo-app-docker-offline -f samples/pipelines/pipeline-docker.yaml -d samples

# 验证项目列表
./deploy project list

# 查看单项目详情
./deploy project detail demo-app-binary-same

# =============================================================================
# 3. 全量部署（apply）
# =============================================================================
# demo-app-binary-same 有 prod/test 多环境：先发 test，验证后再晋升到 prod；其余 4 个单环境用 -e default

# 3.1 先发版到 test 环境（构建 + 部署到 compute-test/nginx-test）
./deploy apply demo-app-binary-same -m "首次发版 同机版" -e test -v v1.0.0

# 3.2 【手动】验证 test 环境部署正常（如访问 test 域名、健康检查等）

# 3.3 晋升到 prod：跳过 Build，复用 test 的产物直接部署到 compute/nginx
./deploy apply demo-app-binary-same -m "晋升到 prod" -e prod --from-env test --from-version v1.0.0
./deploy apply demo-app-separate -m "首次发版 分离版" -e default -v v1.0.0
./deploy apply demo-app-docker-compose -m "首次发版 Compose版" -e default -v v1.0.0
./deploy apply demo-app-docker-container -m "首次发版 容器版" -e default -v v1.0.0
./deploy apply demo-app-docker-offline -m "首次发版 离线版" -e default -v v1.0.0

# =============================================================================
# 4. 历史记录查询（history）
# =============================================================================

# 列出 demo-app-binary-same 的部署历史（prod 环境）
./deploy history demo-app-binary-same -e prod

# 查看单条历史详情
./deploy history demo-app-binary-same v1.0.0 -e prod

# 列出单环境项目的历史（如 demo-app-separate）
./deploy history demo-app-separate -e default

# =============================================================================
# 5. 临时 Pipeline 覆盖测试（ad-hoc apply -c）
# =============================================================================

# 创建临时 override YAML 文件
cat <<'EOF' > /tmp/deploy-temp.yaml
pipeline:
  steps:
    - name: Adhoc Test
      type: local_command
      with:
        cmd: "echo 'Ad-hoc execution successful!'"
EOF

# 指定 project 的增量/覆盖任务
./deploy apply demo-app-binary-same -c /tmp/deploy-temp.yaml

# 纯一次性任务（不指定 project）
./deploy apply -c /tmp/deploy-temp.yaml

# 组合技：提取历史快照跑临时任务（--from-env 须与 apply 时一致）
./deploy apply demo-app-binary-same -c /tmp/deploy-temp.yaml --from-env prod --from-version v1.0.0

# =============================================================================
# 6. 回滚与重载（仅针对 demo-app-binary-same，因其有 prod/test）
# =============================================================================

# 发布 v2.0.0 准备回滚
./deploy apply demo-app-binary-same -e prod -v v2.0.0 -m "V2 发布"

# 删除 v2.0.0 的部署记录（-e 须与 apply 一致）
./deploy history delete demo-app-binary-same -v v2.0.0 -e prod

# 回滚到 v1.0.0
./deploy rollback demo-app-binary-same -v v1.0.0 -e prod

# 配置覆盖更新
./deploy infra reload -f resources/infra.yaml
./deploy project reload -n demo-app-binary-same -f samples/pipelines/pipeline-binary-same.yaml -d samples

# =============================================================================
# 7. 清理（destroy & project delete）
# =============================================================================

# 卸载 4 个单环境项目的远端资源（用 -e default）
./deploy destroy demo-app-separate -e default --full
./deploy destroy demo-app-docker-compose -e default --full
./deploy destroy demo-app-docker-container -e default --full
./deploy destroy demo-app-docker-offline -e default --full

# 卸载 demo-app-binary-same 并 purge（从 store 删除项目记录）
./deploy destroy demo-app-binary-same -e prod --purge

# 验证其它未 purge 的项目依然存在
./deploy project list

# 手动清理剩余 4 个项目的注册数据（-y 跳过确认）
./deploy project delete demo-app-separate -y
./deploy project delete demo-app-docker-compose -y
./deploy project delete demo-app-docker-container -y
./deploy project delete demo-app-docker-offline -y
