package casengine

import (
	"strings"
	"testing"

	"diy-strm/internal/cloud189"
	"diy-strm/internal/models"
)

// TestDeriveRapidDriveTypes 五哈希 → 可秒传盘推导
func TestDeriveRapidDriveTypes(t *testing.T) {
	cases := []struct {
		name string
		hs   cloud189.HashSet
		want []string
	}{
		{"天翼", cloud189.HashSet{FileMd5: "a", SliceMd5: "b"}, []string{"cloud189"}},
		{"移动", cloud189.HashSet{Sha256: "a"}, []string{"cloud139"}},
		{"夸克", cloud189.HashSet{FileMd5: "a", PreHash: "b"}, []string{"cloud189", "quark"}},
		{"全量", cloud189.HashSet{FileMd5: "a", SliceMd5: "b", Sha256: "c", PreHash: "d"}, []string{"cloud189", "cloud139", "quark"}},
		{"空", cloud189.HashSet{}, []string{}},
	}
	for _, tc := range cases {
		got := deriveRapidDriveTypes(tc.hs)
		if got == "" && len(tc.want) == 0 {
			continue
		}
		if got != strings.Join(tc.want, ",") {
			t.Fatalf("%s: deriveRapidDriveTypes = %q, want %q", tc.name, got, strings.Join(tc.want, ","))
		}
	}
}

// TestBuildRapidPayload 秒传请求原文构造（各盘）
func TestBuildRapidPayload(t *testing.T) {
	task := models.DbUploadTask{}
	task.FileName = "test.mkv"
	task.FileSize = 100
	hs := cloud189.HashSet{FileMd5: "AA", SliceMd5: "BB", Sha256: "CC", PreHash: "DD"}

	p139 := buildRapidPayload(models.SourceTypePan139, task, hs)
	if !strings.Contains(p139, "cloud139") || !strings.Contains(p139, "contentHashAlgorithm") || !strings.Contains(p139, "SHA256") {
		t.Fatalf("139 payload 错误：%s", p139)
	}
	p189 := buildRapidPayload(models.SourceTypeCloud189, task, hs)
	if !strings.Contains(p189, "cloud189") || !strings.Contains(p189, "fileMd5") || !strings.Contains(p189, "sliceMd5") {
		t.Fatalf("天翼 payload 错误：%s", p189)
	}
	pq := buildRapidPayload(models.SourceTypeQuark, task, hs)
	if !strings.Contains(pq, "quark") || !strings.Contains(pq, "pre_hash") {
		t.Fatalf("夸克 payload 错误：%s", pq)
	}
}
