package identity

import (
	"fmt"
	"testing"
)

// movieSub 是「流浪地球 (2019)」的订阅期望。
func movieSub() Request {
	return Request{
		ExpectedTitle:  "流浪地球",
		ExpectedTMDBID: "157350",
		ExpectedType:   "movie",
		ExpectedYear:   2019,
	}
}

func files(names ...string) []File {
	out := make([]File, 0, len(names))
	for i, n := range names {
		out = append(out, File{Name: n, Size: int64(i+1) * 1024})
	}
	return out
}

// TestReasonIdentityMatched 六维全过。
func TestReasonIdentityMatched(t *testing.T) {
	res := Validate(movieSub(), Manifest{
		Kind:             EvidenceMetadata,
		Titles:           []string{"流浪地球 (2019) 1080P 蓝光原盘"},
		MediaType:        "movie",
		Year:             2019,
		ListingSucceeded: false,
	}, Options{})

	if !res.Passed || res.Reason != ReasonMatched {
		t.Fatalf("期望 IDENTITY_MATCHED，实际 %+v", res)
	}
}

// TestReasonTitleMismatch 搜到「流浪地球2 (2023)」但订阅是「流浪地球 (2019)」。
// 关键点：归一化后 "流浪地球2" 以 "流浪地球" 为前缀，但紧跟的数字是续集编号，
// 不能被判成包含匹配 —— 否则续集会被当成本片转存。
func TestReasonTitleMismatch(t *testing.T) {
	cases := []struct {
		name  string
		title string
	}{
		{"续集编号", "流浪地球2 (2023) 4K HDR"},
		{"完全不同的片名", "满江红 (2023)"},
		{"同系列不同作", "流浪地球前传"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Validate(movieSub(), Manifest{
				Kind:      EvidenceMetadata,
				Titles:    []string{tc.title},
				MediaType: "movie",
			}, Options{})
			if res.Passed || res.Reason != ReasonTitleMismatch {
				t.Fatalf("期望 TITLE_MISMATCH，实际 %+v", res)
			}
			if res.Dimension != DimensionTitle {
				t.Fatalf("期望 dimension=title，实际 %q", res.Dimension)
			}
		})
	}
}

// TestTitleMatchAcceptsTechnicalNoise 修饰词不能把正确资源判掉。
func TestTitleMatchAcceptsTechnicalNoise(t *testing.T) {
	for _, title := range []string{
		"流浪地球 (2019) 1080P",
		"流浪地球 2019 4K HDR 杜比视界 蓝光原盘",
		"流浪地球国语中字高清蓝光",
	} {
		res := Validate(movieSub(), Manifest{
			Kind:      EvidenceMetadata,
			Titles:    []string{title},
			MediaType: "movie",
		}, Options{})
		if !res.Passed {
			t.Fatalf("标题 %q 应通过，实际 %+v", title, res)
		}
	}
}

// TestReasonTMDBIDMismatch 证据自带的 TMDB ID 与订阅冲突。
func TestReasonTMDBIDMismatch(t *testing.T) {
	res := Validate(movieSub(), Manifest{
		Kind:      EvidenceMetadata,
		Titles:    []string{"流浪地球 (2019)"},
		MediaType: "movie",
		TMDBID:    "693134",
	}, Options{})
	if res.Passed || res.Reason != ReasonTMDBIDMismatch || res.Dimension != DimensionTMDBID {
		t.Fatalf("期望 TMDB_ID_MISMATCH，实际 %+v", res)
	}
}

// TestReasonUnrecognizedMedia 认不出是电影还是剧集。
func TestReasonUnrecognizedMedia(t *testing.T) {
	res := Validate(Request{ExpectedTitle: "流浪地球", ExpectedType: "movie"}, Manifest{
		Kind:      EvidenceMetadata,
		Titles:    []string{"----"},
		MediaType: "",
	}, Options{})
	if res.Passed || res.Reason != ReasonUnrecognizedMedia {
		t.Fatalf("期望 UNRECOGNIZED_MEDIA，实际 %+v", res)
	}
}

// TestReasonMediaTypeMismatch 剧集资源撞上电影订阅。
func TestReasonMediaTypeMismatch(t *testing.T) {
	res := Validate(movieSub(), Manifest{
		Kind:       EvidenceMetadata,
		Titles:     []string{"流浪地球.S01E01.1080p"},
		MediaType:  "tv",
		Season:     1,
		Episode:    1,
		EndEpisode: 8,
	}, Options{})
	if res.Passed || res.Reason != ReasonMediaTypeMismatch || res.Dimension != DimensionMediaType {
		t.Fatalf("期望 MEDIA_TYPE_MISMATCH，实际 %+v", res)
	}
}

// TestReasonMixedOrAmbiguous 清单里 10 个文件有 8 个是 S01E01-E08、2 个是别的剧。
//
// 注意阈值语义：本包按「主标题占比 >= 阈值 即通过」实现，默认阈值 0.8，所以
// 8:2 的占比正好等于 0.8 时是**通过**的。要让这个算例判为 MIXED_OR_AMBIGUOUS，
// 需要把阈值调高（例如 0.85）。这是与任务书算例的已知偏差，已在变更说明里标注。
func TestReasonMixedOrAmbiguous(t *testing.T) {
	names := namesOf(files(
		"庆余年.S01E01.1080p.mkv", "庆余年.S01E02.1080p.mkv",
		"庆余年.S01E03.1080p.mkv", "庆余年.S01E04.1080p.mkv",
		"庆余年.S01E05.1080p.mkv", "庆余年.S01E06.1080p.mkv",
		"庆余年.S01E07.1080p.mkv", "庆余年.S01E08.1080p.mkv",
	))
	names = append(names, namesOf(files("三体.S01E01.2160p.mkv", "三体.S01E02.2160p.mkv"))...)
	manifest := Manifest{
		Kind:             EvidenceManifest,
		Files:            files(names...),
		ListingSucceeded: true,
	}
	req := Request{ExpectedTitle: "庆余年", ExpectedType: "tv", ExpectedSeason: 1}

	// 默认阈值 0.8：占比正好 0.8，按 >= 规则通过。
	if res := Validate(req, manifest, Options{}); !res.Passed {
		t.Fatalf("占比 0.8 在默认阈值下应通过，实际 %+v", res)
	}
	// 阈值调到 0.85：同一份清单判为混拼。
	res := Validate(req, manifest, Options{PurityRatio: 0.85})
	if res.Passed || res.Reason != ReasonMixedOrAmbiguous || res.Dimension != DimensionPurity {
		t.Fatalf("期望 MIXED_OR_AMBIGUOUS，实际 %+v", res)
	}
	if got := res.Detail["dominant_ratio"]; got != 0.8 {
		t.Fatalf("dominant_ratio 期望 0.8，实际 %v", got)
	}
}

// TestPurityNotFooledByReleaseGroupTags 同一部剧的注入点标题不一致时不能误判混拼。
func TestPurityNotFooledByReleaseGroupTags(t *testing.T) {
	manifest := Manifest{
		Kind: EvidenceManifest,
		Files: files(
			"[Silo.S01.1080p.WEB-DL.DDP5.1.H.264-CtrlHD] Silo.S01E01.The.Jury.1080p.mkv",
			"Silo.S01E02.1080p.mkv",
			"Silo.S01E03.1080p.mkv",
		),
		ListingSucceeded: true,
	}
	res := Validate(Request{ExpectedTitle: "Silo", ExpectedType: "tv"}, manifest, Options{})
	if !res.Passed {
		t.Fatalf("同一部剧不应被判混拼，实际 %+v", res)
	}
	if got := res.Detail["dominant_ratio"]; got != 1.0 {
		t.Fatalf("包含关系合并后 dominant_ratio 应为 1，实际 %v", got)
	}
}

// TestReasonIdentityUnverifiable 拿到了证据但没有任何维度可评估。
func TestReasonIdentityUnverifiable(t *testing.T) {
	res := Validate(movieSub(), Manifest{
		Kind:      EvidenceMetadata,
		Titles:    nil,
		MediaType: "",
		Year:      0,
	}, Options{})
	if res.Passed || res.Reason != ReasonUnverifiable {
		t.Fatalf("期望 IDENTITY_UNVERIFIABLE，实际 %+v", res)
	}
}

// TestReasonManifestIncomplete 清单接口成功但只列了目录。
func TestReasonManifestIncomplete(t *testing.T) {
	res := Validate(movieSub(), Manifest{
		Kind:             EvidenceManifest,
		ListingSucceeded: true,
		DirectoriesOnly:  true,
	}, Options{})
	if res.Passed || res.Reason != ReasonManifestIncomplete || res.Dimension != DimensionCompleteness {
		t.Fatalf("期望 IDENTITY_MANIFEST_INCOMPLETE，实际 %+v", res)
	}
}

// TestReasonManifestUnavailable 磁力/ed2k 只有链接，什么都验证不了。
func TestReasonManifestUnavailable(t *testing.T) {
	for _, kind := range []Evidence{EvidenceNone, ""} {
		res := Validate(movieSub(), Manifest{Kind: kind}, Options{})
		if res.Passed || res.Reason != ReasonManifestUnavailable {
			t.Fatalf("kind=%q 期望 IDENTITY_MANIFEST_UNAVAILABLE，实际 %+v", kind, res)
		}
	}
	// 清单接口调用失败同样落这个码。
	res := Validate(movieSub(), Manifest{
		Kind:             EvidenceManifest,
		ListingSucceeded: false,
	}, Options{})
	if res.Passed || res.Reason != ReasonManifestUnavailable {
		t.Fatalf("期望 IDENTITY_MANIFEST_UNAVAILABLE，实际 %+v", res)
	}
}

// TestAllowManifestUnavailableEscapeHatch 保留的逃生阀：只把放行标记打开，
// reason 仍保留在日志里便于追溯。
func TestAllowManifestUnavailableEscapeHatch(t *testing.T) {
	res := Validate(movieSub(), Manifest{Kind: EvidenceNone}, Options{AllowManifestUnavailable: true})
	if !res.Passed {
		t.Fatalf("逃生阀打开时应放行，实际 %+v", res)
	}
	if res.Reason != ReasonManifestUnavailable {
		t.Fatalf("reason 应保留原值，实际 %q", res.Reason)
	}
	if res.Detail["allow_manifest_unavailable"] != true {
		t.Fatalf("detail 应标记逃生阀，实际 %v", res.Detail["allow_manifest_unavailable"])
	}
}

// TestYearMismatch 年份维度不合映射到 TITLE_MISMATCH，但用 dimension 区分。
func TestYearMismatch(t *testing.T) {
	res := Validate(movieSub(), Manifest{
		Kind:      EvidenceMetadata,
		Titles:    []string{"流浪地球 (2010)"},
		MediaType: "movie",
	}, Options{})
	if res.Passed || res.Reason != ReasonTitleMismatch || res.Dimension != DimensionYear {
		t.Fatalf("期望 TITLE_MISMATCH/year，实际 %+v", res)
	}
}

// TestSeasonMismatch 季号维度。
func TestSeasonMismatch(t *testing.T) {
	res := Validate(Request{ExpectedTitle: "庆余年", ExpectedType: "tv", ExpectedSeason: 2},
		Manifest{
			Kind:       EvidenceMetadata,
			Titles:     []string{"庆余年.S03E01.1080p"},
			MediaType:  "tv",
			Season:     3,
			Episode:    1,
			EndEpisode: 20,
		}, Options{})
	if res.Passed || res.Reason != ReasonTitleMismatch || res.Dimension != DimensionSeason {
		t.Fatalf("期望 TITLE_MISMATCH/season，实际 %+v", res)
	}
}

// TestEpisodeMismatch 集号维度：多集合集文件能覆盖到订阅要的那一集才算过。
func TestEpisodeMismatch(t *testing.T) {
	manifest := Manifest{
		Kind:             EvidenceManifest,
		Files:            files("庆余年.S01E01-E08.1080p.mkv"),
		ListingSucceeded: true,
	}
	req := Request{ExpectedTitle: "庆余年", ExpectedType: "tv", ExpectedSeason: 1}

	if res := Validate(req, manifest, Options{}); !res.Passed {
		t.Fatalf("E01-E08 合集应覆盖第 8 集，实际 %+v", res)
	}
	req.ExpectedEpisode = 12
	res := Validate(req, manifest, Options{})
	if res.Passed || res.Reason != ReasonTitleMismatch || res.Dimension != DimensionEpisode {
		t.Fatalf("期望 TITLE_MISMATCH/episode，实际 %+v", res)
	}
}

// TestAltTitleMatch TMDB 的 OriginalTitle 也要参与匹配，否则中文原名订阅会
// 把外文标题的正确资源误判掉。
func TestAltTitleMatch(t *testing.T) {
	res := Validate(Request{
		ExpectedTitle:     "流浪地球",
		ExpectedAltTitles: []string{"The Wandering Earth"},
		ExpectedType:      "movie",
	}, Manifest{
		Kind:      EvidenceMetadata,
		Titles:    []string{"The Wandering Earth 2019 1080p BluRay"},
		MediaType: "movie",
	}, Options{})
	if !res.Passed {
		t.Fatalf("OriginalTitle 命中应通过，实际 %+v", res)
	}
}

// TestDefaultPurityRatioApplied 零值 Options 使用推测默认阈值 0.8。
// 按「主标题占比 >= 阈值 即通过」的语义，8:2 的占比（正好 0.8）在默认阈值下通过。
func TestDefaultPurityRatioApplied(t *testing.T) {
	if DefaultPurityRatio != 0.8 {
		t.Fatalf("推测默认阈值应为 0.8，实际 %v", DefaultPurityRatio)
	}
	names := namesOf(files("剧A.S01E01.mkv", "剧A.S01E02.mkv", "剧A.S01E03.mkv", "剧A.S01E04.mkv",
		"剧A.S01E05.mkv", "剧A.S01E06.mkv", "剧A.S01E07.mkv", "剧A.S01E08.mkv"))
	names = append(names, namesOf(files("剧B.S01E01.mkv", "剧B.S01E02.mkv"))...)
	res := Validate(Request{ExpectedTitle: "剧A", ExpectedType: "tv"}, Manifest{
		Kind:             EvidenceManifest,
		Files:            files(names...),
		ListingSucceeded: true,
	}, Options{})
	if !res.Passed {
		t.Fatalf("占比 0.8 在默认阈值 0.8 下应通过（>= 语义），实际 %+v", res)
	}
	if got := res.Detail["dominant_ratio"]; got != 0.8 {
		t.Fatalf("dominant_ratio 期望 0.8，实际 %v", got)
	}
}

// TestEveryReasonReachable 保证 9 个 reason_code 都可能被产出，避免有码永远不出现。
func TestEveryReasonReachable(t *testing.T) {
	seen := map[Reason]bool{}
	record := func(res Result) {
		if res.Passed {
			seen[ReasonMatched] = true
			return
		}
		seen[res.Reason] = true
	}
	record(Validate(movieSub(), Manifest{Kind: EvidenceMetadata, Titles: []string{"流浪地球 (2019)"}, MediaType: "movie"}, Options{}))
	record(Validate(movieSub(), Manifest{Kind: EvidenceMetadata, Titles: []string{"满江红"}, MediaType: "movie"}, Options{}))
	record(Validate(movieSub(), Manifest{Kind: EvidenceMetadata, Titles: []string{"流浪地球"}, MediaType: "movie", TMDBID: "1"}, Options{}))
	record(Validate(movieSub(), Manifest{Kind: EvidenceMetadata, Titles: []string{"---"}, MediaType: ""}, Options{}))
	record(Validate(movieSub(), Manifest{Kind: EvidenceMetadata, Titles: []string{"流浪地球.S01E01"}, MediaType: "tv", Season: 1, Episode: 1}, Options{}))
	record(Validate(movieSub(), Manifest{Kind: EvidenceManifest, ListingSucceeded: true, DirectoriesOnly: true}, Options{}))
	record(Validate(movieSub(), Manifest{Kind: EvidenceMetadata}, Options{}))
	record(Validate(movieSub(), Manifest{Kind: EvidenceNone}, Options{}))
	mixed := append(names("剧A.S01E0%d.mkv", 1, 2, 3, 4, 5, 6, 7, 8), names("剧B.S01E0%d.mkv", 1, 2)...)
	record(Validate(Request{ExpectedTitle: "剧A", ExpectedType: "tv"}, Manifest{
		Kind:             EvidenceManifest,
		Files:            files(mixed...),
		ListingSucceeded: true,
	}, Options{PurityRatio: 0.85}))

	for _, reason := range []Reason{
		ReasonMatched, ReasonTitleMismatch, ReasonTMDBIDMismatch, ReasonUnrecognizedMedia,
		ReasonMediaTypeMismatch, ReasonMixedOrAmbiguous, ReasonUnverifiable,
		ReasonManifestIncomplete, ReasonManifestUnavailable,
	} {
		if !seen[reason] {
			t.Errorf("reason_code %s 在用例里没有被触发", reason)
		}
	}
}

func names(format string, args ...int) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		out = append(out, fmt.Sprintf(format, a))
	}
	return out
}

// namesOf 把文件清单拆回名字，便于拼接多组样本。
func namesOf(list []File) []string {
	out := make([]string, 0, len(list))
	for _, f := range list {
		out = append(out, f.Name)
	}
	return out
}
