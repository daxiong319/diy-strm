package localhash

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestResolveLocalMediaPathValid 合法路径（相对与绝对）应解析为根内真实路径。
func TestResolveLocalMediaPathValid(t *testing.T) {
	root := t.TempDir()
	realRoot := mustEvalSymlinks(t, root)
	p := writeFile(t, root, "movie.mkv", 128)

	got, err := ResolveLocalMediaPath(p, []string{root})
	if err != nil {
		t.Fatalf("合法路径应通过，got err=%v", err)
	}
	want := mustEvalSymlinks(t, p)
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	// 子目录内文件
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sp := writeFile(t, sub, "deep.mkv", 32)
	if _, err := ResolveLocalMediaPath(sp, []string{root}); err != nil {
		t.Fatalf("子目录文件应通过，got err=%v", err)
	}
	_ = realRoot

	// 多个根目录：命中第二个也应通过
	if _, err := ResolveLocalMediaPath(sp, []string{"/nonexistent-root", realRoot}); err != nil {
		t.Fatalf("多根目录命中第二个应通过，got err=%v", err)
	}
}

// TestResolveLocalMediaPathRejectsEmpty 空路径与纯空白必须拒绝。
func TestResolveLocalMediaPathRejectsEmpty(t *testing.T) {
	root := t.TempDir()
	for _, raw := range []string{"", "   ", "\t\n"} {
		if _, err := ResolveLocalMediaPath(raw, []string{root}); err == nil {
			t.Errorf("空路径 %q 应被拒绝", raw)
		}
	}
}

// TestResolveLocalMediaPathRejectsNoRoots 未配置根目录时一律拒绝（功能未启用）。
func TestResolveLocalMediaPathRejectsNoRoots(t *testing.T) {
	p := writeFile(t, t.TempDir(), "x.mkv", 16)
	if _, err := ResolveLocalMediaPath(p, nil); err == nil {
		t.Fatalf("未配置根目录时应拒绝")
	}
	if _, err := ResolveLocalMediaPath(p, []string{"", "  "}); err == nil {
		t.Fatalf("空白根目录应视为未配置并拒绝")
	}
}

// TestResolveLocalMediaPathRejectsTraversal 用 ../ 逃逸必须被拒绝。
func TestResolveLocalMediaPathRejectsTraversal(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "media")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// 根目录外的文件
	outside := writeFile(t, base, "secret.mkv", 64)

	escape := filepath.Join(root, "..", "secret.mkv")
	if _, err := ResolveLocalMediaPath(escape, []string{root}); err == nil {
		t.Fatalf("'../' 逃逸应被拒绝：%s", escape)
	}

	// 根自身是文件而非目录 → 也拒绝
	if _, err := ResolveLocalMediaPath(outside, []string{root}); err == nil {
		t.Fatalf("根目录外文件应被拒绝")
	}
}

// TestResolveLocalMediaPathRejectsSymlinkEscape 指向根外的符号链接必须被解析后拒绝。
func TestResolveLocalMediaPathRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 下创建符号链接需要特权")
	}
	base := t.TempDir()
	root := filepath.Join(base, "media")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	outside := writeFile(t, base, "secret.mkv", 64)

	link := filepath.Join(root, "escape.mkv")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("无法创建符号链接：%v", err)
	}

	// 关键：链接本身路径看起来在 root 内，但 EvalSymlinks 后落在 root 外 → 必须拒绝
	if _, err := ResolveLocalMediaPath(link, []string{root}); err == nil {
		t.Fatalf("symlink 逃逸应被拒绝（链接 %s → %s）", link, outside)
	}
}

// TestResolveLocalMediaPathAcceptsSymlinkInsideRoot 指向根内的符号链接允许通过。
func TestResolveLocalMediaPathAcceptsSymlinkInsideRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 下创建符号链接需要特权")
	}
	root := t.TempDir()
	target := writeFile(t, root, "target.mkv", 64)
	link := filepath.Join(root, "link.mkv")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("无法创建符号链接：%v", err)
	}

	got, err := ResolveLocalMediaPath(link, []string{root})
	if err != nil {
		t.Fatalf("根内符号链接应通过，got err=%v", err)
	}
	// 返回值必须是解析后的真实路径
	if got != mustEvalSymlinks(t, target) {
		t.Fatalf("got %q, want %q", got, mustEvalSymlinks(t, target))
	}
}

// TestResolveLocalMediaPathRejectsDirectory 目录必须拒绝。
func TestResolveLocalMediaPathRejectsDirectory(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "folder")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := ResolveLocalMediaPath(sub, []string{root}); err == nil {
		t.Fatalf("目录应被拒绝")
	}
	// 根目录本身（目录）也应拒绝
	if _, err := ResolveLocalMediaPath(root, []string{root}); err == nil {
		t.Fatalf("根目录自身应被拒绝（是目录）")
	}
}

// TestResolveLocalMediaPathRejectsDeviceAndFifo 设备文件与命名管道必须拒绝。
func TestResolveLocalMediaPathRejectsDeviceAndFifo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无 /dev/null 与 FIFO 语义")
	}
	root := t.TempDir()

	// /dev/null 是字符设备；为了让它落在白名单根内，用 symlink 指到根内路径。
	// 但 symlink 会被解析，所以直接用 /dev 作为白名单根来测设备文件。
	if _, err := ResolveLocalMediaPath("/dev/null", []string{"/dev"}); err == nil {
		t.Fatalf("字符设备文件应被拒绝")
	}

	// 命名管道
	fifo := filepath.Join(root, "pipe")
	if err := mkfifo(fifo); err != nil {
		t.Skipf("无法创建 FIFO：%v", err)
	}
	if _, err := ResolveLocalMediaPath(fifo, []string{root}); err == nil {
		t.Fatalf("命名管道应被拒绝")
	}
}

// TestResolveLocalMediaPathRejectsMissing 不存在的路径必须拒绝。
func TestResolveLocalMediaPathRejectsMissing(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "nope.mkv")
	if _, err := ResolveLocalMediaPath(missing, []string{root}); err == nil {
		t.Fatalf("不存在的文件应被拒绝")
	}
}

// TestResolveLocalMediaPathRejectsSiblingPrefix 前缀相同但不同目录不得误判为根内。
func TestResolveLocalMediaPathRejectsSiblingPrefix(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "media")
	sibling := filepath.Join(base, "media-evil")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := writeFile(t, sibling, "x.mkv", 32)

	if _, err := ResolveLocalMediaPath(p, []string{root}); err == nil {
		t.Fatalf("同前缀兄弟目录不应被当作根内（%s vs %s）", p, root)
	}
}

// TestIsWithinRoot 边界：相等、子路径、父路径、兄弟前缀。
func TestIsWithinRoot(t *testing.T) {
	sep := string(filepath.Separator)
	root := "/srv/media"

	cases := []struct {
		path string
		want bool
	}{
		{root, true},
		{root + sep + "a.mkv", true},
		{root + sep + "x" + sep + "y.mkv", true},
		{"/srv", false},
		{"/srv/media-evil/a.mkv", false},
		{"/srv/media/../secret.mkv", false},
		{"/other/a.mkv", false},
	}
	for _, c := range cases {
		if got := isWithinRoot(c.path, root); got != c.want {
			t.Errorf("isWithinRoot(%q, %q)=%v, want %v", c.path, root, got, c.want)
		}
	}
}

// TestResolveLocalMediaPathRelativeToRoot 相对路径应优先相对媒体根目录解释，
// 而不是相对进程 CWD（容器里 CWD=/app，直接 AbS 会必然被拒）。
func TestResolveLocalMediaPathRelativeToRoot(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "movies")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, sub, "rel.mkv", 64)

	// 相对根：movies/rel.mkv
	got, err := ResolveLocalMediaPath(filepath.Join("movies", "rel.mkv"), []string{root})
	if err != nil {
		t.Fatalf("相对根目录的路径应通过，got err=%v", err)
	}
	want := mustEvalSymlinks(t, filepath.Join(sub, "rel.mkv"))
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	// 同一相对路径在多个根里，命中第二个
	other := t.TempDir()
	if _, err := ResolveLocalMediaPath(filepath.Join("movies", "rel.mkv"), []string{other, root}); err != nil {
		t.Fatalf("应命中第二个根目录，got err=%v", err)
	}
}

// TestResolveLocalMediaPathRelativeNotFoundStillRejected 相对路径不存在时仍须拒绝。
func TestResolveLocalMediaPathRelativeNotFoundStillRejected(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveLocalMediaPath("nope/missing.mkv", []string{root}); err == nil {
		t.Fatalf("不存在的相对路径应被拒绝")
	}
}

// TestResolveLocalMediaPathRelativeTraversalEscape 相对路径里的 ../ 不能借
// 「相对根拼接」逃逸出根目录（拼接后目标真实存在时也必须拒绝）。
func TestResolveLocalMediaPathRelativeTraversalEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "media")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "secret.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	rel := filepath.Join("..", "secret.mkv")
	if _, err := ResolveLocalMediaPath(rel, []string{root}); err == nil {
		t.Fatalf("相对路径 ../ 逃逸应被拒绝（relative=%s root=%s）", rel, root)
	}
}

func mustEvalSymlinks(t *testing.T, p string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", p, err)
	}
	return real
}
