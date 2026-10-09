package adminui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// SettingsRow 只渲染两个具名插槽：#info 与 #control，没有默认插槽。
//
// 因此把 <SettingsRowLabel> / 控件直接写成 <SettingsRow> 的子节点时，
// Vue 会把它们送进默认插槽，而默认插槽根本不被渲染 —— 结果是整行变成
// 一个空的 <div class="settings-row__info"><!----></div>，页面上只剩卡片
// 标题、下面一片空白。这个错误 vue-tsc 与 vite build 都不会报，只能靠
// 真实渲染才能发现（本项目就曾因此让整个「MCP 服务」页全白）。
//
// 这个测试用静态扫描守住该契约：每个 <SettingsRow> 的开标签之后，到它的
// 闭合标签之前，不允许出现未被 <template #info> / <template #control>
// 包住的子元素。
func TestSettingsRowUsesNamedSlots(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "web", "src", "components", "admin")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("跳过：找不到前端组件目录 %s（%v）", dir, err)
	}

	// 只关心确实使用了 SettingsRow 的组件。
	// rowOpen 用来定位每一个 SettingsRow 的起点；
	// rowOpenNested 用来在同一文件里查找嵌套的 SettingsRow 开标签 —— 它要求
	// 标签名之后紧跟空白或 '>'，否则 "<SettingsRowLabel" 会被误当成
	// "<SettingsRow"（这是个真实的坑：前缀相同，strings.Index 会命中）。
	rowOpen := regexp.MustCompile(`<SettingsRow(?:\s[^>]*)?>`)
	rowOpenNested := regexp.MustCompile(`<SettingsRow(?:\s|>)`)
	rowClose := "</SettingsRow>"

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".vue") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", path, err)
		}
		src := string(raw)

		for _, block := range extractBlocks(src, rowOpen, rowOpenNested, rowClose) {
			body := block
			// 去掉所有具名 template 片段后再检查：剩下的裸内容即为默认插槽用法。
			stripped := stripNamedTemplates(body)
			if bare := firstBareChild(stripped); bare != "" {
				t.Errorf("%s: <SettingsRow> 的子节点 %q 没有包在 <template #info> 或 <template #control> 中；"+
					"SettingsRow 没有默认插槽，这些内容不会被渲染（页面会显示空白行）", e.Name(), bare)
			}
		}
	}
}

// extractBlocks 返回每一段 <SettingsRow>...</SettingsRow> 的内容（含开标签）。
//
// 必须按嵌套深度配对，不能用「第一个 </SettingsRow>」——SettingsRow 内部可以
// 嵌套 <template v-if>/<template v-else> 等结构，简单扫描会把后续无关内容吞进
// 当前块，产生误报。
//
// 注意：查找嵌套开标签时必须用 openTagRe（要求标签名后跟空白或 '>'），
// 否则 "<SettingsRowLabel" 也会被当成 "<SettingsRow" 命中，深度算错。
func extractBlocks(src string, first *regexp.Regexp, openTagRe *regexp.Regexp, closeTag string) []string {
	var out []string
	rest := src
	for {
		loc := first.FindStringIndex(rest)
		if loc == nil {
			return out
		}
		tail := rest[loc[0]:]
		// 跳过当前这个开标签本身，否则它会被当成嵌套的第一层。
		openEnd := strings.Index(tail, ">")
		if openEnd < 0 {
			return out
		}
		depth := 1
		i := openEnd + 1
		end := -1
		for i < len(tail) {
			nextOpen := openTagRe.FindStringIndex(tail[i:])
			nextClose := strings.Index(tail[i:], closeTag)
			if nextClose < 0 {
				break
			}
			if nextOpen != nil && nextOpen[0] < nextClose {
				depth++
				i += nextOpen[1]
				continue
			}
			depth--
			i += nextClose + len(closeTag)
			if depth == 0 {
				end = i
				break
			}
		}
		if end < 0 {
			return out
		}
		out = append(out, tail[:end])
		rest = tail[end:]
	}
}

// stripNamedTemplates 移除所有 <template #xxx>...</template> 片段。
//
// 必须按嵌套深度配对：具名 template 内部可以再嵌 <template v-if>/<template v-else>，
// 用非贪婪正则会在内层 </template> 处提前截断，把剩余的内层结构当成裸子节点（误报）。
func stripNamedTemplates(body string) string {
	openRe := regexp.MustCompile(`<template\s+#[A-Za-z0-9_-]+[^>]*>`)
	var out strings.Builder
	rest := body
	for {
		loc := openRe.FindStringIndex(rest)
		if loc == nil {
			out.WriteString(rest)
			return out.String()
		}
		out.WriteString(rest[:loc[0]])
		tail := rest[loc[0]:]
		depth := 0
		i := 0
		end := -1
		for i < len(tail) {
			no := strings.Index(tail[i:], "<template")
			nc := strings.Index(tail[i:], "</template>")
			if nc < 0 {
				break
			}
			if no >= 0 && no < nc {
				depth++
				i += no + len("<template")
				continue
			}
			depth--
			i += nc + len("</template>")
			if depth == 0 {
				end = i
				break
			}
		}
		if end < 0 {
			// 配对失败：原样保留，交由测试报出问题。
			out.WriteString(tail)
			return out.String()
		}
		rest = tail[end:]
	}
}

// firstBareChild 返回去掉开标签后第一个非空、非注释内容的首个标签名或片段。
func firstBareChild(stripped string) string {
	// 丢掉 <SettingsRow ...> 这个开标签本身。
	if i := strings.Index(stripped, ">"); i >= 0 {
		stripped = stripped[i+1:]
	}
	for _, line := range strings.Split(stripped, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "<!--") {
			continue
		}
		if strings.HasPrefix(line, "</") {
			continue
		}
		if len(line) > 60 {
			line = line[:60]
		}
		return line
	}
	return ""
}

// repoRoot 从测试文件所在目录向上找到含 go.mod 的仓库根。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd 失败：%v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Skip("跳过：向上未找到 go.mod")
	return ""
}
