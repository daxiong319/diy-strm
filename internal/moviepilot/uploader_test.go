package moviepilot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalDirFingerprint(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "BDMV", "STREAM"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("BDMV/STREAM/00000.m2ts", "aaa")
	mustWrite("BDMV/STREAM/00000.m2ts.!qB", "bbb")
	mustWrite("BDMV/index.bdmv", "c")

	fp1, err := localDirFingerprint(root)
	if err != nil {
		t.Fatal(err)
	}
	// 内容不变（含临时未完成文件）指纹一致
	fp1b, _ := localDirFingerprint(root)
	if fp1 != fp1b {
		t.Errorf("指纹应一致：%q vs %q", fp1, fp1b)
	}
	// 写入中的大文件 size 变化 → 指纹变化
	mustWrite("BDMV/STREAM/00000.m2ts", "aaaX")
	fp2, err := localDirFingerprint(root)
	if err != nil {
		t.Fatal(err)
	}
	if fp1 == fp2 {
		t.Errorf("size 变化但指纹未变：%q", fp1)
	}
	// 临时文件落盘为正式文件（.!qB 消失、正式文件出现）→ 指纹变化
	if err := os.Remove(filepath.Join(root, "BDMV/STREAM/00000.m2ts.!qB")); err != nil {
		t.Fatal(err)
	}
	fp3, _ := localDirFingerprint(root)
	if fp2 == fp3 {
		t.Errorf("临时文件落盘后指纹应变化")
	}
}