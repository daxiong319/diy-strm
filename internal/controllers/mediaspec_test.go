package controllers

import "testing"

func TestParseMediaSpec(t *testing.T) {
	cases := []struct {
		text string
		want MediaSpec
	}{
		{"星际穿越.Interstellar.2024.2160p.BluRay.REMUX.H265.Dolby.Vision.50GB",
			MediaSpec{Resolution: 3, Source: 5, Codec: 2, Effect: 3, SizeGB: 50}},
		{"某片.2023.1080p.WEB-DL.H264.HDR.2.5GB",
			MediaSpec{Resolution: 2, Source: 3, Codec: 1, Effect: 2, SizeGB: 2.5}},
		{"老片.1999.720p.HDTV.X264",
			MediaSpec{Resolution: 1, Source: 1, Codec: 1, Effect: 1, SizeGB: 0}},
		{"剧集.S01E05.2160p.HEVC.DV.10bit",
			MediaSpec{Resolution: 3, Source: 0, Codec: 2, Effect: 3, SizeGB: 0}},
		{"普通视频", MediaSpec{Resolution: 0, Source: 0, Codec: 0, Effect: 1, SizeGB: 0}},
		{"大片.2024.4K.BluRay.x265.HDR10.HDR.1TB",
			MediaSpec{Resolution: 3, Source: 4, Codec: 2, Effect: 2, SizeGB: 1024}},
		{"影.2022.WEBRip.H264.800MB", MediaSpec{Resolution: 0, Source: 2, Codec: 1, Effect: 1, SizeGB: 0}},
		{"DVDRip 版本", MediaSpec{Resolution: 0, Source: 1, Codec: 0, Effect: 1, SizeGB: 0}},
	}
	for _, c := range cases {
		got := ParseMediaSpec(c.text)
		if got != c.want {
			t.Errorf("ParseMediaSpec(%q) = %+v, want %+v", c.text, got, c.want)
		}
	}
}

func TestWashScores(t *testing.T) {
	a := MediaSpec{Resolution: 3, Source: 5, Codec: 2, Effect: 3, SizeGB: 50}
	b := MediaSpec{Resolution: 2, Source: 4, Codec: 2, Effect: 2, SizeGB: 20}
	if !a.BetterThan(b) {
		t.Errorf("4K REMUX 应优于 1080p BluRay")
	}
	if b.BetterThan(a) {
		t.Errorf("1080p 不应优于 4K")
	}
	// 1080p + H265 (20100) vs 1080p + H264 (20000)：编码低一级、体积大应更优
	c := MediaSpec{Resolution: 2, Source: 3, Codec: 1, Effect: 1, SizeGB: 10}
	d := MediaSpec{Resolution: 2, Source: 3, Codec: 2, Effect: 1, SizeGB: 5}
	if !d.BetterThan(c) {
		t.Errorf("H265 应优于 H264")
	}
	// 目标判定
	if WashTargetScore("4k_remux") != 35000 {
		t.Errorf("4k_remux 目标分应为 35000")
	}
	if WashTargetScore("1080p") != 20000 {
		t.Errorf("1080p 目标分应为 20000")
	}
	if WashTargetScore("") != 0 {
		t.Errorf("无目标应为 0")
	}
}
