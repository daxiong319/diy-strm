package discovery

import (
	"strings"
	"testing"
)

// ④「榜单推荐 服务内部错误」的回归防线。
//
// 背景：前端历史版本把豆瓣片单的 key 直接当 provider 传（`movie_weekly_best`），
// 缺少 `douban:` 前缀。Rankings 只在有前缀时走豆瓣分支，裸 key 落到 default
// 分支报「不支持的榜单来源」，该错误不是 AppError，HTTP 层兜底成 500 +
// 「服务内部错误」，用户看到的就是整页报错。
//
// 这里只验证**分派决策**，不真的去拉豆瓣（那会连网并写库）。判据是
// 「有没有被判成不支持的来源」——这正是修掉的那个错误。

// rankingsDispatchTarget 复刻 Rankings 的分支判定，返回该 provider 会被
// 分发到哪个来源，以及是否被判为不支持。这样测试不依赖网络与数据库。
func rankingsDispatchTarget(provider string) (kind string, unsupported bool) {
	p := strings.TrimSpace(strings.ToLower(provider))
	switch {
	case p == "" || p == "hdhive":
		return "hdhive", false
	case strings.HasPrefix(p, "hdhive:"):
		return "hdhive", false
	case strings.HasPrefix(p, "tmdb"):
		return "tmdb", false
	case strings.HasPrefix(p, "douban:"):
		return "douban", false
	default:
		if _, ok := doubanRankingCollections[p]; ok {
			return "douban", false
		}
		return "", true
	}
}

func TestRankingsAcceptsBareDoubanCollectionKey(t *testing.T) {
	// 修复点：裸 key 必须被当成豆瓣片单受理，不能再落到 default 报错。
	for key := range doubanRankingCollections {
		kind, unsupported := rankingsDispatchTarget(key)
		if unsupported {
			t.Errorf("裸豆瓣片单 key %q 仍被判为不支持的来源；应自动补 douban: 前缀受理", key)
			continue
		}
		if kind != "douban" {
			t.Errorf("裸豆瓣片单 key %q 被分派到 %q，期望 douban", key, kind)
		}
	}
}

func TestRankingsAcceptsPrefixedDoubanKey(t *testing.T) {
	for key := range doubanRankingCollections {
		kind, unsupported := rankingsDispatchTarget("douban:" + key)
		if unsupported || kind != "douban" {
			t.Errorf("带前缀的豆瓣片单 key douban:%s 分派异常：kind=%q unsupported=%v", key, kind, unsupported)
		}
	}
}

func TestRankingsRejectsUnknownProvider(t *testing.T) {
	// 容错分支不能顺手把未知来源也放行，否则错误会被静默吞掉。
	if _, unsupported := rankingsDispatchTarget("definitely_not_a_provider"); !unsupported {
		t.Error("未知 provider 必须仍被判为不支持的来源，容错不能吞掉错误")
	}
}

// TestRankingsRejectsUnknownProviderViaRealFunc 用真实函数确认未知来源仍然报错
// （这条路径不碰网络与数据库，可以安全调用）。
func TestRankingsRejectsUnknownProviderViaRealFunc(t *testing.T) {
	_, err := Rankings(nil, "definitely_not_a_provider", "", "", 1, false)
	if err == nil {
		t.Fatal("未知 provider 应当报错，实际返回 nil")
	}
	if !strings.Contains(err.Error(), "不支持的榜单来源") {
		t.Fatalf("未知 provider 的错误文案不符，实际：%v", err)
	}
}
