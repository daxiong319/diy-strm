package unrecognized

import (
	"path"
	"strings"
)

// 未识别兜底：识别不出标题的文件往哪放。
//
// 三种填法照搬参考实现，但**判定写成纯函数**，原因是这一段有三个相互独立
// 的岔路口（绝对路径 / 纯目录名 / 留空），再叠上 skip_action 与 Emby 反查，
// 任何一处判断写错都表现为「文件默默去了别的地方」，而计划里只有一行 skip 记录。
// 纯函数让这四种组合能被一张表穷举，不必真跑一次网盘整理。

const (
	// UnrecognizedKeep 留在源目录，只记一条识别失败。
	UnrecognizedKeep = "keep"
	// UnrecognizedMove 移到兜底目录。
	UnrecognizedMove = "move"

	// UnrecognizedDefaultDir 未识别兜底目录的默认名。
	UnrecognizedDefaultDir = "未识别"
)

// UnrecognizedTargetKind 兜底目标的三种形态。
type UnrecognizedTargetKind string

const (
	// TargetKeep 留在源目录，不产生任何目标。
	TargetKeep UnrecognizedTargetKind = "keep"
	// TargetAbsolute 以 / 开头的完整路径，直接落该目录。
	TargetAbsolute UnrecognizedTargetKind = "absolute"
	// TargetRelative 纯目录名，落在 <整理目录>/<一级>/<目录名>/。
	TargetRelative UnrecognizedTargetKind = "relative"
)

// UnrecognizedTarget 是「未识别文件该落哪」的判定结果。
type UnrecognizedTarget struct {
	// Kind 三种形态之一。
	Kind UnrecognizedTargetKind `json:"kind"`
	// Path 绝对路径形态下的目标路径（以 / 开头）。
	Path string `json:"path,omitempty"`
	// DirName 纯目录名形态下的目录名。
	DirName string `json:"dir_name,omitempty"`
	// KeepSource 为真表示「留在源目录，只记一条识别失败」。
	KeepSource bool `json:"keep_source"`
	// SkipReason 写进 plan.Skipped 的原因文案。
	SkipReason string `json:"skip_reason"`
}

// Options 是兜底判定的全部输入。
type Options struct {
	// SkipAction keep / move；空值等同 keep。
	SkipAction string
	// UnrecognizedDir 兜底目录的三种填法之一。
	UnrecognizedDir string
	// TargetDirectory 整理目录路径（纯目录名形态的父目录）。
	TargetDirectory string
	// Category 一级分类目录名（纯目录名形态的第二层）。
	Category string
	// FollowExistingLocation 为真且 Emby 已查到该作品的位置时，优先落已有位置。
	FollowExistingLocation bool
	// ExistingLocation Emby 反查到的库内位置（绝对路径），查不到时为空。
	ExistingLocation string
}

// ResolveUnrecognizedTarget 判定未识别文件的落点。
//
// 优先级刻意是「Emby 已有位置 > 绝对路径 > 纯目录名 > 留源目录」：
// 沿用已有位置是唯一一个由外部事实（媒体库里已经有它了）决定的答案，
// 把它排在用户配置前面，用户配置才不会覆盖一个更准的答案 —— 但反过来，
// 用户显式填了绝对路径时不该被 Emby 抢走，所以只有**开启开关**才让 Emby 优先。
//
// ⚠️ skip_action=keep 时这里直接返回 TargetKeep，**不看目录填法**：
// 两种填法都是「keep 之后去哪」的备选，而 keep 的定义就是不去。
func ResolveUnrecognizedTarget(opts Options) UnrecognizedTarget {
	if strings.TrimSpace(opts.SkipAction) != UnrecognizedMove {
		return UnrecognizedTarget{
			Kind:       TargetKeep,
			KeepSource: true,
			SkipReason: "媒体识别失败，留在源目录",
		}
	}
	if opts.FollowExistingLocation {
		if existing := normalizeAbsoluteDir(opts.ExistingLocation); existing != "" {
			return UnrecognizedTarget{
				Kind:       TargetAbsolute,
				Path:       existing,
				SkipReason: "媒体识别失败，沿用媒体库里已有的位置",
			}
		}
	}
	raw := strings.TrimSpace(opts.UnrecognizedDir)
	if raw == "" {
		// 留空 = 留在源目录。skip_action=move 在这里被否决：把文件
		// 移到一个「没填」的地方等于随机挑一个目录。
		return UnrecognizedTarget{
			Kind:       TargetKeep,
			KeepSource: true,
			SkipReason: "媒体识别失败，未配置兜底目录，留在源目录",
		}
	}
	if strings.HasPrefix(raw, "/") {
		cleaned := normalizeAbsoluteDir(raw)
		if cleaned == "" {
			return UnrecognizedTarget{
				Kind:       TargetKeep,
				KeepSource: true,
				SkipReason: "媒体识别失败，兜底目录路径无效，留在源目录",
			}
		}
		return UnrecognizedTarget{
			Kind:       TargetAbsolute,
			Path:       cleaned,
			SkipReason: "媒体识别失败，移到兜底目录",
		}
	}
	name := sanitizeUnrecognizedDirName(raw)
	if name == "" {
		return UnrecognizedTarget{
			Kind:       TargetKeep,
			KeepSource: true,
			SkipReason: "媒体识别失败，兜底目录名无效，留在源目录",
		}
	}
	return UnrecognizedTarget{
		Kind:       TargetRelative,
		DirName:    name,
		SkipReason: "媒体识别失败，移到未识别目录",
	}
}

// RelativeTargetPath 把「纯目录名」形态的目标拼成完整相对路径：
// <整理目录>/<一级分类>/<目录名>。整理目录或分类为空时逐段省略，
// 绝不用 / 或空串占位 —— 那会让最终路径变成 /未识别 或 //未识别。
func RelativeTargetPath(target UnrecognizedTarget, targetDirectory, category string) string {
	if target.Kind != TargetRelative {
		return ""
	}
	parts := make([]string, 0, 3)
	if dir := normalizeAbsoluteDir(targetDirectory); dir != "" {
		parts = append(parts, dir)
	}
	if cat := strings.TrimSpace(category); cat != "" {
		parts = append(parts, path.Clean(cat))
	}
	if target.DirName != "" {
		parts = append(parts, target.DirName)
	}
	return strings.Join(parts, "/")
}

// normalizeAbsoluteDir 把绝对路径归一，并挡掉 ../ 逃逸。
//
// path.Clean 会把 /media/../etc 折叠成 /etc —— 那不是用户填的意思，
// 而兜底目录是要被整理任务当作写入目标的，逃逸等于让整理写到别的地方去。
// 这里因此在 Clean 之后再检查：结果里不允许出现 /../ 或以 ../ 开头。
func normalizeAbsoluteDir(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "/") {
		return ""
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	cleaned := path.Clean(trimmed)
	if cleaned == "/" {
		// 根目录当兜底目录 = 整盘皆兜底，整理会失去任何约束。判无效。
		return ""
	}
	if strings.Contains(cleaned, "/../") || strings.HasPrefix(cleaned, "../") {
		return ""
	}
	return cleaned
}

// sanitizeUnrecognizedDirName 清洗纯目录名形态的填法。
//
// 网盘目录名里出现的控制字符与路径分隔符都要去掉：这一段拼出来的路径
// 会直接交给 CreateFolder，而网盘的同名判断是按字面来的，
// 一个带斜杠的「目录名」会静默变成两级目录。
func sanitizeUnrecognizedDirName(raw string) string {
	cleaned := strings.TrimSpace(raw)
	if cleaned == "" || cleaned == "." || cleaned == ".." {
		return ""
	}
	cleaned = strings.ReplaceAll(cleaned, "\\", "_")
	cleaned = strings.ReplaceAll(cleaned, "/", "_")
	var b strings.Builder
	for _, r := range cleaned {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	out = strings.Trim(out, ". ")
	if out == "" {
		return ""
	}
	return out
}
