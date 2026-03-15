这是一份为你量身定制的**《Nova Deploy (Deployflow) 交互式向导 (TUI Wizard) 需求文档》**。这份文档采用了大厂标准的产品需求文档（PRD）结构，融合了系统核心功能与现有的 CLI 契约，你可以直接把它贴到你的 Notion、飞书或者 GitHub Wiki 里作为开发蓝图。

---

# 📄 产品需求文档 (PRD)：Nova Deploy 交互式向导功能

## 1. 背景与业务目标 (Background & Objectives)

### 1.1 业务背景

Nova Deploy (Deployflow) 目前拥有极其强大的底层 DAG 引擎和纯命令行的调用契约（如 `deploy apply`、`deploy rollback` 等）。但在日常的高频发版、紧急回滚等人工操作场景中，工程师往往记不清冗长的参数（如特定环境的 `-e`，回滚时的准确版本号 `-v v1.0.5`）。为了提升开发人员和 SRE 的使用体验，我们计划引入基于 **Bubbletea / Huh** 的**交互式终端向导（TUI Wizard）**。

### 1.2 核心目标

* **极简心智负担**：用户只需在终端敲击 `deploy`，即可通过上下键和交互表单完成 90% 的日常操作（发版、回滚、配置加载）。
* **防呆与防错**：通过下拉列表代替手动输入（例如直接列出最近的 10 次历史版本供回滚选择，列出可用的环境），彻底杜绝拼写错误导致的生产事故。
* **渐进式披露**：根据用户选择的“动作”和“项目”，动态询问所需的参数，隐藏无关信息。
* **绝对向下兼容**：严格保持现有的纯命令行参数调用模式不变，绝不影响 CI/CD 流水线（如 GitHub Actions、GitLab CI）的无头（Headless）自动化执行。

---

## 2. 核心用户场景 (User Scenarios)

| 场景 | 用户行为 | 期望结果 |
| --- | --- | --- |
| **日常发版** | 开发人员敲击 `deploy`，选择 `Apply` 和目标项目。 | 系统询问部署说明 (Message) 和环境后，直接开始跑发版进度条。 |
| **紧急回滚** | 线上报警，SRE 敲击 `deploy`，选择 `Rollback` 和出问题项目。 | 系统立刻列出**最近 10 次成功的发版记录及时间**，SRE 选中上一个版本敲回车，秒级完成回滚。 |
| **临时运维** | 运维需要清理服务或迁移数据。选择 `Apply` -> `<临时任务>`。 | 系统打开文件选择器要求输入本地 YAML 路径（相当于 `-c`），不污染版本历史记录执行任务。 |
| **配置加载** | 初次使用或更新配置。选择 `Infra` 或 `Project` -> `Load/Reload`。 | 系统列出当前目录下的 `.yaml` 文件供选择，完成后自动持久化到 SQLite。 |
| **流水线调用** | CI 触发 `deploy apply my-app -v v2.0 -e production` | 系统检测到带了参数，静默跳过向导，直接进入无头执行模式。 |

---

## 3. 交互流程设计 (Interaction Flow)

整个向导采用树状分支逻辑。默认触发条件为：**用户在终端仅输入 `deploy`，未携带任何子命令或参数。**

### 步骤 1：动作选择 (Action Selection)

拦截 Root 默认的 Help 界面，渲染动作主菜单：

* **选项**：
  * 🚀 `Apply` (一键部署项目 / 运行临时任务)
  * ⏪ `Rollback` (秒级回滚到历史版本)
  * 📜 `History` (查看项目部署历史)
  * 🗑️ `Destroy` (卸载项目及远端资源)
  * 🌍 `Infra` (加载/更新全局基础设施)
  * 📦 `Project` (注册/更新项目流水线)

* *操作说明*：用户通过 `↑` `↓` 键选择，`Enter` 确认。

### 步骤 2：项目或资源选择 (Target Selection)

根据步骤 1 的动作，系统查询本地 SQLite 或文件系统，渲染列表：

* **若选择 Apply / Rollback / History / Destroy**：
  * 列出所有已注册的 Project Name（从 `projects` 表读取）。
  * *（若选择了 Apply，列表最顶部额外增加一项 `🛠️ <Ad-hoc 临时一次性任务>`）*
* **若选择 Infra / Project**：
  * 选择操作类型：`Load` (首次加载) / `Reload` (覆盖更新) / `List` (仅限 Project)。

### 步骤 3：上下文参数收集 (Contextual Parameters)

根据前两步的选择，动态弹出独立的表单组合：

#### 分支 A：选择了 `Apply` + `具体项目`

1. **目标环境 (Env)**：下拉选择或输入（默认：`default` 或 `production`）。
2. **部署说明 (Message)**：文本输入框（选填，如“修复支付 Bug”）。
3. **版本策略**：下拉单选（`自动升级 (Patch Bump)` / `手动指定版本号` / `从其他环境晋升 (Promotion)`）。
   * 若选 **手动指定版本号**，弹出输入框要求输入自定义版本号。
   * 若选 **从其他环境晋升 (Promotion)**，动态追加两个选择表单：
     * **来源环境 (From Env)**：下拉选择（对应 `--from-env`，如选择 `staging`）。
     * **来源版本 (From Version)**：根据选中的来源环境，拉取历史成功记录供列表选择（对应 `--from-version`）。

#### 分支 B：选择了 `Apply` + `<Ad-hoc 临时任务>`
1. **YAML 脚本路径**：文本输入框或文件选择器（要求用户选择或输入类似 `one-time-task.yaml` 的路径，对应 `-c` 参数）。
2. **目标环境**：下拉选择。

#### 分支 C：选择了 `Rollback` + `具体项目` **(🌟 杀手级体验)**
1. **目标环境**：下拉选择。
2. **选择历史版本**：系统拉取该项目在该环境下的最新 10 条 `Status="SUCCESS"` 的历史记录。
   * *渲染格式示例*：`v1.0.0 (2026-03-10 14:00) - feat: 发布全新的首页 UI`
   * 用户直接用键盘选中目标版本，极大地降低出错率。

#### 分支 D：选择了 `Destroy` / `History`
* **Destroy**：高危操作。询问是否彻底清理（`--full`）。并弹出红色警示表单（要求输入项目名确认或直接 `y/N`）。
* **History**：询问要查看前多少条记录（默认 10）。

#### 分支 E：选择了 `Infra` / `Project` 的加载
1. **选择配置文件 (-f)**：扫描当前目录及子目录下的 `.yaml`/`.yml` 文件，以下拉列表展示供用户选择。
2. **项目名称 (-n)** *(仅限 Project Load)*：文本输入框。

### 步骤 4：无缝交接引擎 (Execution)

向导使命结束。将收集到的所有变量（ProjectName, Env, Version, Message, OverridePath, FilePath 等）组装好，直接调用现有的底层执行函数，无缝将界面控制权移交给已有的 Bubbletea 进度条 TUI。

---

## 4. 技术实现建议 (Technical Guidelines)

### 4.1 推荐技术栈

* **核心库**：强烈建议使用 `github.com/charmbracelet/huh`。
  * *理由*：它是 Bubbletea 生态的官方表单库，与 Nova Deploy 现有的终端交互风格（如 `spinner`、进度条）完全一致，主题美观，API 基于极简的链式调用设计。

### 4.2 代码架构修改点

1. **Root 劫持** (`cmd/deploy/main.go` 或 root cmd 定义处)：
```go
// 移除 rootCmd 默认的单纯打印 Help 逻辑，加入向导判断
rootCmd.RunE = func(cmd *cobra.Command, args []string) error {
    // 当没有任何子命令和参数时，触发交互式向导
    if len(args) == 0 && cmd.Flags().NFlag() == 0 {
        return wizard.RunInteractiveWizard()
    }
    return cmd.Help()
}
```

2. **向导模块** (新增包如 `internal/wizard/wizard.go`)：
   * 负责编排 `huh` 的 Form 流程。
   * 处理对底层 SQLite 的查询逻辑（为回滚版本、已注册项目提供动态选项）。
   * 扫描本地目录获取 YAML 文件列表。

3. **函数打通**：
   * 确保 `apply`, `rollback`, `infra`, `project`, `destroy` 等命令中的核心业务逻辑（如 `runApplyImpl`）与 Cobra 的参数解析解耦。
   * 能够接受通过向导传入的结构化配置（Options Struct），而不仅仅是依赖 `cmd.Flags()`。

---

## 5. 非功能性需求 (Non-Functional Requirements)

1. **安全退出**：在向导的任何一个表单步骤中，用户按下 `Ctrl+C` 或 `Esc` 均应安全退出程序，不产生任何脏数据。
2. **优雅降级**：如果程序检测到当前运行环境不是标准的 TTY 终端（例如在 Jenkins / GitHub Actions 等 CI 环境中没有键盘交互能力），必须自动跳过向导并抛出错误或静默打印 Help 提示。
3. **UI 风格一致性**：向导的颜色主题（Theme）应当与现存部署执行图的颜色（如成功绿、运行蓝、警示黄）保持视觉风格的高度一致性。
