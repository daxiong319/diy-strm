package cas

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"litepan/internal/cas/cloud189"
	"litepan/internal/discover/ddb"
)

func setupLocalManifestDB(t *testing.T) {
	t.Helper()
	if ddb.Db != nil {
		return
	}
	if err := ddb.Init(filepath.Join(t.TempDir(), "t.db"), nil); err != nil {
		t.Skipf("无法初始化测试库，跳过：%v", err)
	}
	if err := ddb.Db.AutoMigrate(&CasManifestRecord{}); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
}

// 每个归一化网盘类型都必须产出非空 payload，且落库后是可解析的合法 JSON。
func TestBuildLocalManifestPerDrivePayload(t *testing.T) {
	setupLocalManifestDB(t)
	hs := cloud189.HashSet{
		FileMd5:  "bee46317da4862449b09e107d3a95c23",
		SliceMd5: "feedfacefeedfacefeedfacefeedface",
		Sha1:     "0123456789abcdef0123456789abcdef01234567",
		Sha256:   "aa11bb22cc33dd44ee55ff6677889900aa11bb22cc33dd44ee55ff6677889900",
	}
	// 光鸭无 payload 分支，已在 BuildLocalManifest 降级，单列在
	// TestBuildLocalManifestUnsupportedDriveDegrades 里断言。
	for _, drive := range []string{"123_open", "cloud189", "cloud139", "quark"} {
		res, err := BuildLocalManifest("demo.mkv", 4852464416, hs, 3, drive)
		if err != nil {
			t.Fatalf("drive %s: %v", drive, err)
		}
		if !res.Restorable {
			t.Fatalf("drive %s: 指定了账号+类型应可恢复", drive)
		}
		if res.RapidDriveTypes == "" {
			t.Fatalf("drive %s: RapidDriveTypes 为空", drive)
		}
		var rec CasManifestRecord
		if err := ddb.Db.First(&rec, res.ID).Error; err != nil {
			t.Fatalf("drive %s: 回查失败 %v", drive, err)
		}
		if rec.SourceType != drive {
			t.Fatalf("drive %s: SourceType 落库为 %q（应为真实网盘类型，不能是 local）", drive, rec.SourceType)
		}
		if rec.AccountID != 3 {
			t.Fatalf("drive %s: AccountID 落库为 %d（应保留真实账号）", drive, rec.AccountID)
		}
		if rec.RapidPayload == "" {
			t.Fatalf("drive %s: RapidPayload 为空 → restore 会跳过重放", drive)
		}
		var probe map[string]any
		if err := json.Unmarshal([]byte(rec.RapidPayload), &probe); err != nil {
			t.Fatalf("drive %s: RapidPayload 非法 JSON：%v", drive, err)
		}
		t.Logf("drive=%-9s rapid=%-26s payload_keys=%d", drive, res.RapidDriveTypes, len(probe))
	}
}

// 未指定目标盘：不标可恢复、不伪造 payload，但清单内容仍可用。
func TestBuildLocalManifestWithoutTarget(t *testing.T) {
	setupLocalManifestDB(t)
	hs := cloud189.HashSet{FileMd5: "bee46317da4862449b09e107d3a95c23"}
	for _, drive := range []string{"", "local", "localfs"} {
		res, err := BuildLocalManifest("demo.mkv", 100, hs, 0, drive)
		if err != nil {
			t.Fatalf("drive %q: %v", drive, err)
		}
		if res.Restorable {
			t.Fatalf("drive %q: 不应标记可恢复", drive)
		}
		if res.CasContent == "" {
			t.Fatalf("drive %q: 清单内容仍应可携带", drive)
		}
		var rec CasManifestRecord
		if err := ddb.Db.First(&rec, res.ID).Error; err != nil {
			t.Fatalf("drive %q: 回查失败 %v", drive, err)
		}
		if rec.SourceType == "localfs" || rec.SourceType == "local" {
			t.Fatalf("drive %q: 绝不能落成 %q（restore 解析不到驱动）", drive, rec.SourceType)
		}
		if rec.RapidPayload != "" {
			t.Fatalf("drive %q: 无目标盘时不应产出 payload", drive)
		}
	}
}

// 无 payload 分支的盘（光鸭）必须被降级为不可恢复，而不是产出死记录。
func TestBuildLocalManifestUnsupportedDriveDegrades(t *testing.T) {
	setupLocalManifestDB(t)
	hs := cloud189.HashSet{FileMd5: "bee46317da4862449b09e107d3a95c23"}
	res, err := BuildLocalManifest("demo.mkv", 100, hs, 3, "guangya")
	if err != nil {
		t.Fatalf("不应报错，应降级：%v", err)
	}
	if res.Restorable {
		t.Fatalf("光鸭无 payload 分支，不得标记为可恢复（否则是永远恢复不了的死记录）")
	}
	var rec CasManifestRecord
	if err := ddb.Db.First(&rec, res.ID).Error; err != nil {
		t.Fatalf("回查失败：%v", err)
	}
	if rec.RapidPayload != "" {
		t.Fatalf("不应产出 payload，实得 %q", rec.RapidPayload)
	}
	if rec.SourceType == "guangya" {
		t.Fatalf("SourceType 不应是 guangya（restore 会取驱动但无 payload 可重放）")
	}
}

// 夸克秒传需要 md5+sha1（drivers/Quark/upload.go:696），payload 必须带上 sha1，
// 否则重放时报「夸克秒传缺少 md5/sha1 特征」。
func TestQuarkPayloadCarriesSha1(t *testing.T) {
	hs := cloud189.HashSet{
		FileMd5: "bee46317da4862449b09e107d3a95c23",
		Sha1:    "0123456789abcdef0123456789abcdef01234567",
	}
	payload := BuildRapidPayloadFor("quark", "demo.mkv", 100, hs)
	if payload == "" {
		t.Fatalf("quark 应有 payload 分支")
	}
	var probe struct {
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal([]byte(payload), &probe); err != nil {
		t.Fatalf("payload 非法 JSON：%v", err)
	}
	if got, _ := probe.Params["sha1"].(string); got != hs.Sha1 {
		t.Fatalf("quark payload 必须携带 sha1，实得 %#v（payload=%s）", probe.Params["sha1"], payload)
	}
	if got, _ := probe.Params["file_md5"].(string); got != hs.FileMd5 {
		t.Fatalf("quark payload 必须携带 file_md5，实得 %#v", probe.Params["file_md5"])
	}
}
