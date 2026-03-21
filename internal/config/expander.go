package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Xsxdot/go-deploy/internal/core"

	"gopkg.in/yaml.v3"
)

// TemplateDef 表示独立步骤模板的 YAML 结构
type TemplateDef struct {
	Steps []core.Step `yaml:"steps"`
}

// ExpandPipeline 对 Pipeline 的 Build、Deploy、Steps 分别进行宏展开（兼容旧版仅含 steps 的 YAML）
// 各阶段独立展开，不跨阶段传递 includeLeaves，保持三阶段语义独立
func ExpandPipeline(pipeline *core.Pipeline, baseDir string) (*core.Pipeline, error) {
	expanded := &core.Pipeline{Name: pipeline.Name}
	var err error
	if expanded.Build, err = expandStepArray(pipeline.Build, baseDir); err != nil {
		return nil, err
	}
	if expanded.Deploy, err = expandStepArray(pipeline.Deploy, baseDir); err != nil {
		return nil, err
	}
	if expanded.Steps, err = expandStepArray(pipeline.Steps, baseDir); err != nil {
		return nil, err
	}
	return expanded, nil
}

// expandStepArray 对步骤数组进行宏展开，抹平所有 include 步骤（各阶段独立，不跨阶段传递）
func expandStepArray(steps []core.Step, baseDir string) ([]core.Step, error) {
	expanded := make([]core.Step, 0)
	includeLeaves := make(map[string][]string)

	for _, step := range steps {
		if step.Type != "include" {
			// ==========================================
			// 1. 普通步骤的下游重连 (Exitpoint Relinking)
			// ==========================================
			// 如果这个普通步骤依赖了某个被展开的 include 步骤，
			// 我们需要把这个依赖替换为该 include 步骤内部的"叶子节点"。
			newNeeds := make([]string, 0)
			for _, dep := range step.Needs {
				if leaves, ok := includeLeaves[dep]; ok {
					newNeeds = append(newNeeds, leaves...)
				} else {
					newNeeds = append(newNeeds, dep)
				}
			}
			step.Needs = newNeeds
			expanded = append(expanded, step)
			continue
		}

		// ==========================================
		// 2. 处理 include 步骤：加载并解析模板
		// ==========================================
		templatePath, ok := step.With["template"].(string)
		if !ok || templatePath == "" {
			slog.Error("include 步骤缺少 template 路径", "step", step.Name)
			return nil, fmt.Errorf("step '%s' (include) missing 'template' path in with", step.Name)
		}

		// 解析 vars
		templateVars := make(map[string]string)
		if varsRaw, ok := step.With["vars"].(map[string]interface{}); ok {
			for k, v := range varsRaw {
				templateVars[k] = fmt.Sprintf("%v", v)
			}
		}

		// 读取文件（支持 @/ 表示 workspace 根、相对路径、绝对路径）
		resolvedPath := templatePath
		if strings.HasPrefix(templatePath, "@/") {
			resolvedPath = templatePath[2:] // strip "@/"
		}
		absPath := resolvedPath
		if !filepath.IsAbs(absPath) && baseDir != "" {
			absPath = filepath.Join(baseDir, resolvedPath)
		}
		data, err := os.ReadFile(absPath)
		if err != nil {
			slog.Error("读取模板文件失败", "path", absPath, "step", step.Name, "err", err)
			return nil, fmt.Errorf("failed to read template '%s' for step '%s': %w", absPath, step.Name, err)
		}

		var tmplDef TemplateDef
		if err := yaml.Unmarshal(data, &tmplDef); err != nil {
			slog.Error("解析模板文件失败", "path", absPath, "err", err)
			return nil, fmt.Errorf("failed to parse template '%s': %w", absPath, err)
		}

		// ==========================================
		// 3. 模板内部的 DAG 拓扑分析与重组
		// ==========================================
		// 找出模板内部哪些节点是被依赖的，剩下的就是"叶子节点"
		isDependedOn := make(map[string]bool)
		for _, tStep := range tmplDef.Steps {
			for _, dep := range tStep.Needs {
				isDependedOn[dep] = true
			}
		}

		var leaves []string

		for _, tStep := range tmplDef.Steps {
			// A. 命名空间隔离 (防止多次 include 同一个模板导致同名冲突)
			newName := fmt.Sprintf("%s.%s", step.Name, tStep.Name)

			if !isDependedOn[tStep.Name] {
				leaves = append(leaves, newName)
			}

			// B. 依赖重连 (Needs Relinking)
			// 入口节点继承主流程中 include 步骤的 needs，且需展开其中的 include 名
			newNeeds := make([]string, 0)
			if len(tStep.Needs) == 0 {
				for _, dep := range step.Needs {
					if leafList, ok := includeLeaves[dep]; ok {
						newNeeds = append(newNeeds, leafList...)
					} else {
						newNeeds = append(newNeeds, dep)
					}
				}
			} else {
				// 内部节点：加上命名空间前缀，保持内部依赖正确
				for _, dep := range tStep.Needs {
					newNeeds = append(newNeeds, fmt.Sprintf("%s.%s", step.Name, dep))
				}
			}

			// C. 编译期变量替换 (只替换 ${vars.xxx})
			renderedStep := renderTemplateVars(tStep, templateVars)

			// 覆盖原属性
			renderedStep.Name = newName
			renderedStep.Needs = newNeeds

			// 加入主干道
			expanded = append(expanded, renderedStep)
		}

		// 记录该 include 步骤的所有叶子节点，供后面的普通步骤重连
		includeLeaves[step.Name] = leaves
	}

	return expanded, nil
}

// renderTemplateVars 是一个简易的宏替换器，替换 ${vars.key} 和 ${vars.key:-default} 形式
func renderTemplateVars(step core.Step, vars map[string]string) core.Step {
	// 将 step 序列化为 JSON 字符串，进行全局字符替换，再反序列化回来
	// 这是一种极其简单且鲁棒的深拷贝 + 宏替换方式
	b, _ := json.Marshal(step)
	str := string(b)

	// 先替换带默认值的 ${vars.key:-default} 形式
	// 正则匹配 ${vars.xxx:-yyy} 形式
	varWithDefault := regexp.MustCompile(`\$\{vars\.([a-zA-Z0-9_]+):-([^}]*)\}`)
	str = varWithDefault.ReplaceAllStringFunc(str, func(match string) string {
		subs := varWithDefault.FindStringSubmatch(match)
		if len(subs) != 3 {
			return match
		}
		key, defaultVal := subs[1], subs[2]
		if v, ok := vars[key]; ok && v != "" {
			// 变量存在且非空，使用变量值
			return strings.ReplaceAll(v, "\"", "\\\"")
		}
		// 变量不存在或为空，使用默认值
		return strings.ReplaceAll(defaultVal, "\"", "\\\"")
	})

	// 再替换普通的 ${vars.key} 形式
	for k, v := range vars {
		placeholder := fmt.Sprintf("${vars.%s}", k)
		// 注意：如果 v 里面有双引号，为了 JSON 语法安全，应当做一次转义，
		// 但通常 vars 传的都是简单的 role 名字或字符串。
		safeVal := strings.ReplaceAll(v, "\"", "\\\"")
		str = strings.ReplaceAll(str, placeholder, safeVal)
	}

	var newStep core.Step
	_ = json.Unmarshal([]byte(str), &newStep)
	return newStep
}
