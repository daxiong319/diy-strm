package connector

import (
	"testing"

	"litepan/internal/discover/identity"
)

// 本文件钉住 T05 从原始资源字段到结构化 Item 的归一化口径。
//
// 归一化是接口化的地基：连接器把各家的字段名抹平成 Item，
// 闸门和跨源排序只认 Item。归一化错了，后面两层全错，而且很难查 ——
// 因为「一条 1080p 被 4K 闸门放行」这种错误只在特定资源上出现。

func TestClassifyKind(t *testing.T) {
	cases := map[string]ItemKind{
		"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567": ItemMagnet,
		"ed2k://|file|a.mkv|100|0123456789ABCDEF0123456789ABCDEF|/":    ItemEd2k,
		"magnet:?dn=x&xt=urn:btih:4a6qzt7x6bnv3q2hh2m6mjqyxw4jjjusy":   ItemMagnet,
		"ab12": ItemShareLink, // 裸分享码没有前缀，归默认
		"":     ItemShareLink, // RE0 的 PanType 为空即分享
	}
	for raw, want := range cases {
		if got := ClassifyKind(raw); got != want {
			t.Fatalf("ClassifyKind(%q) = %q，期望 %q", raw, got, want)
		}
	}
}

func TestValidShareCodeAndMagnet(t *testing.T) {
	if !ValidShareCode("abc123") || !ValidShareCode("A1") {
		t.Fatalf("合法分享码被拒")
	}
	if ValidShareCode("abc-123") || ValidShareCode("") {
		t.Fatalf("含连字符或空的分享码应被拒")
	}
	if !ValidMagnet("magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567") {
		t.Fatalf("40 位 hex 磁力应合法")
	}
	if !ValidMagnet("magnet:?xt=urn:btih:4a6qzt7x6bnv3q2hh2m6mjqyxw4jjjus") {
		t.Fatalf("32 位 base32 磁力应合法")
	}
	if ValidMagnet("magnet:?dn=x") {
		t.Fatalf("没有 btih 的磁力应被拒")
	}
	if ValidMagnet("http://example.com/a.torrent") {
		t.Fatalf("非磁力链接应被拒")
	}
}

// TestNormalizeInfoHashBase32 32 位 base32 磁力（BTIH 常见形态）也要归一成
// 40 位小写 hex，否则同一个种子在两个源里会算出两个不同的去重键。
func TestNormalizeInfoHashBase32(t *testing.T) {
	got := NormalizeInfoHash("4a6qzt7x6bnv3q2hh2m6mjqyxw4jjjus")
	if len(got) != 40 {
		t.Fatalf("base32 应归一成 40 位 hex，实际 %q（长度 %d）", got, len(got))
	}
	if got != "e03d0ccff7f05b5dc3473e99e62618bdb894a692" {
		t.Fatalf("base32 解码结果不对：%q", got)
	}
	if got != NormalizeInfoHash("4A6QZT7X6BNV3Q2HH2M6MJQYXW4JJJUS") {
		t.Fatalf("base32 大小写归一后应一致")
	}
	if NormalizeInfoHash("not-a-hash") != "" {
		t.Fatalf("非法哈希应归一成空串")
	}
	if NormalizeInfoHash("ABCDEF0123456789ABCDEF0123456789ABCDEF01") !=
		"abcdef0123456789abcdef0123456789abcdef01" {
		t.Fatalf("40 位 hex 应归一成小写")
	}
}

func TestParseSizeBytes(t *testing.T) {
	cases := map[string]int64{
		"":          0,
		"12.5 GB":   12800 * 1024 * 1024,
		"12.5GB":    12800 * 1024 * 1024,
		"1.2 TB":    1319413953331, // 1.2 × 2^40
		"800 MB":    800 * 1024 * 1024,
		"800MB":     800 * 1024 * 1024,
		"1.2T":      1319413953331,
		"2.5 gb":    2560 * 1024 * 1024, // 大小写不敏感
		"0":         0,
		"unknown":   0,
		"约 3.2 GB":  3435973836, // 3.2 × 2^30，容忍中文前缀
		"大小: 3.2GB": 3435973836,
		"12800":     0, // 纯数字不带单位：源没给单位时按字节解释会得到荒谬的巨大值，宁可当未知
		"3.2 GiB":   3435973836,
	}
	for raw, want := range cases {
		if got := ParseSizeBytes(raw); got != want {
			t.Fatalf("ParseSizeBytes(%q) = %d，期望 %d", raw, got, want)
		}
	}
}

// TestApplySpecTagsBeatsTitle 站方标签优先于标题解析。
// 这是刻意的：标题里写 4K 而站点标签写 1080p 时，站点的判定更可信 ——
// 它对应的是盘方实际存的那份文件，标题是压制组随手写的。
func TestApplySpecTagsBeatsTitle(t *testing.T) {
	it := Item{Title: "Movie.2023.2160p.WEB-DL.mkv"}
	ApplyQuality(&it)
	if it.Resolution != 2160 {
		t.Fatalf("标题解析应识别出 2160，实际 %d", it.Resolution)
	}

	it2 := Item{Title: "Movie.2023.2160p.WEB-DL.mkv"}
	ApplySpecTags(&it2, []string{"1080p"})
	ApplyQuality(&it2)
	if it2.Resolution != 1080 {
		t.Fatalf("站方标签应覆盖标题解析，实际 %d", it2.Resolution)
	}
}

func TestApplyQualityDetectsHDRAndCodec(t *testing.T) {
	it := Item{Title: "Show.S01E01.2160p.HEVC.DV.HDR10+.TrueHD.Atmos.DTS-HD.MA"}
	ApplyQuality(&it)
	if it.Resolution != 2160 {
		t.Fatalf("分辨率识别失败：%d", it.Resolution)
	}
	if it.Codec != "hevc" && it.Codec != "h265" {
		t.Fatalf("编码识别失败：%q", it.Codec)
	}
	if !it.HasHDR {
		t.Fatalf("HDR 未识别")
	}
	if !it.HasAtmos {
		t.Fatalf("Atmos 未识别")
	}
	if !it.HasDTS {
		t.Fatalf("DTS 未识别")
	}
}

// TestApplyQualityDVFallback DV 有时只写成 "DV"，ParseQualityFromName 会归到 HDR；
// 这里钉住「标 DV 必然 HasHDR」这个不变式 —— 订阅的 sdr 闸门依赖它。
func TestApplyQualityDVFallback(t *testing.T) {
	it := Item{Title: "Movie.2023.2160p.DV.HDR10"}
	ApplyQuality(&it)
	if !it.HasHDR {
		t.Fatalf("DV 候选必须同时标为 HDR，否则 sdr 闸门会漏放")
	}
}

// TestApplyQualityNoResolutionLeavesZero 标题里完全没有分辨率线索时必须留 0，
// 不能瞎猜 1080。闸门遇到 0 是放行 + 记「信息不全」，猜错则是把 4K 拒了。
func TestApplyQualityNoResolutionLeavesZero(t *testing.T) {
	it := Item{Title: "Some Movie"}
	ApplyQuality(&it)
	if it.Resolution != 0 {
		t.Fatalf("无法判定分辨率时应留 0，实际 %d", it.Resolution)
	}
}

func TestMetadataManifestEvidence(t *testing.T) {
	it := Item{Title: "Breaking Bad", Remark: "第一季"}
	m := MetadataManifest(it)
	if m.Kind != identity.EvidenceMetadata {
		t.Fatalf("分享候选应给 metadata 档，实际 %v", m.Kind)
	}
	if len(m.Titles) == 0 {
		t.Fatalf("metadata 档必须带候选侧标题作为证据来源")
	}
	if m.Kind == identity.EvidenceManifest && len(m.Files) == 0 {
		t.Fatalf("manifest 档必须带文件清单")
	}
	if m.Kind == identity.EvidenceMetadata && m.ListingSucceeded {
		t.Fatalf("metadata 档没查过清单，不该声称清单成功")
	}
}

// TestMetadataManifestMagnetNeedsResolve 磁力/ed2k 拿不到文件清单，
// 必须落在 EvidenceNone（由调用方交给 T03 的身份闸门判不可转存），
// 而不是伪造一个 metadata 档 —— 那会让「未通过身份校验，一律不转存」失效。
func TestMetadataManifestMagnetNeedsResolve(t *testing.T) {
	m := MetadataManifest(Item{Kind: ItemMagnet, Title: "Movie"})
	if m.Kind != identity.EvidenceNone {
		t.Fatalf("磁力候选不该有 metadata 档，实际 %v", m.Kind)
	}
	if DefaultNeedsResolve(Item{Kind: ItemMagnet}) != true {
		t.Fatalf("磁力候选必须先 Resolve")
	}
}
