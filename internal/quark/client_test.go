package quark

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRapidUploadNeedsPreHash 秒传必须带逗号分隔的 preHash 分块串
func TestRapidUploadNeedsPreHash(t *testing.T) {
	c := NewClient("__pus=x;__puus=y")
	// preHash 无逗号 → 报错（无需真联网）
	_, err := c.RapidUpload(nil, "0", "a.mkv", 100, "abcdef")
	if err == nil || !strings.Contains(err.Error(), "逗号分隔") {
		t.Fatalf("应报 preHash 格式错误：%v", err)
	}
}

// TestPreSliceConstants 分块预检参数固定 4×4MB×4
func TestPreSliceConstants(t *testing.T) {
	if PreSliceSize != 4*1024*1024 {
		t.Fatalf("PreSliceSize 应为 4MB")
	}
	if PreSliceCount != 4 {
		t.Fatalf("PreSliceCount 应为 4")
	}
}

// TestAnyHelpers 宽容类型提取
func TestAnyHelpers(t *testing.T) {
	if anyString("abc") != "abc" || anyString(nil) != "" {
		t.Fatal("anyString 错误")
	}
	if anyFloat(float64(3.5)) != 3.5 || anyFloat("4") != 4 {
		t.Fatal("anyFloat 错误")
	}
	if !anyBool(true) || anyBool(false) {
		t.Fatal("anyBool 错误")
	}
	if firstNonEmpty("", "b", "c") != "b" {
		t.Fatal("firstNonEmpty 错误")
	}
}

// TestBuildPreHash 前 4×4MB 分块 MD5 拼接（与 md5 逐块计算对齐）
func TestBuildPreHash(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "test.bin")
	// 写一个 6MB 文件（> 4MB，验证 2 块场景）
	data := make([]byte, 6*1024*1024)
	for i := range data {
		data[i] = byte(i % 251)
	}
	if err := os.WriteFile(fp, data, 0644); err != nil {
		t.Fatal(err)
	}
	preHash, err := BuildPreHash(fp, int64(len(data)))
	if err != nil {
		t.Fatalf("BuildPreHash 失败：%v", err)
	}
	parts := strings.Split(preHash, ",")
	if len(parts) != 2 {
		t.Fatalf("6MB 应 2 块，实际 %d 块：%s", len(parts), preHash)
	}
	// 逐块 MD5 校验
	for i := 0; i < 2; i++ {
		start := i * 4 * 1024 * 1024
		end := start + 4*1024*1024
		if end > len(data) {
			end = len(data)
		}
		h := md5.Sum(data[start:end])
		want := hex.EncodeToString(h[:])
		if parts[i] != want {
			t.Fatalf("第 %d 块 MD5 不符：got=%s want=%s", i, parts[i], want)
		}
	}
}

// TestBuildPreHashSmallFile 小文件（< 4MB）单块
func TestBuildPreHashSmallFile(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "small.bin")
	data := []byte("hello quark prehash")
	if err := os.WriteFile(fp, data, 0644); err != nil {
		t.Fatal(err)
	}
	preHash, err := BuildPreHash(fp, int64(len(data)))
	if err != nil {
		t.Fatalf("BuildPreHash 失败：%v", err)
	}
	if strings.Contains(preHash, ",") {
		t.Fatalf("小文件应单块，实际 %s", preHash)
	}
	h := md5.Sum(data)
	if preHash != hex.EncodeToString(h[:]) {
		t.Fatalf("小文件 MD5 不符：got=%s", preHash)
	}
}
