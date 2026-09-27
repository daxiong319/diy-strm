package quark

import (
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
