package moviepilot

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// 本文件用无第三方依赖的方式替代旧实现依赖的 github.com/flosch/pongo2/v5。
//
// 旧实现（/tmp/v030/internal/moviepilot/wash_naming.go）用 pongo2 渲染重命名模板，
// 但仅用到两类语法：`{{ 变量 }}` 与 `{% if 变量 %}...{% endif %}`（可带 `{% else %}`）。
// 这里实现一个覆盖该子集的最小求值器，语义与 pongo2 在这两个模板上的行为一致：
//   - 未定义的变量渲染为空串（pongo2 默认行为）；
//   - if 的真值判定：非空字符串、非零数值为真；
//   - 模板编译失败（语法非法）时调用方回退到内置默认模板。

// TemplateVars 模板变量集合。键与旧实现的 washNamingVars.pongoContext() 完全一致（共 20 个）：
// title, year, category, season, episode, s, e, ep, tags, ext,
// resolution, codec, audio, format, hdr, bitdepth, edition, group, customization, tmdb_id
type TemplateVars map[string]any

// templateNode 模板语法树节点。
type templateNode interface {
	render(vars TemplateVars) string
}

// textNode 原样文本。
type textNode struct{ text string }

func (n *textNode) render(TemplateVars) string { return n.text }

// varNode 变量输出节点。
type varNode struct{ name string }

func (n *varNode) render(vars TemplateVars) string { return stringifyVar(vars[n.name]) }

// ifNode 条件节点。
type ifNode struct {
	name     string
	thenBody []templateNode
	elseBody []templateNode
}

func (n *ifNode) render(vars TemplateVars) string {
	body := n.elseBody
	if truthy(vars[n.name]) {
		body = n.thenBody
	}
	var sb strings.Builder
	for _, c := range body {
		sb.WriteString(c.render(vars))
	}
	return sb.String()
}

// Template 已编译的模板。
type Template struct {
	raw   string
	nodes []templateNode
}

// templateCache 模板编译缓存，避免重复解析同一模板串。
var templateCache sync.Map // string -> *Template

// ErrTemplateSyntax 模板语法错误。
type ErrTemplateSyntax struct{ Msg string }

func (e *ErrTemplateSyntax) Error() string { return e.Msg }

// CompileTemplate 编译模板。仅支持 `{{ var }}`、`{% if var %}`、`{% else %}`、`{% endif %}`。
// 出现其它标签（for/自定义 filter/比较运算等）返回语法错误，由调用方回退默认模板。
func CompileTemplate(raw string) (*Template, error) {
	if cached, ok := templateCache.Load(raw); ok {
		return cached.(*Template), nil
	}
	nodes, err := parseTemplate(raw)
	if err != nil {
		return nil, err
	}
	tpl := &Template{raw: raw, nodes: nodes}
	templateCache.Store(raw, tpl)
	return tpl, nil
}

// Render 渲染模板。
func (t *Template) Render(vars TemplateVars) string {
	var sb strings.Builder
	for _, n := range t.nodes {
		sb.WriteString(n.render(vars))
	}
	return sb.String()
}

// parseTemplate 解析模板为节点列表。
func parseTemplate(raw string) ([]templateNode, error) {
	nodes, rest, err := parseNodes(raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(rest) != "" {
		return nil, &ErrTemplateSyntax{Msg: fmt.Sprintf("模板存在未闭合的标签：%q", strings.TrimSpace(rest))}
	}
	return nodes, nil
}

// parseNodes 解析节点序列，遇到 {% endif %} / {% else %} 时提前返回剩余文本。
func parseNodes(raw string) ([]templateNode, string, error) {
	var nodes []templateNode
	for {
		start := strings.Index(raw, "{{")
		tagStart := strings.Index(raw, "{%")
		// 找最先出现的标记
		switch {
		case start < 0 && tagStart < 0:
			if raw != "" {
				nodes = append(nodes, &textNode{text: raw})
			}
			return nodes, "", nil
		case tagStart < 0 || (start >= 0 && start < tagStart):
			end := strings.Index(raw[start:], "}}")
			if end < 0 {
				return nil, "", &ErrTemplateSyntax{Msg: "模板存在未闭合的 {{ 标记"}
			}
			nodes = append(nodes, &textNode{text: raw[:start]})
			name := strings.TrimSpace(raw[start+2 : start+end])
			if name == "" {
				return nil, "", &ErrTemplateSyntax{Msg: "模板存在空的 {{ }} 变量"}
			}
			if !isValidVarName(name) {
				return nil, "", &ErrTemplateSyntax{Msg: fmt.Sprintf("不支持的模板表达式：%q", name)}
			}
			nodes = append(nodes, &varNode{name: name})
			raw = raw[start+end+2:]
		default:
			end := strings.Index(raw[tagStart:], "%}")
			if end < 0 {
				return nil, "", &ErrTemplateSyntax{Msg: "模板存在未闭合的 {% 标记"}
			}
			nodes = append(nodes, &textNode{text: raw[:tagStart]})
			tag := strings.TrimSpace(raw[tagStart+2 : tagStart+end])
			inner := raw[tagStart+end+2:]
			switch {
			case tag == "endif" || tag == "else":
				// 交给上层 if 处理
				return nodes, tag + inner, nil
			case strings.HasPrefix(tag, "if "):
				name := strings.TrimSpace(strings.TrimPrefix(tag, "if "))
				if !isValidVarName(name) {
					return nil, "", &ErrTemplateSyntax{Msg: fmt.Sprintf("不支持的 if 条件：%q", name)}
				}
				thenBody, afterThen, err := parseNodes(inner)
				if err != nil {
					return nil, "", err
				}
				node := &ifNode{name: name, thenBody: thenBody}
				rest := strings.TrimSpace(afterThen)
				if strings.HasPrefix(rest, "else") {
					afterElse := strings.TrimPrefix(rest, "else")
					elseBody, afterEndif, err := parseNodes(afterElse)
					if err != nil {
						return nil, "", err
					}
					node.elseBody = elseBody
					rest = strings.TrimSpace(afterEndif)
				}
				if !strings.HasPrefix(rest, "endif") {
					return nil, "", &ErrTemplateSyntax{Msg: fmt.Sprintf("if 缺少 endif：%q", tag)}
				}
				nodes = append(nodes, node)
				raw = strings.TrimPrefix(rest, "endif")
			default:
				return nil, "", &ErrTemplateSyntax{Msg: fmt.Sprintf("不支持的模板标签：%q", tag)}
			}
		}
	}
}

// isValidVarName 仅接受单层标识符（字母/数字/下划线），拒绝过滤器与运算表达式。
func isValidVarName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// stringifyVar 把变量值渲染为字符串，nil/未知渲染为空串（与 pongo2 缺省行为一致）。
func stringifyVar(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case uint:
		return strconv.FormatUint(uint64(x), 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 32)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// truthy 真值判定：非空字符串、非零数值、true 为真。
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return strings.TrimSpace(x) != ""
	case int:
		return x != 0
	case int64:
		return x != 0
	case int32:
		return x != 0
	case uint:
		return x != 0
	case uint64:
		return x != 0
	case float64:
		return x != 0
	case float32:
		return x != 0
	default:
		return true
	}
}

// 内置默认模板（与旧实现字节级一致）。
const (
	DefaultMovieNameTemplate = `{{ title }}{% if year %} ({{ year }}){% endif %}{% if tags %}.{{ tags }}{% endif %}{{ ext }}`
	DefaultTvNameTemplate    = `{{ title }}{% if year %}.{{ year }}{% endif %}.{{ s }}{{ e }}.第{{ ep }}集{% if tags %}.{{ tags }}{% endif %}{{ ext }}`
)

// DefaultNameTemplateFor 按媒体类型返回内置默认模板。
func DefaultNameTemplateFor(category string) string {
	if category == "tv" {
		return DefaultTvNameTemplate
	}
	return DefaultMovieNameTemplate
}

// RenderWashName 渲染命名模板：空模板用默认模板；编译失败记录并回退默认模板；
// 渲染结果为空或仍含未替换的 {{ 时返回错误（由调用方再回退历史命名）。
// 返回的第二个值表示是否发生了回退（供调用方记录日志）。
func RenderWashName(tpl, category string, vars TemplateVars) (string, bool, error) {
	raw := strings.TrimSpace(tpl)
	fellBack := false
	if raw == "" {
		raw = DefaultNameTemplateFor(category)
		fellBack = tpl != ""
	}
	compiled, err := CompileTemplate(raw)
	if err != nil {
		fallback, ferr := CompileTemplate(DefaultNameTemplateFor(category))
		if ferr != nil {
			return "", true, fmt.Errorf("默认命名模板不可用：%w", ferr)
		}
		out := strings.TrimSpace(fallback.Render(vars))
		if out == "" || strings.Contains(out, "{{") {
			return "", true, fmt.Errorf("命名模板渲染结果无效：%q", out)
		}
		return out, true, nil
	}
	out := strings.TrimSpace(compiled.Render(vars))
	if out == "" || strings.Contains(out, "{{") {
		return "", fellBack, fmt.Errorf("命名模板渲染结果无效：%q", out)
	}
	return out, fellBack, nil
}
