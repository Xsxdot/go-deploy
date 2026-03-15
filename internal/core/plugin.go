package core

// StepPlugin 统一插件契约，所有执行动作必须实现此接口以接入引擎调度
type StepPlugin interface {
	// Name 返回插件类型标识，与 Step.Type 对应
	Name() string

	// Execute 接收上下文、当前 Step 定义和解析后的 Target 列表，执行具体动作
	// 插件内部通过类型断言过滤自己认识的 Target（如 HostTarget、K8sTarget）
	Execute(ctx *DeployContext, step Step, targets []Target) error

	// Rollback 在 Context Cancel 时提供状态补偿机制
	Rollback(ctx *DeployContext, step Step) error

	// Uninstall 彻底卸载时的资源回收，按 DAG 逆序调用
	Uninstall(ctx *DeployContext, step Step, targets []Target) error
}
