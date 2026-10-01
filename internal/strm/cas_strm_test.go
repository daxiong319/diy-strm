package strm

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestStrm(t *testing.T, root, rel, content string) string {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return full
}

func readTestStrm(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

// 老 STRM 指向被删源文件时应被改写成 CAS 播放直链。
func TestRewriteCASStrmReferences(t *testing.T) {
	root := t.TempDir()
	fileID := "file-abc"
	old := "https://pan.example.com/api/strm/play/3/" + EncodeFileKey(fileID) + "/t/lpk_strm_x/n/%E6%B5%8B%E8%AF%95.mkv"
	oldPath := writeTestStrm(t, root, "动画/测试.strm", old)
	otherPath := writeTestStrm(t, root, "其他/别的.strm", "https://pan.example.com/api/strm/play/3/b3RoZXI=/t/tok/n/other.mkv")

	matched, updated, err := RewriteCASStrmReferences(root, fileID, 42, "测试.mkv")
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if matched != 1 || updated != 1 {
		t.Fatalf("matched=%d updated=%d, want 1/1", matched, updated)
	}
	want := "/cas/play/42/%E6%B5%8B%E8%AF%95.mkv"
	if got := readTestStrm(t, oldPath); got != want+"\n" {
		t.Fatalf("cas strm content = %q, want %q", got, want+"\n")
	}
	if got := readTestStrm(t, otherPath); got == want+"\n" {
		t.Fatalf("unrelated strm was rewritten")
	}
}

// 无匹配时不应报错，也不应改动任何文件。
func TestRewriteCASStrmReferencesNoMatch(t *testing.T) {
	root := t.TempDir()
	path := writeTestStrm(t, root, "动画/测试.strm", "https://pan.example.com/api/strm/play/3/bm9wZQ==/t/tok/n/no.mkv")
	matched, updated, err := RewriteCASStrmReferences(root, "missing-id", 1, "no.mkv")
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if matched != 0 || updated != 0 {
		t.Fatalf("matched=%d updated=%d, want 0/0", matched, updated)
	}
	if got := readTestStrm(t, path); got == "" {
		t.Fatalf("file emptied")
	}
}

// CAS 播放 .strm 必须能识别，且所在目录应被判定为含 CAS 内容。
func TestIsCasPlayStrmFile(t *testing.T) {
	root := t.TempDir()
	casPath := writeTestStrm(t, root, "a/cas.strm", "/cas/play/7/film.mkv")
	plainPath := writeTestStrm(t, root, "a/plain.strm", "https://pan.example.com/api/strm/play/3/eA==/t/tok/n/f.mkv")

	if !isCasPlayStrmFile(casPath) {
		t.Fatalf("cas strm not detected")
	}
	if isCasPlayStrmFile(plainPath) {
		t.Fatalf("plain strm misdetected as cas")
	}
	if !dirContainsCasPlayStrm(filepath.Dir(casPath)) {
		t.Fatalf("dir with cas strm not detected")
	}
	if dirContainsCasPlayStrm(t.TempDir()) {
		t.Fatalf("empty dir reported as containing cas strm")
	}
}

// pruneMissingRemoteDir 只清普通 STRM，保留 CAS 播放 .strm 及其目录。
func TestPruneMissingRemoteDirKeepsCas(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "剧集")
	casPath := writeTestStrm(t, root, "剧集/剧集.strm", "/cas/play/9/ep01.mkv")
	plainPath := writeTestStrm(t, root, "剧集/旧的.strm", "https://pan.example.com/api/strm/play/3/b2xk/t/tok/n/old.mkv")

	removed, err := pruneMissingRemoteDir(dir)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed=%d, want 1 (plain only)", removed)
	}
	if _, err := os.Stat(casPath); err != nil {
		t.Fatalf("cas strm was deleted: %v", err)
	}
	if _, err := os.Stat(plainPath); !os.IsNotExist(err) {
		t.Fatalf("plain strm survived")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("dir containing cas strm was deleted: %v", err)
	}
}
