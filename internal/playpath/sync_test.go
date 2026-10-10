package playpath

import (
	"testing"
	"time"
)

// 开关关掉必须是彻底 no-op：规则还在、统计还在，但一个字节都不改写。
// 断言两条路径：Map 的结果，以及 Enabled 的回显。
func TestSyncDisabledIsCompleteNoOp(t *testing.T) {
	now := time.Now()
	s := New(nil, func() time.Time { return now })
	s.Map("/media/a.mkv") // 先在打开状态下攒一次命中
	s.Sync(Config{Enabled: true, Rules: `{"rules":[{"id":"r1","source":"/media","target":"/A"}]}`})
	if got := s.Map("/media/a.mkv"); !got.Matched || got.Path != "/A/a.mkv" {
		t.Fatalf("开启状态下应映射，matched=%v path=%q", got.Matched, got.Path)
	}
	hitsBefore := s.Stat("r1").Hits

	s.Sync(Config{Enabled: false, Rules: `{"rules":[{"id":"r1","source":"/media","target":"/A"}]}`})
	if s.Enabled() {
		t.Fatalf("Enabled 回显应为 false")
	}
	if got := s.Map("/media/a.mkv"); got.Matched || got.Path != "/media/a.mkv" {
		t.Fatalf("开关关掉时不得改写路径：matched=%v path=%q", got.Matched, got.Path)
	}
	if got := s.Stat("r1").Hits; got != hitsBefore {
		t.Fatalf("关开关不该清掉命中统计：%d != %d", got, hitsBefore)
	}
	// 再打开，统计必须还在 —— 用户临时关一下不该把诊断依据抹掉。
	s.Sync(Config{Enabled: true, Rules: `{"rules":[{"id":"r1","source":"/media","target":"/A"}]}`})
	if got := s.Stat("r1").Hits; got != hitsBefore {
		t.Fatalf("重开后命中统计应保留：%d != %d", got, hitsBefore)
	}
}

// 同值 Sync 必须是无操作。这是本文件最容易写错的一条：
// 若无操作判断失效，每次刷新配置都会把统计清零，
// 配置页会永远显示「从未命中」，而这正是本功能唯一的诊断依据。
func TestSyncWithIdenticalConfigKeepsHitStats(t *testing.T) {
	s := New(nil, nil)
	cfg := Config{Enabled: true, Rules: `{"rules":[{"id":"r1","source":"/media","target":"/A"}]}`}
	s.Sync(cfg)
	s.Map("/media/a.mkv")
	s.Map("/media/b.mkv")
	if got := s.Stat("r1").Hits; got != 2 {
		t.Fatalf("前置条件不对：hits=%d", got)
	}
	s.Sync(cfg)
	s.Sync(cfg)
	if got := s.Stat("r1").Hits; got != 2 {
		t.Fatalf("同值 Sync 抹掉了命中统计：%d != 2", got)
	}
}

// 规则改了才重建，并清掉已删规则的统计。
func TestSyncRebuildsRulesAndPrunesStats(t *testing.T) {
	s := New(nil, nil)
	s.Sync(Config{Enabled: true, Rules: `{"rules":[{"id":"r1","source":"/media","target":"/A"},{"id":"r2","source":"/other","target":"/B"}]}`})
	s.Map("/media/a.mkv")
	s.Map("/other/b.mkv")
	s.Sync(Config{Enabled: true, Rules: `{"rules":[{"id":"r1","source":"/media","target":"/A2"}]}`})
	if got := s.Map("/media/a.mkv"); got.Path != "/A2/a.mkv" {
		t.Fatalf("新目标未生效：%q", got.Path)
	}
	if got := s.Stat("r1").Hits; got != 2 {
		t.Fatalf("保留规则的统计应保留（改目标后再命中一次）：%d", got)
	}
	if got := s.Stat("r2").Hits; got != 0 {
		t.Fatalf("被删规则的统计应清零：%d", got)
	}
}

// 坏 JSON 不该让播放链路 500，也不该把已有规则清空成「看起来一条都没有」——
// 后者同样是无声的失败。坏原文视为解析出空规则（= 不改写路径），
// 但不能 panic、不能静默改用旧规则（那会让用户以为改动已生效）。
func TestSyncToleratesGarbageRules(t *testing.T) {
	s := New(nil, nil)
	s.Sync(Config{Enabled: true, Rules: "{not json"})
	got := s.Map("/media/a.mkv")
	if got.Matched || got.Path != "/media/a.mkv" {
		t.Fatalf("坏原文应退化为不改写：%+v", got)
	}
}

// 测试路径按钮必须走真实匹配：命中/不命中、遮蔽规则都要如实报出来。
// 「写反时测试路径立刻看出结果不对」这条验收就靠它。
func TestTestPathReportsSameResultAsRealMapping(t *testing.T) {
	s := New(nil, nil)
	s.Sync(Config{Enabled: true, Rules: `{"rules":[{"id":"broad","source":"/media","target":"/A"},{"id":"narrow","source":"/media/4k","target":"/B"}]}`})
	res := s.TestPath("/media/4k/movie.mkv")
	if !res.Matched || res.RuleID != "broad" || res.Path != "/A/4k/movie.mkv" {
		t.Fatalf("测试路径的结果不对：%+v", res)
	}
	if len(res.Candidates) != 1 || res.Candidates[0].ID != "narrow" {
		t.Fatalf("被遮蔽规则未报出，用户看不出为什么改了没生效：%+v", res.Candidates)
	}
	// 源写反时：用户想映射的那条根本命不中，测试路径会立刻显示「不会改写」。
	if got := s.TestPath("/mnt/media/movie.mkv"); got.Matched {
		t.Fatalf("写反的规则不该匹配 %q：%+v", "/mnt/media/movie.mkv", got)
	}
}
