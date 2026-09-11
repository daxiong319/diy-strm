package discovery

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"testing"
)

// TestTorrentBytesToMagnet 最小 bencode 种子 → 磁力（v1 infohash）
func TestTorrentBytesToMagnet(t *testing.T) {
	info := []byte("d6:lengthi1e4:name4:test12:piece lengthi16384e6:pieces0:e")
	content := "d4:info" + string(info) + "e"
	magnet, err := TorrentBytesToMagnet([]byte(content))
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	sum := sha1.Sum(info)
	want := "magnet:?xt=urn:btih:" + hex.EncodeToString(sum[:]) + "&dn=test"
	if magnet != want {
		t.Fatalf("磁力不符：got %s want %s", magnet, want)
	}
}

// TestTorrentBytesToMagnetV2Unsupported v2-only 种子应拒绝
func TestTorrentBytesToMagnetV2Unsupported(t *testing.T) {
	info := []byte("d9:file treed4:name4:testee4:name4:teste")
	content := "d4:info" + string(info) + "e"
	if _, err := TorrentBytesToMagnet([]byte(content)); err == nil {
		t.Fatalf("v2-only 种子应报错")
	}
}

// TestExtractEpisodeEvidence 季集证据解析
func TestExtractEpisodeEvidence(t *testing.T) {
	media, ep := extractEpisodeEvidence("某剧 S01E05 第5集", "")
	if media != "tv" || ep == nil || ep.SeasonNum == nil || ep.EpisodeNum == nil {
		t.Fatalf("S01E05 解析失败：%v %v", media, ep)
	}
	if *ep.SeasonNum != 1 || *ep.EpisodeNum != 5 {
		t.Fatalf("季集错误：%d %d", *ep.SeasonNum, *ep.EpisodeNum)
	}
	media, ep = extractEpisodeEvidence("某剧 S01E05-E08", "")
	if media != "tv" || ep == nil || ep.EndEpisodeNum == nil || *ep.EndEpisodeNum != 8 {
		t.Fatalf("区间解析失败：%v", ep)
	}
	media, _ = extractEpisodeEvidence("某个电影 2023.1080p", "")
	if media != "movie" {
		t.Fatalf("电影误判为剧集：%s", media)
	}
}

// TestScoreSubjectCandidate 匹配评分
func TestScoreSubjectCandidate(t *testing.T) {
	score := scoreSubjectCandidate("交锋", "Clash", 2024, "交锋", "Clash", 2024)
	if score < 3 {
		t.Fatalf("完全一致应得高分：%f", score)
	}
	score = scoreSubjectCandidate("完全无关", "Other", 1999, "交锋", "Clash", 2024)
	if score > 0.5 {
		t.Fatalf("无关条目不应得分：%f", score)
	}
	score = scoreSubjectCandidate("交锋 第二季", "Clash S2", 2026, "交锋", "", 2024)
	if score < 1.5 {
		t.Fatalf("前缀包含+年份差一年应过阈值：%f", score)
	}
}

// TestNormalizeTransferProvider 目标网盘归一
func TestNormalizeTransferProvider(t *testing.T) {
	for raw, want := range map[string]string{
		"123": "123", "guangyapan": "guangya", "GUANGYA": "guangya", "pan139": "pan139", "139": "pan139",
	} {
		got, err := NormalizeTransferProvider(raw)
		if err != nil || got != want {
			t.Fatalf("%s → %v, %v", raw, got, err)
		}
	}
	if _, err := NormalizeTransferProvider("115"); err == nil {
		t.Fatalf("115 应报未接入错误")
	}
	if _, err := NormalizeTransferProvider(""); err == nil {
		t.Fatalf("空网盘应报错")
	}
}

// TestTGSearchTerms 搜索词构造
func TestTGSearchTerms(t *testing.T) {
	terms := tgSearchTerms("交锋", []string{"Clash", "交锋", "x", ""})
	if len(terms) != 2 {
		t.Fatalf("应为 2 个去重关键词：%v", terms)
	}
	if terms[0] != "交锋" || terms[1] != "Clash" {
		t.Fatalf("顺序错误：%v", terms)
	}
}

// TestHashStringStable 键哈希稳定
func TestHashStringStable(t *testing.T) {
	if hashString("test") != hashString("test") {
		t.Fatalf("哈希不稳定")
	}
	if hashString("a") == hashString("b") {
		t.Fatalf("哈希碰撞")
	}
}

// TestScanHeatObjects 猫眼容错解析（递归扫描标题+热度）
func TestScanHeatObjects(t *testing.T) {
	payload := `{"code":1,"data":{"list":[{"subjectName":"某剧","heatValue":1234.5,"releaseDate":"2024-05-01"},{"title":"某综","heat":99,"year":"2023"}]}}`
	var root any
	if err := jsonUnmarshal([]byte(payload), &root); err != nil {
		t.Fatal(err)
	}
	entries := scanHeatObjects(root, 0)
	if len(entries) != 2 {
		t.Fatalf("应识别 2 条：%d", len(entries))
	}
	if entries[0].Title != "某剧" || entries[0].Heat != 1234.5 {
		t.Fatalf("条目解析错误：%+v", entries[0])
	}
	if entries[0].Year != 2024 {
		t.Fatalf("年份应从 releaseDate 提取：%d", entries[0].Year)
	}
}

// TestTorrentBytesToMagnetTooLarge 超限拒绝
func TestTorrentBytesToMagnetTooLarge(t *testing.T) {
	big := make([]byte, (4<<20)+10)
	if _, err := TorrentBytesToMagnet(big); err == nil {
		t.Fatalf("超大种子应报错")
	}
	if !strings.Contains(TorrentNameForTest(), "占位") {
		// 仅保证函数被引用
		_ = torrentNameRe
	}
}

func TorrentNameForTest() string { return "占位" }
