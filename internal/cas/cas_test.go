package cas

import (
	"testing"

	"litepan/internal/cas/cloud189"
)

func TestManifestRoundTrip(t *testing.T) {
	m := cloud189.CasManifestV2{Version: 2, FileName: "a.mkv", FileSize: 123,
		Hashes: cloud189.HashSet{FileMd5: "ABC", SliceMd5: "DEF", Sha256: "FFF", PreHash: "PRE"}}
	enc := cloud189.EncodeManifestV2(m)
	dec, err := cloud189.ParseManifestV2(enc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if dec.FileName != "a.mkv" || dec.FileSize != 123 {
		t.Errorf("roundtrip mismatch: %+v", dec)
	}
	if dec.Hashes.FileMd5 != "ABC" || dec.Hashes.PreHash != "PRE" {
		t.Errorf("hash mismatch: %+v", dec.Hashes)
	}
}

func TestDeriveRapidDriveTypes(t *testing.T) {
	hs := cloud189.HashSet{FileMd5: "a", SliceMd5: "b", Sha256: "c", PreHash: "d"}
	got := DeriveRapidDriveTypes(hs)
	if got != "cloud189,cloud139,quark,123_open" {
		t.Errorf("got %q", got)
	}
}

func TestBuildRapidPayload(t *testing.T) {
	hs := cloud189.HashSet{FileMd5: "md5", SliceMd5: "slice"}
	p := BuildRapidPayloadFor("189cloud", "x.mkv", 99, hs)
	if p == "" {
		t.Error("empty payload")
	}
	if !contains(p, "cloud189") {
		t.Errorf("payload missing drive_type: %s", p)
	}
}

func TestCasConfigDefaults(t *testing.T) {
	cfg := getConfig()
	if !cfg.Enabled {
		t.Error("默认应当 enabled")
	}
	if cfg.AgeDays != 30 {
		t.Errorf("AgeDays 默认应为 30, got %d", cfg.AgeDays)
	}
	if cfg.DelayDeleteHours != 2 {
		t.Errorf("DelayDeleteHours 默认应为 2, got %d", cfg.DelayDeleteHours)
	}
	if !cfg.DeleteSource {
		t.Error("DeleteSource 默认应为 true")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestBuildRapidPayload123(t *testing.T) {
	hs := cloud189.HashSet{FileMd5: "abc123"}
	p := BuildRapidPayloadFor("123_open", "x.mkv", 99, hs)
	if p == "" {
		t.Fatal("123_open payload 不应为空")
	}
	if !contains(p, "123_open") || !contains(p, "abc123") {
		t.Errorf("123_open payload 缺 md5 特征：%s", p)
	}
}
