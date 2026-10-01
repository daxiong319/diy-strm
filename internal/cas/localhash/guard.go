package localhash

import (
	"os"
	"path/filepath"
	"strings"

	"litepan/internal/domain"
)

// ResolveLocalMediaPath 把用户传入的相对/绝对路径解析并校验在允许的根目录内。
//
// 校验顺序：空值 → Abs → EvalSymlinks（必须解析符号链接，防 symlink 逃逸）→
// 逐个 allowedRoots 做 Rel 包含判定 → os.Stat 必须是普通文件。
// 返回解析后的绝对真实路径（已消除符号链接）。
//
// 相对路径的语义：**优先相对第一个媒体根目录**解释，而不是相对进程 CWD。
// 因为进程 CWD 在容器里是 /app，用户写 "影片.mkv" 会被解析成 /app/影片.mkv
// 并必然被白名单拒绝，属于无谓的可用性陷阱。
// 回退顺序：逐根尝试 raw 拼接 → 若都不存在再按 CWD（保留原有绝对语义）。
func ResolveLocalMediaPath(rawPath string, allowedRoots []string) (string, error) {
	raw := strings.TrimSpace(rawPath)
	if raw == "" {
		return "", domain.Errorf(domain.CodeValidation, "本地文件路径不能为空")
	}

	if len(allowedRoots) == 0 {
		return "", domain.Errorf(domain.CodeValidation, "未配置本地媒体根目录，该功能未启用")
	}

	abs, err := resolveAgainstRoots(raw, allowedRoots)
	if err != nil {
		return "", err
	}

	// 必须在任何 Stat 之前解析符号链接：否则一个指向根目录之外的 symlink
	// 会带着"看起来在根目录内"的路径通过后续检查。
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", domain.Errorf(domain.CodeValidation, "本地文件不存在：%s", raw)
		}
		return "", domain.Wrap(domain.CodeValidation, err)
	}

	if !withinAnyRoot(real, allowedRoots) {
		return "", domain.Errorf(domain.CodeValidation, "路径超出允许的媒体根目录：%s", raw)
	}

	info, err := os.Stat(real)
	if err != nil {
		if os.IsNotExist(err) {
			return "", domain.Errorf(domain.CodeValidation, "本地文件不存在：%s", raw)
		}
		return "", domain.Wrap(domain.CodeValidation, err)
	}
	// 目录、设备文件、命名管道、socket 一律拒绝：只有普通文件才有稳定的哈希。
	if !info.Mode().IsRegular() {
		if info.IsDir() {
			return "", domain.Errorf(domain.CodeValidation, "路径是目录而非文件：%s", raw)
		}
		return "", domain.Errorf(domain.CodeValidation, "路径不是普通文件：%s", raw)
	}

	return real, nil
}

// resolveAgainstRoots 把 raw 解析为绝对路径：绝对路径原样返回，相对路径优先
// 相对各媒体根目录拼接（取第一个真实存在的），否则退回相对进程 CWD。
func resolveAgainstRoots(raw string, allowedRoots []string) (string, error) {
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw), nil
	}
	for _, root := range allowedRoots {
		trimmed := strings.TrimSpace(root)
		if trimmed == "" {
			continue
		}
		candidate := filepath.Join(trimmed, raw)
		// 相对根拼接后存在即采用；不存在则继续试下一个根。
		if _, err := os.Stat(candidate); err == nil {
			abs, aerr := filepath.Abs(candidate)
			if aerr != nil {
				return "", domain.Wrap(domain.CodeValidation, aerr)
			}
			return abs, nil
		}
	}
	// 都不存在：按 CWD 解析，让后续 EvalSymlinks 给出统一的"不存在"错误。
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", domain.Wrap(domain.CodeValidation, err)
	}
	return abs, nil
}

// withinAnyRoot 判断 real 是否落在任一 allowedRoots 之内。
// 根目录自身也先解析符号链接，再退化为 Abs 兜底（根不存在时不因此放行）。
func withinAnyRoot(real string, allowedRoots []string) bool {
	for _, root := range allowedRoots {
		trimmed := strings.TrimSpace(root)
		if trimmed == "" {
			continue
		}
		absRoot, err := filepath.Abs(trimmed)
		if err != nil {
			continue
		}
		if resolved, rerr := filepath.EvalSymlinks(absRoot); rerr == nil {
			absRoot = resolved
		}
		if isWithinRoot(real, absRoot) {
			return true
		}
	}
	return false
}

// isWithinRoot 用 filepath.Rel 判定包含关系：
// rel 为 "." 表示路径等于根自身；以 ".." 开头即越界；
// 绝对 rel（不同卷/盘符）同样视为越界。
func isWithinRoot(abs, root string) bool {
	abs = filepath.Clean(abs)
	root = filepath.Clean(root)
	rel, err := filepath.Rel(root, abs)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}
