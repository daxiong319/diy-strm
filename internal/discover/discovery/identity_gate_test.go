package discovery

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"litepan/internal/discover/ddb"
	"litepan/internal/discover/identity"
	"litepan/internal/settings"
)

// 本文件锁死「未通过身份校验，一律不转存」这条不变式在订阅转存链路上的落点。
//
// 与 identity 包的单测分工：
//   - identity 包测的是判定规则本身（六维、9 个 reason_code）；
//   - 这里测的是接线：证据从候选怎么抽、设置怎么读、记录怎么落库、
//     以及 planAndTransferRuleCandidates 里闸门是不是真的挡在转存之前。

type identitySettingsRepo struct {
	values map[string]string
}

func (r *identitySettingsRepo) Get(_ context.Context, key string) (string, bool, error) {
	v, ok := r.values[key]
	return v, ok, nil
}

func (r *identitySettingsRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}

func (r *identitySettingsRepo) All(_ context.Context) (map[string]string, error) {
	out := make(map[string]string, len(r.values))
	for k, v := range r.values {
		out[k] = v
	}
	return out, nil
}

// setupIdentityGate 装好测试库 + 设置服务。ddb.Init 是 once 语义，同进程只生效一次。
func setupIdentityGate(t *testing.T, overrides map[string]string) {
	t.Helper()
	if ddb.Db == nil {
		if err := ddb.Init(filepath.Join(t.TempDir(), "identity_gate_test.db"), slog.Default()); err != nil {
			t.Fatalf("初始化测试数据库失败：%v", err)
		}
	}
	if err := ddb.Db.Exec("DROP TABLE IF EXISTS discovery_subscription_identity_checks").Error; err != nil {
		t.Fatalf("清理表失败：%v", err)
	}
	if err := ddb.Db.AutoMigrate(&DiscoverySubscriptionIdentityCheck{}); err != nil {
		t.Fatalf("建表失败：%v", err)
	}

	values := map[string]string{}
	for k, v := range overrides {
		values[k] = v
	}
	svc, err := settings.New(context.Background(), &identitySettingsRepo{values: values})
	if err != nil {
		t.Fatalf("构造设置服务失败：%v", err)
	}
	BindSettings(svc)
	t.Cleanup(func() { BindSettings(nil) })
}

func identityTestSub() *DiscoverySubscription {
	return &DiscoverySubscription{
		ID: 4242, TMDBID: 157350, MediaType: "movie",
		Title: "流浪地球", OriginalTitle: "The Wandering Earth",
		TargetProvider: "123",
	}
}

func countIdentityRecords(t *testing.T) int64 {
	t.Helper()
	var n int64
	if err := ddb.Db.Model(&DiscoverySubscriptionIdentityCheck{}).Count(&n).Error; err != nil {
		t.Fatalf("统计记录失败：%v", err)
	}
	return n
}

// TestIdentityGateBlocksMagnetAndEd2k 验收项 1：磁力/ed2k 只有链接，
// 拿不到任何可验证证据 → IDENTITY_MANIFEST_UNAVAILABLE → 不转存。
func TestIdentityGateBlocksMagnetAndEd2k(t *testing.T) {
	setupIdentityGate(t, nil)
	sub := identityTestSub()
	rule := DiscoverySubscriptionRule{ID: 7, TargetProvider: "123", MaxPoints: 4}

	for _, linkType := range []string{"magnet", "ed2k"} {
		cand := resourceCandidate{
			Source: "re0", Provider: linkType, LinkType: linkType,
			Title: "流浪地球 (2019) 1080P", Slug: "magnet:?xt=urn:btih:abc",
			MediaType: "movie",
		}
		res := checkCandidateIdentity(sub, rule, cand, 88)
		if res.Passed {
			t.Fatalf("%s 候选应被身份校验挡下，实际通过", linkType)
		}
		if res.Reason != identity.ReasonManifestUnavailable {
			t.Fatalf("%s 期望 IDENTITY_MANIFEST_UNAVAILABLE，实际 %s", linkType, res.Reason)
		}
		if reason := identitySkipReason(res); reason == "" {
			t.Fatalf("%s 跳过原因不应为空", linkType)
		}
	}
	if got := countIdentityRecords(t); got != 2 {
		t.Fatalf("判定记录应落库 2 条，实际 %d", got)
	}
}

// TestIdentityGateAllowsMatchingCloudCandidate 验收项 5/7：网盘候选标题对得上就放行，
// 存量订阅的正常行为不能被误伤。
func TestIdentityGateAllowsMatchingCloudCandidate(t *testing.T) {
	setupIdentityGate(t, nil)
	res := checkCandidateIdentity(identityTestSub(), DiscoverySubscriptionRule{ID: 1}, resourceCandidate{
		Source: "re0", Provider: "115", LinkType: "share",
		Title: "流浪地球 (2019) 1080P 蓝光原盘", Slug: "abc123",
		MediaType: "movie", IsUnlocked: true, PointsKnown: true,
	}, 0)
	if !res.Passed || res.Reason != identity.ReasonMatched {
		t.Fatalf("期望 IDENTITY_MATCHED，实际 %+v", res)
	}
	if identitySkipReason(res) != "" {
		t.Fatalf("通过时不应有跳过原因")
	}
}

// TestIdentityGateBlocksSequel 验收项 2：搜到「流浪地球2」但订阅是「流浪地球」。
func TestIdentityGateBlocksSequel(t *testing.T) {
	setupIdentityGate(t, nil)
	res := checkCandidateIdentity(identityTestSub(), DiscoverySubscriptionRule{ID: 1}, resourceCandidate{
		Source: "re0", Provider: "123", LinkType: "share",
		Title: "流浪地球2 (2023) 4K HDR", Slug: "seq1", MediaType: "movie",
	}, 0)
	if res.Passed || res.Reason != identity.ReasonTitleMismatch {
		t.Fatalf("期望 TITLE_MISMATCH，实际 %+v", res)
	}
}

// TestIdentityGateBlocksMediaTypeMismatch 验收项 3：剧集资源撞上电影订阅。
func TestIdentityGateBlocksMediaTypeMismatch(t *testing.T) {
	setupIdentityGate(t, nil)
	res := checkCandidateIdentity(identityTestSub(), DiscoverySubscriptionRule{ID: 1}, resourceCandidate{
		Source: "re0", Provider: "123", LinkType: "share",
		Title: "流浪地球.S01E01.1080p", Slug: "tv1", MediaType: "tv",
		Episode: &candidateEpisode{SeasonNum: intPtr(1), EpisodeNum: intPtr(1), EndEpisodeNum: intPtr(8)},
	}, 0)
	if res.Passed || res.Reason != identity.ReasonMediaTypeMismatch {
		t.Fatalf("期望 MEDIA_TYPE_MISMATCH，实际 %+v", res)
	}
}

// TestIdentityGateDisabled 总开关关掉时完全放行（回滚路径）。
func TestIdentityGateDisabled(t *testing.T) {
	setupIdentityGate(t, map[string]string{settings.KeyMOSubscriptionIdentityEnabled: "false"})
	res := checkCandidateIdentity(identityTestSub(), DiscoverySubscriptionRule{ID: 1}, resourceCandidate{
		Source: "re0", Provider: "123", LinkType: "magnet", Title: "流浪地球", Slug: "m1",
	}, 0)
	if !res.Passed {
		t.Fatalf("总开关关闭时不应拦截，实际 %+v", res)
	}
	if got := countIdentityRecords(t); got != 0 {
		t.Fatalf("总开关关闭时不该落判定记录，实际 %d 条", got)
	}
}

// TestIdentityGateEscapeHatch 逃生阀：证据缺失也放行，但记录里仍留原 reason。
func TestIdentityGateEscapeHatch(t *testing.T) {
	setupIdentityGate(t, map[string]string{settings.KeyMOSubscriptionIdentityAllowUnavailable: "true"})
	res := checkCandidateIdentity(identityTestSub(), DiscoverySubscriptionRule{ID: 1}, resourceCandidate{
		Source: "re0", Provider: "123", LinkType: "magnet", Title: "流浪地球", Slug: "m1",
	}, 0)
	if !res.Passed {
		t.Fatalf("逃生阀打开时应放行，实际 %+v", res)
	}
	if res.Reason != identity.ReasonManifestUnavailable {
		t.Fatalf("reason 应保留原值，实际 %s", res.Reason)
	}
}

// TestIdentityGatePurityRatioFromSettings 纯度阈值走设置项，非法值回落到推测默认 0.8。
func TestIdentityGatePurityRatioFromSettings(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want float64
	}{
		{"0.9", 0.9},
		{"", identity.DefaultPurityRatio},
		{"abc", identity.DefaultPurityRatio},
		{"1.5", identity.DefaultPurityRatio},
		{"-1", identity.DefaultPurityRatio},
	} {
		setupIdentityGate(t, map[string]string{settings.KeyMOSubscriptionIdentityPurityRatio: tc.raw})
		_, opts := identityGateOptions()
		if opts.PurityRatio != tc.want {
			t.Fatalf("设置 %q 时期望阈值 %v，实际 %v", tc.raw, tc.want, opts.PurityRatio)
		}
	}
}

// TestIdentityGateWithoutSettingsService 没装配设置服务时保持保守（校验开着）。
func TestIdentityGateWithoutSettingsService(t *testing.T) {
	BindSettings(nil)
	enabled, opts := identityGateOptions()
	if !enabled {
		t.Fatalf("未装配设置服务时应默认开启校验")
	}
	if opts.PurityRatio != identity.DefaultPurityRatio {
		t.Fatalf("未装配设置服务时应使用推测默认阈值，实际 %v", opts.PurityRatio)
	}
	// 没库时也不能 panic。
	res := checkCandidateIdentity(identityTestSub(), DiscoverySubscriptionRule{ID: 1}, resourceCandidate{
		Source: "re0", Provider: "123", LinkType: "magnet", Title: "流浪地球", Slug: "m1",
	}, 0)
	if res.Passed {
		t.Fatalf("磁力候选仍应被挡下")
	}
}

// TestManifestForCandidateEvidenceTier 证据分档：磁力/ed2k 落 none 档，网盘落 metadata 档。
func TestManifestForCandidateEvidenceTier(t *testing.T) {
	season, ep, end := 1, 1, 8
	cases := []struct {
		name string
		cand resourceCandidate
		want identity.Evidence
	}{
		{"magnet", resourceCandidate{LinkType: "magnet", Title: "流浪地球"}, identity.EvidenceNone},
		{"ed2k", resourceCandidate{LinkType: "ed2k", Title: "流浪地球"}, identity.EvidenceNone},
		{"无标题无备注", resourceCandidate{LinkType: "share"}, identity.EvidenceNone},
		{"网盘", resourceCandidate{LinkType: "share", Title: "流浪地球", MediaType: "movie"}, identity.EvidenceMetadata},
		{"带季集证据", resourceCandidate{
			LinkType: "share", Title: "庆余年.S01E01", MediaType: "tv",
			Episode: &candidateEpisode{SeasonNum: &season, EpisodeNum: &ep, EndEpisodeNum: &end},
		}, identity.EvidenceMetadata},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := manifestForCandidate(tc.cand).Kind; got != tc.want {
				t.Fatalf("期望 %s，实际 %s", tc.want, got)
			}
		})
	}

	// 季集证据要如实带进 Manifest，否则 metadata 档的季集维度形同虚设。
	m := manifestForCandidate(resourceCandidate{
		LinkType: "share", Title: "庆余年.S01E01", MediaType: "tv",
		Episode: &candidateEpisode{SeasonNum: &season, EpisodeNum: &ep, EndEpisodeNum: &end},
	})
	if m.Season != 1 || m.Episode != 1 || m.EndEpisode != 8 {
		t.Fatalf("季集证据未透传：%+v", m)
	}
}

// TestIdentityGateRecordPersistsContext 落库记录要带够反查上下文。
func TestIdentityGateRecordPersistsContext(t *testing.T) {
	setupIdentityGate(t, nil)
	sub := identityTestSub()
	res := checkCandidateIdentity(sub, DiscoverySubscriptionRule{ID: 7}, resourceCandidate{
		Source: "re0", Provider: "123", LinkType: "share",
		Title: "流浪地球2 (2023)", Slug: "slug-1", ItemKey: "r7:slug-1", MediaType: "movie",
	}, 99)

	var rec DiscoverySubscriptionIdentityCheck
	if err := ddb.Db.Where("run_id = ?", 99).First(&rec).Error; err != nil {
		t.Fatalf("读取判定记录失败：%v", err)
	}
	if rec.Passed || rec.ReasonCode != string(identity.ReasonTitleMismatch) {
		t.Fatalf("记录内容不对：%+v", rec)
	}
	if rec.SubscriptionID != sub.ID || rec.TMDBID != sub.TMDBID || rec.RuleID != 7 {
		t.Fatalf("订阅/规则上下文丢失：%+v", rec)
	}
	if rec.Slug != "slug-1" || rec.ExpectedTitle != "流浪地球" || rec.CandidateTitle != "流浪地球2 (2023)" {
		t.Fatalf("候选上下文丢失：%+v", rec)
	}
	if rec.Detail == "" {
		t.Fatalf("Detail 应落库，便于事后排查")
	}
	if res.Detail["message"] == nil {
		t.Fatalf("跳过原因需要 message 明细")
	}
}

// TestIdentityGateAlternateTitleMatch TMDB 原名命中的正确资源不能被误伤。
func TestIdentityGateAlternateTitleMatch(t *testing.T) {
	setupIdentityGate(t, nil)
	res := checkCandidateIdentity(identityTestSub(), DiscoverySubscriptionRule{ID: 1}, resourceCandidate{
		Source: "re0", Provider: "115", LinkType: "share",
		Title: "The Wandering Earth 2019 1080p BluRay", Slug: "en1", MediaType: "movie",
	}, 0)
	if !res.Passed {
		t.Fatalf("原名命中应放行，实际 %+v", res)
	}
}

func intPtr(v int) *int { return &v }
