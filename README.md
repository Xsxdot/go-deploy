# 🚀 Nova Deploy (Deployflow)

> 一个基于 **DAG 流水线**的现代化、无 Agent 部署控制平面。专为传统物理机、虚拟机及 Docker 环境设计，提供丝滑的自动化部署、版本管理和秒级回滚体验。

Nova Deploy 采用配置与状态分离的架构，通过 SSH/SCP 与目标机通信，零侵入性。内置强大的状态持久化（SQLite），让你随时随地掌控所有环境的发布脉络。

---

## ✨ 核心优势 (Why Nova Deploy?)

- **🕸️ DAG 任务编排**：基于有向无环图（DAG）的执行引擎，天然支持任务的依赖解析与**高并发执行**，极大地缩短部署窗口。
- **🛡️ 状态持久化与秒级回滚**：每次部署自动生成全局唯一版本号（如 `v1.0.1`）并建立快照。无论何时发生故障，只需一条 `rollback` 命令即可精准恢复到任意历史状态。
- **🌍 全局基础设施 (Infra)**：将主机、跳板机（Bastion）、角色池（Roles）与具体流水线解耦。一次配置，多项目多环境共享，极具扩展性。
- **🧩 丰富的声明式插件**：内置涵盖命令执行、文件传输、`Systemd` 管理、`Nginx` 配置、`Docker / Compose`、`DNS` 解析、软链接零停机切换等 15+ 种实用插件，告别繁琐的 Shell 脚本。
- **📦 模块化与模板复用**：支持 `include` 引入外部步骤模板，轻松实现**金丝雀发布 (Canary Release)**、蓝绿部署，以及跨环境的流程复用。
- **🖥️ 绝佳的终端体验 (TUI)**：基于 Bubbletea 构建的实时交互式终端界面，日志聚合与任务进度一目了然。
- **🤝 无缝对接 CI/CD**：支持 `--dry-run` 预检、非交互式人工审批（通过环境变量）、临时 Pipeline 覆盖（`-c`），极易集成至 GitHub Actions、GitLab CI 或 Jenkins。

## 🎯 典型使用场景 (Use Cases)

1. **传统架构应用的平滑发布**：Go 编译二进制、前端静态资源、Java Jar 包等，搭配 `transfer` + `symlink_switch` + `systemd_service` 实现**零停机更新**。
2. **轻量级容器编排**：无需引入笨重的 Kubernetes，通过内置的 `docker_container` 和 `docker_compose` 插件，在多台云主机上远程拉起和管理容器服务。
3. **多环境晋升 (Environment Promotion)**：使用 `--from-env staging --from-version v1.0.0` 直接提取测试环境的构建产物与配置发布到生产环境，避免重复构建导致的不可靠性。
4. **复杂的多节点/多层级集群部署**：通过配置不同的 `roles` (如 `gateway`, `compute`, `db`)，并在 DAG 中定义节点间的依赖关系（先更新数据库 -> 再更新后端 -> 最后重启网关）。
5. **自动化运维与一次性任务**：使用 `deploy apply -c one-time-task.yaml` 执行数据迁移、服务器批量初始化等一次性脚本，不污染部署历史。

---

## 🚀 快速开始

### 1. 编译与安装

```bash
# 需要 Go 1.26+ 环境
go build -o deploy ./cmd/deploy
sudo mv deploy /usr/local/bin/
```

### 2. 核心工作流演示

**第一步：加载全局基础设施 (Infra)**
定义你的服务器和角色（如 `samples/examples/infra.yaml`）。
```bash
deploy infra load -f infra.yaml
# 如果有修改，使用 reload 覆盖
deploy infra reload -f infra.yaml
```

**第二步：注册项目流水线 (Project)**
定义应用怎么构建和发布（如 `samples/examples/pipeline.yaml`）。
```bash
deploy project load -n my-app -f pipeline.yaml
```

**第三步：一键部署 (Apply)**
部署时，系统会自动分配新版本号（如 v1.0.0）。
```bash
deploy apply my-app -m "feat: 发布全新的首页UI" -e production
```

**第四步：一键回滚 (Rollback)**
如果发现新版本有 Bug，可以立即回滚。
```bash
deploy rollback my-app -v v1.0.0 -e production
```

**第五步：查看部署历史**
```bash
deploy history my-app
```

---

## 🛠️ CLI 命令指南

### Infra（基础设施管理）
| 命令 | 说明 |
|------|------|
| `deploy infra load -f <file>` | 首次加载全局基础设施 |
| `deploy infra reload -f <file>` | 覆盖更新全局基础设施 |

### Project（项目流管理）
| 命令 | 说明 |
|------|------|
| `deploy project load -n <name> -f <file>` | 注册项目流水线 |
| `deploy project reload -n <name> -f <file>` | 覆盖更新项目 |
| `deploy project list` | 查看所有注册项目 |
| `deploy project detail <name>` | 显示项目详情 |

### 部署与回滚
| 命令 | 说明 |
|------|------|
| `deploy apply [project] [options]` | 执行部署。常用参数：<br>`-m`: 发布说明 <br>`-e`: 目标环境 <br>`-v`: 指定版本 <br>`-c`: 指定临时流水线覆盖 <br>`--dry-run`: 预检不执行 |
| `deploy rollback <project> -v <version>` | 回滚到指定版本 |
| `deploy destroy <project> [options]` | 卸载项目应用，支持 `--full` 彻底清理 DNS 与全部版本目录 |

*(注：临时 Pipeline `apply -c tasks.yaml` 可用于在不持久化版本记录的前提下，运行一次性运维任务。)*

---

## 🔌 强大的插件生态

Nova Deploy 采用可扩展的插件机制，在流水线中通过 `type` 指定：

| 插件类别 | 插件名称 (`type`) | 功能描述 |
|---------|------------------|----------|
| **命令与脚本** | `local_command` | 本地环境执行 Shell，常用于代码拉取、打包构建 |
| | `remote_command` | 通过 SSH 在目标资源池并行执行 Shell 命令 |
| **文件与传输** | `transfer` | 高并发分发本地产物至远端目录，支持压缩/解压与权限保持 |
| | `archive` | 本地一键归档 (`.tar.gz`/`.zip`)，自动导出变量供下游使用 |
| **状态与校验** | `http_check` | 轮询检测指定 URL 的存活状态及返回码 |
| | `systemd_check` | 探针检测 Systemd 服务的 `active` 状态 |
| | `docker_check` | 探针检测特定 Docker 容器的运行状态 |
| **服务管理** | `systemd_service` | 渲染 Systemd `.service` 模板，下发并自动 `daemon-reload/enable/restart` |
| | `nginx_config` | 基于 Go Template 的 Nginx 配置下发与重载，支持自动解析 Upstream IP |
| | `dns_record` | Cloudflare / Aliyun DNS 记录自动更新，实现流量调度 |
| **容器部署** | `docker_container` | 远端拉起/更新单体 Docker 容器，管理挂载与端口 |
| | `docker_compose` | 远端执行 Docker Compose 项目编排，自动注入 `.env` 环境 |
| **版本控制** | `symlink_switch` | 软链接原子切换 (`current` 指向新 `version`) |
| | `backup_state` | 发布前快照目标目录，支持失败后自动容错回滚 |
| | `release_pruner` | 自动清理过期版本目录，释放磁盘空间 |
| **控制流** | `manual_approval` | 发起人工确认暂停，适用于关键节点的阻断审批 |
| | `include` | 动态加载外部 YAML 流水线，实现模板的高级复用 |

---

## 📁 系统数据与配置持久化

所有的 Infra 定义、注册的项目 Pipeline 以及包含完整变量的**发布快照记录**，都持久化存储在你的本地（默认路径：`~/.deploy/deploy.db`）。
这使得 Nova Deploy **完全无状态依赖**于特定的工作目录。哪怕你换一个文件夹，依然可以通过 `deploy apply my-app` 部署最新的代码，或随时触发回滚。

## 💡 深入了解与示例

想要学习如何编写完整的 DAG 流水线、配置多级跳板机或使用高级的模板渲染特性？请查阅 `samples` 目录下的实战配置：

- [完整功能展示 (Full Pipeline)](./samples/examples/pipeline-full-example.yaml)
- [基础设施定义示例 (Infra)](./samples/examples/infra.yaml)
- [容器编排部署示例 (Docker Compose)](./samples/pipelines/pipeline-docker-compose.yaml)
- [金丝雀发布模板 (Canary Release)](./samples/testdata/templates/canary-release.yaml)

---
*Built with [Go](https://golang.org) & [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) & [Bubbletea](https://github.com/charmbracelet/bubbletea).*
