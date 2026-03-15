package core

// Step 代表流水线中的一个通用执行单元
type Step struct {
	Name             string                 `yaml:"name"`
	Type             string                 `yaml:"type"`             // 映射到注册的 Plugin Name
	Needs            []string               `yaml:"needs"`            // 依赖的前置步骤的 Name，空则引擎启动时立即执行
	Roles            []string               `yaml:"roles"`            // 目标节点组，空则代表本地/全局执行
	RunIf            string                 `yaml:"run_if,omitempty"` // 条件执行表达式，如 "${image_tag} == ''"，为 false 时跳过
	BatchSize        int                    `yaml:"batch_size"`
	Retries          int                    `yaml:"retries"`
	RetryDelay       string                 `yaml:"retry_delay"`
	TolerateFailures string                 `yaml:"tolerate_failures"`
	With             map[string]interface{} `yaml:"with"` // 插件自定义参数，延迟解析
}

// Pipeline 流水线定义
// Build: 构建阶段 (CI)，SkipBuild 时跳过
// Deploy: 部署阶段 (CD)
// Steps: finally 性质，最终一定会执行；兼容旧版 YAML 结构
type Pipeline struct {
	Name   string `yaml:"name"`
	Build  []Step `yaml:"build"`
	Deploy []Step `yaml:"deploy"`
	Steps  []Step `yaml:"steps"`
}

// Target 代表任何可以作为部署目标的异构资源
type Target interface {
	ID() string
	Type() string
}

// HostTarget 异构目标实现：物理机 (Host)
type HostTarget struct {
	ResourceID string            `yaml:"-"`
	Addr       string            `yaml:"addr"`
	LanAddr    string            `yaml:"lanAddr,omitempty"`    // 内网 IP，Nginx proxy_pass 等路由时优先使用
	PublicAddr string            `yaml:"publicAddr,omitempty"` // 公网 IP，供 DNS/对外暴露；为空时 DNS 插件回退使用 Addr
	User       string            `yaml:"user"`
	Auth       map[string]string `yaml:"auth"`
	Proxy      string            `yaml:"proxy,omitempty"`
	// Bastion 为跳板机/堡垒机的 resource ID，非空时 SSH 连接经由该堡垒机转发
	// 对应 infra.yaml 中另一个 HostTarget 的 key，例如 bastion: "jump-server"
	Bastion       string `yaml:"bastion,omitempty"`
	HasNginx      bool   `yaml:"hasNginx,omitempty"`
	NginxConfPath string `yaml:"nginxConfPath,omitempty"` // HasNginx 为 true 且该值为空时自动补足 /etc/nginx/conf.d/
}

// GetRouteIP 优先返回内网 IP (LanAddr)，为空时退化为 Addr
func (h *HostTarget) GetRouteIP() string {
	if h.LanAddr != "" {
		return h.LanAddr
	}
	return h.Addr
}

// ID 实现 Target 接口
func (h *HostTarget) ID() string { return h.ResourceID }

// Type 实现 Target 接口
func (h *HostTarget) Type() string { return "host" }

// K8sTarget 异构目标实现：Kubernetes 集群
type K8sTarget struct {
	ResourceID string `yaml:"-"`
	Context    string `yaml:"context"`
	Namespace  string `yaml:"namespace"`
}

// ID 实现 Target 接口
func (k *K8sTarget) ID() string { return k.ResourceID }

// Type 实现 Target 接口
func (k *K8sTarget) Type() string { return "k8s" }

// InfraConfig 基础设施清单
type InfraConfig struct {
	Providers  map[string]map[string]map[string]interface{} `yaml:"providers"`
	GlobalVars map[string]string                            `yaml:"globalVars"`
	Resources  map[string]Target                            `yaml:"resources"` // 异构资源池，key 为资源 ID
	Roles      map[string][]string                          `yaml:"roles"`
}

// EnvironmentConfig 环境级变量重载
type EnvironmentConfig struct {
	Variables map[string]string `yaml:"variables"`
}

// PipelineConfig 完整的流水线配置（含顶层元数据）
type PipelineConfig struct {
	Name         string                       `yaml:"name"`
	Environments map[string]EnvironmentConfig `yaml:"environments"` // 按环境名重载变量
	Roles        []string                     `yaml:"roles"`        // 全局默认角色，step 未声明 roles 时继承
	Variables    map[string]string            `yaml:"variables"`
	Pipeline     Pipeline                     `yaml:"pipeline"`
}
