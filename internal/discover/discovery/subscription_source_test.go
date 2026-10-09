package discovery

import (
	"testing"
)

// 本文件锁死「source 字段真的会被写进去」。
//
// 这条字段以前是死的：SaveSubscription 新建时硬编码 Source: "tmdb"，更新分支根本不碰它。
// 求片中心（T09）要靠 source == "request" 判断「这条订阅是家人求来的」——
// 字段不落库的话，管理台上看到的每条求片订阅都跟手动加的没区别，
// 出问题（重复求片建了两条、转存失败没人跟进）时连排查的抓手都没有。
//
// 所以这里测三件事：新建时按 payload 写、更新时按 payload 写、更新时为空则保持原样。

func saveSourceSubscription(t *testing.T, tmdbID int64, source string) *DiscoverySubscription {
	t.Helper()
	saved, _, err := SaveSubscription(&SubscriptionUpsertPayload{
		Source: source, EntityType: "tv", TMDBID: tmdbID,
		MediaType: "tv", Title: "来源测试剧", TargetProvider: "123", TransferMode: "auto",
	})
	if err != nil {
		t.Fatalf("保存订阅失败：%v", err)
	}
	return saved
}

func TestSaveSubscriptionKeepsSourceOnCreate(t *testing.T) {
	setupRuleTestDB(t)

	fromRequest := saveSourceSubscription(t, 777001, "request")
	if fromRequest.Source != "request" {
		t.Fatalf("求片建出来的订阅 source = %q，期望 request（求片中心靠这个字段识别来源）", fromRequest.Source)
	}

	// 空 source 回落 tmdb，不能存成空串 —— 空串会让「按来源筛选」把它漏掉。
	fallback := saveSourceSubscription(t, 777002, "")
	if fallback.Source != "tmdb" {
		t.Fatalf("空 source 回落成 %q，期望 tmdb", fallback.Source)
	}
}

func TestSaveSubscriptionSourceOnUpdate(t *testing.T) {
	setupRuleTestDB(t)
	saved := saveSourceSubscription(t, 777003, "request")

	// 管理台编辑这条订阅：表单会带 source 回来（subscriptionToPayload 会回填），
	// 值不变是常态，这里确认改标题不会把来源改掉。
	_, _, err := SaveSubscription(&SubscriptionUpsertPayload{
		Source: "request", EntityType: "tv", TMDBID: 777003,
		MediaType: "tv", Title: "来源测试剧（改名）", TargetProvider: "123", TransferMode: "auto",
	})
	if err != nil {
		t.Fatalf("更新订阅失败：%v", err)
	}
	got, err := GetSubscription(saved.ID)
	if err != nil {
		t.Fatalf("读回订阅失败：%v", err)
	}
	if got.Source != "request" {
		t.Fatalf("改名后 source = %q，期望仍是 request", got.Source)
	}

	// 反过来：调用方明确要改来源时必须改得动（emby 补建等路径依赖这件事）。
	if _, _, err := SaveSubscription(&SubscriptionUpsertPayload{
		Source: "tmdb", EntityType: "tv", TMDBID: 777003,
		MediaType: "tv", Title: "来源测试剧（改名）", TargetProvider: "123", TransferMode: "auto",
	}); err != nil {
		t.Fatalf("改来源失败：%v", err)
	}
	got, err = GetSubscription(saved.ID)
	if err != nil {
		t.Fatalf("读回订阅失败：%v", err)
	}
	if got.Source != "tmdb" {
		t.Fatalf("显式改来源后 source = %q，期望 tmdb", got.Source)
	}
}

func TestSaveSubscriptionEmptySourceOnUpdateKeepsPrevious(t *testing.T) {
	setupRuleTestDB(t)
	saved := saveSourceSubscription(t, 777004, "request")

	// 有些内部路径只改字段不带 source。这条路径最危险：
	// 如果「空即不改」没做，每次调用都会把来源静默抹成 tmdb，
	// 而求片记录里的 subscription_id 还指着它 —— 两边对不上，排查时根本看不出发生过什么。
	if _, _, err := SaveSubscription(&SubscriptionUpsertPayload{
		EntityType: "tv", TMDBID: 777004,
		MediaType: "tv", Title: "来源测试剧（静默更新）", TargetProvider: "123", TransferMode: "auto",
	}); err != nil {
		t.Fatalf("更新订阅失败：%v", err)
	}
	got, err := GetSubscription(saved.ID)
	if err != nil {
		t.Fatalf("读回订阅失败：%v", err)
	}
	if got.Source != "request" {
		t.Fatalf("空 source 更新后 source = %q，期望保持 request（空即不改）", got.Source)
	}
}
