package engine

import (
	"fmt"
	"log/slog"

	"github.com/Xsxdot/go-deploy/internal/core"
)

// ValidateDAG 对 Build、Deploy、Steps 三阶段分别拓扑排序，每阶段内依赖独立，阶段间按序执行。
// 返回 (合并后的执行顺序用于 TUI/回滚, Build 阶段步骤数, error)。
func ValidateDAG(pipeline *core.Pipeline) (order []*core.Step, buildCount int, err error) {
	buildOrder, err := validateDAGSteps(pipeline.Build)
	if err != nil {
		return nil, 0, fmt.Errorf("build phase: %w", err)
	}
	deployOrder, err := validateDAGSteps(pipeline.Deploy)
	if err != nil {
		return nil, 0, fmt.Errorf("deploy phase: %w", err)
	}
	stepsOrder, err := validateDAGSteps(pipeline.Steps)
	if err != nil {
		return nil, 0, fmt.Errorf("steps phase: %w", err)
	}
	order = make([]*core.Step, 0, len(buildOrder)+len(deployOrder)+len(stepsOrder))
	order = append(order, buildOrder...)
	order = append(order, deployOrder...)
	order = append(order, stepsOrder...)
	buildCount = len(buildOrder)
	return order, buildCount, nil
}

// validateDAGSteps 对步骤数组进行拓扑排序并检测循环依赖。
func validateDAGSteps(steps []core.Step) ([]*core.Step, error) {
	if len(steps) == 0 {
		return nil, nil
	}
	nameToStep := make(map[string]*core.Step)
	for i := range steps {
		nameToStep[steps[i].Name] = &steps[i]
	}

	inDegree := make(map[string]int)
	adj := make(map[string][]string)

	for i := range steps {
		s := &steps[i]
		if _, ok := inDegree[s.Name]; !ok {
			inDegree[s.Name] = 0
		}
		for _, dep := range s.Needs {
			if _, exists := nameToStep[dep]; !exists {
				slog.Error("步骤存在未知依赖", "step", s.Name, "dep", dep)
				return nil, fmt.Errorf("step '%s' has unknown dependency: '%s'", s.Name, dep)
			}
			adj[dep] = append(adj[dep], s.Name)
			inDegree[s.Name]++
		}
	}

	var queue []string
	for name, d := range inDegree {
		if d == 0 {
			queue = append(queue, name)
		}
	}

	var order []*core.Step
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		order = append(order, nameToStep[name])

		for _, to := range adj[name] {
			inDegree[to]--
			if inDegree[to] == 0 {
				queue = append(queue, to)
			}
		}
	}

	if len(order) != len(steps) {
		slog.Error("检测到流水线依赖环")
		return nil, fmt.Errorf("cycle detected in pipeline dependencies")
	}

	return order, nil
}
