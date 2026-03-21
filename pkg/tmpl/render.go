package tmpl

import (
	"os"
	"regexp"
	"strings"
)

// varPattern matches ${varName} where varName is alphanumeric, underscore, or dot (for nested vars like vars.xxx)
var varPattern = regexp.MustCompile(`\$\{([a-zA-Z0-9_.]+)\}`)

// envPattern matches ${env.VAR_NAME}，用于安全凭据引用，仅在 Render 时实时读取 os.Getenv，不落盘到 vars/SQLite
var envPattern = regexp.MustCompile(`\$\{env\.([a-zA-Z0-9_]+)\}`)

// defaultVarPattern matches ${varName:-defaultValue}, supports nested ${nested} or literal, and dotted var names
var defaultVarPattern = regexp.MustCompile(`\$\{([a-zA-Z0-9_.]+):-(\$\{[^}]*\}|[^}]*)\}`)

const maxExpandDepth = 32 // prevent infinite recursion from circular refs

// Render replaces ${varName} in template with vars[varName].
// It also replaces ${env.VAR_NAME} with os.Getenv("VAR_NAME") at render time,
// so secrets never appear in the vars map or SQLite snapshots.
// Undefined variables are left unchanged.
func Render(template string, vars map[string]string) string {
	if vars == nil {
		vars = make(map[string]string)
	}
	// Phase 1: resolve ${env.VAR_NAME} in real time from environment
	result := envPattern.ReplaceAllStringFunc(template, func(match string) string {
		// extract name after "env."
		inner := match[2 : len(match)-1] // strip ${ and }
		name := strings.TrimPrefix(inner, "env.")
		if val := os.Getenv(name); val != "" {
			return val
		}
		return match // undefined env var: preserve placeholder
	})

	// Phase 2: resolve ${varName:-defaultValue} - use default when var is missing or empty
	result = defaultVarPattern.ReplaceAllStringFunc(result, func(match string) string {
			subs := defaultVarPattern.FindStringSubmatch(match)
			if len(subs) != 3 {
				return match
			}
			name, defaultVal := subs[1], subs[2]
			if v, ok := vars[name]; ok && v != "" {
				return v
			}
			return Render(defaultVal, vars) // recursive expand default (e.g. ${version})
		})
	// Phase 3: resolve plain ${varName}
	if len(vars) == 0 {
		return result
	}
	return varPattern.ReplaceAllStringFunc(result, func(match string) string {
		name := match[2 : len(match)-1] // strip ${ and }
		if v, ok := vars[name]; ok {
			return v
		}
		return match
	})
}

// RenderValue recursively renders any value: strings via Render, maps and slices recursively.
func RenderValue(v interface{}, vars map[string]string) interface{} {
	return renderValueDepth(v, vars, 0)
}

func renderValueDepth(v interface{}, vars map[string]string, depth int) interface{} {
	if depth >= maxExpandDepth {
		return v
	}
	switch x := v.(type) {
	case string:
		return Render(x, vars)
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, val := range x {
			out[k] = renderValueDepth(val, vars, depth+1)
		}
		return out
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, val := range x {
			if ks, ok := k.(string); ok {
				out[ks] = renderValueDepth(val, vars, depth+1)
			}
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, val := range x {
			out[i] = renderValueDepth(val, vars, depth+1)
		}
		return out
	case []string:
		out := make([]string, len(x))
		for i, s := range x {
			out[i] = Render(s, vars)
		}
		return out
	default:
		return v
	}
}

// NewRenderer returns a func(string) string bound to vars, for DeployContext.Render.
func NewRenderer(vars map[string]string) func(string) string {
	return func(template string) string {
		return Render(template, vars)
	}
}

// BuildVars merges globalVars, variables, and environment variables.
// Priority: env > variables > globalVars.
// Variables may reference globalVars; globalVars values may reference env.
func BuildVars(globalVars, variables map[string]string) map[string]string {
	env := envMap()

	// Phase 1: expand globalVars (values can contain ${ENV_VAR})
	expanded := make(map[string]string)
	for k, v := range globalVars {
		expanded[k] = expandString(v, env, 0)
	}

	// Phase 2: expand variables (can reference globalVars)
	vars := merged(expanded, env)
	for k, v := range variables {
		expanded[k] = expandString(v, vars, 0)
	}

	// Phase 3: merge with env (env overrides)
	return merged(expanded, env)
}

func expandString(s string, vars map[string]string, depth int) string {
	if depth >= maxExpandDepth || vars == nil {
		return s
	}
	replaced := Render(s, vars)
	if replaced == s {
		return s
	}
	return expandString(replaced, vars, depth+1)
}

func merged(a, b map[string]string) map[string]string {
	if a == nil && b == nil {
		return map[string]string{}
	}
	out := make(map[string]string)
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func envMap() map[string]string {
	// Build from os.Environ for one-time snapshot
	env := make(map[string]string)
	for _, e := range os.Environ() {
		for i := 0; i < len(e); i++ {
			if e[i] == '=' {
				env[e[:i]] = e[i+1:]
				break
			}
		}
	}
	return env
}
