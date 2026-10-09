package rules

import "testing"

// T06 · 可配置词表的用例（任务书验收项⑦）。
//
// 这里钉的是三件事：
//  1. **默认表能干活**且标注为推测值 —— 用户不改任何配置，常见写法就要认得出；
//  2. **配置真的能改**，且是「追加」不是「替换」—— 只补一个写法不该把内置的覆盖掉；
//  3. **DVDRip 不会被误判成杜比视界** —— 这条是 T05 期间发现的真 bug，
//     单词条目必须按词边界匹配，改词表实现时最容易把它改回去。

func TestParseLexiconDefaults(t *testing.T) {
	cases := []struct{ token, want string }{
		{"4K", "2160p"},
		{"UHD", "2160p"},
		{"超高清", "2160p"},
		{"FHD", "1080p"},
		{"蓝光", "1080p"},
		{"HD", "720p"},
		{"SD", "480p"},
		{"1080p", "1080p"},
		{"720P", "720p"},
	}
	for _, tc := range cases {
		t.Run(tc.token, func(t *testing.T) {
			got, ok := NormalizeResolutionToken(tc.token)
			if !ok {
				t.Fatalf("默认词表认不出 %q", tc.token)
			}
			if got != tc.want {
				t.Fatalf("NormalizeResolutionToken(%q) = %q, 期望 %q", tc.token, got, tc.want)
			}
		})
	}

	// 认不出的 token 必须返回 ok=false，让调用方保持原值而不是猜。
	if _, ok := NormalizeResolutionToken("随便什么"); ok {
		t.Fatalf("无法识别的 token 不该归一成功")
	}
}

func TestParseLexiconEffectTexts(t *testing.T) {
	cases := []struct {
		text         string
		hdr, dv, sdr bool
	}{
		{"Movie.2023.2160p.WEB-DL.DV.HDR10", true, true, false},
		{"Movie.2023.2160p.BluRay.Dolby.Vision", false, true, false},
		{"Movie.2023.2160p.BluRay.HLG", true, false, false},
		{"Movie.2023.1080p.BluRay.DVDRip.XviD", false, false, false},
		{"Movie.2023.1080p.WEB-DL.H264", false, false, false},
		{"某剧 第二季 更新中 HDR", true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			if got := HasHDRText(tc.text); got != tc.hdr {
				t.Fatalf("HasHDRText(%q) = %v, 期望 %v", tc.text, got, tc.hdr)
			}
			if got := HasDVText(tc.text); got != tc.dv {
				t.Fatalf("HasDVText(%q) = %v, 期望 %v", tc.text, got, tc.dv)
			}
			if got := HasSDRText(tc.text); got != tc.sdr {
				t.Fatalf("HasSDRText(%q) = %v, 期望 %v", tc.text, got, tc.sdr)
			}
		})
	}
}

// TestDVDRipIsNotDolbyVision 单独立一条：这是 T05 期间真实踩到的误判。
// 旧实现是 strings.Contains("dvdrip", "dv")，于是一批 DVD 片源全被当成杜比视界，
// 而 dv / sdr 两条特效闸门都建在这个判断上 —— 一条判断错了，两条闸门一起失效。
func TestDVDRipIsNotDolbyVision(t *testing.T) {
	for _, text := range []string{
		"Movie.2023.1080p.BluRay.DVDRip.XviD",
		"Movie.2023.480p.DVD.RIP",
		"某剧.DVDRip.1080p",
	} {
		if HasDVText(text) {
			t.Fatalf("%q 被误判成杜比视界", text)
		}
	}
}

// TestParseLexiconIsConfigurable 验证四个配置项真的能改，且是追加语义。
func TestParseLexiconIsConfigurable(t *testing.T) {
	restore := ActiveParseLexicon()
	t.Cleanup(func() {
		_ = SetParseLexicon("", "", "", "")
		parseLexiconMu.Lock()
		activeLexicon = restore
		parseLexiconMu.Unlock()
	})

	if err := SetParseLexicon("蓝光原盘=2160p,xhdr=2160p", "HDR10Plus", "AtmosDV", "普通画质"); err != nil {
		t.Fatalf("SetParseLexicon 失败：%v", err)
	}

	// 新增的别名可用，内置的没被冲掉。
	if got, ok := NormalizeResolutionToken("蓝光原盘"); !ok || got != "2160p" {
		t.Fatalf("新增别名未生效：%q -> %q (ok=%v)", "蓝光原盘", got, ok)
	}
	if got, ok := NormalizeResolutionToken("XHDR"); !ok || got != "2160p" {
		t.Fatalf("新增别名未生效（大小写不敏感）：%q -> %q (ok=%v)", "XHDR", got, ok)
	}
	if got, _ := NormalizeResolutionToken("4K"); got != "2160p" {
		t.Fatalf("内置别名被配置冲掉了：4K -> %q", got)
	}

	// 新增的特效文本可用，内置的也还在。
	if !HasHDRText("Movie.2160p.HDR10Plus") {
		t.Fatalf("新增 HDR 文本未生效")
	}
	if !HasHDRText("Movie.2160p.HDR10") {
		t.Fatalf("内置 HDR 文本被配置冲掉了")
	}
	if !HasDVText("Movie.2160p.AtmosDV") {
		t.Fatalf("新增 DV 文本未生效")
	}
	if !HasSDRText("某资源 普通画质") {
		t.Fatalf("新增 SDR 文本未生效")
	}

	if LexiconRevision() == 0 {
		t.Fatalf("词表版本号没有递增")
	}
}

// TestParseLexiconRejectsBadInput 写错的配置必须报错，且**不改全局**。
func TestParseLexiconRejectsBadInput(t *testing.T) {
	before := ActiveParseLexicon()
	beforeRev := LexiconRevision()

	cases := []struct{ name, raw string }{
		{"缺等号", "蓝光原盘"},
		{"等号左侧空", "=2160p"},
		{"等号右侧空", "蓝光原盘="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := SetParseLexicon(tc.raw, "", "", "")
			if err == nil {
				t.Fatalf("%q 应当解析失败", tc.raw)
			}
			after := ActiveParseLexicon()
			if len(after.ResAliases) != len(before.ResAliases) || LexiconRevision() != beforeRev {
				t.Fatalf("解析失败时不该改动全局词表")
			}
		})
	}
}

func TestNormalizeLexiconText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Dolby.Vision", "dolby vision"},
		{"DOLBY_VISION", "dolby vision"},
		{"Dolby Vision", "dolby vision"},
		{"  HDR10  ", "hdr10"},
		{"全角　空格", "全角 空格"},
		{"A-B+C/D", "a b c d"},
		{"", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := NormalizeLexiconText(tc.in); got != tc.want {
				t.Fatalf("NormalizeLexiconText(%q) = %q, 期望 %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestLexiconMatchingIsLanguageAware 钉住匹配口径的**语种分叉**：
// ASCII 单词按词边界、CJK 条目按子串、多词 ASCII 按子串。
//
// 这是 T06 写完词表后补的设计（理由见 MatchesLexiconText 的注释）。
// 少了它会出现两种坏结果：中文自定义词配了不生效（用户从界面上看不出哪里错），
// 或者为了迁就中文词条把 ASCII 也一律改成子串匹配，于是 DVDRip 又变回杜比视界 ——
// 而 dv / sdr 两条特效闸门都建在它上面。
func TestLexiconMatchingIsLanguageAware(t *testing.T) {
	t.Cleanup(func() { _ = SetParseLexicon("", "", "", "") })
	if err := SetParseLexicon("超分辨率=2160p", "纯hd", "锐目", "平清"); err != nil {
		t.Fatalf("配置失败：%v", err)
	}
	if !HasHDRText("某片 纯HD 压制") {
		t.Fatalf("自定义 HDR 词未生效")
	}
	if !HasDVText("某片 锐目版") {
		t.Fatalf("自定义 DV 词未生效")
	}
	if !HasSDRText("某片 平清版") {
		t.Fatalf("自定义 SDR 词未生效")
	}
	if v, ok := NormalizeResolutionToken("超分辨率"); !ok || v != "2160p" {
		t.Fatalf("别名未生效：%q %v", v, ok)
	}
	if HasDVText("某片 DVDRip") {
		t.Fatalf("DVDRip 不该当成杜比视界")
	}
	// 自定义中文词条要能命中带后缀的写法（中文没有词边界）。
	if !HasDVText("某片 锐目版 2160p") || !HasHDRText("某片 纯HD版") {
		t.Fatalf("中文自定义词条应按子串命中")
	}
	// 但 ASCII 条目仍必须按词边界，DVDRip 判不出 DV。
	if HasDVText("某片 HLG DVDRip") {
		t.Fatalf("dvdrip 不该命中 ASCII 词条 dv")
	}
}
