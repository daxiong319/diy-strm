package discovery

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"litepan/internal/discover/ddb"
)

// setupTestDB 初始化发现板块库表并清空本文件涉及的几张表。
// ddb.Init 是 once 语义（同进程只生效第一次），因此不能用 t.TempDir() 隔离每个用例，
// 只能建一次表、每个用例前显式清表，避免用例之间互相污染。
func setupTestDB(t *testing.T) {
	t.Helper()
	if ddb.Db == nil {
		if err := ddb.Init(filepath.Join(t.TempDir(), "test.db"), slog.Default()); err != nil {
			t.Fatalf("初始化测试数据库失败：%v", err)
		}
	}
	for _, table := range []string{"discovery_channels", "discovery_transfer_records", "discovery_monitor_records"} {
		if err := ddb.Db.Exec("DROP TABLE IF EXISTS " + table).Error; err != nil {
			t.Fatalf("清理表 %s 失败：%v", table, err)
		}
	}
	if err := ddb.Db.AutoMigrate(&DiscoveryChannel{}, &DiscoveryTransferRecord{}, &DiscoveryMonitorRecord{}); err != nil {
		t.Fatalf("建表失败：%v", err)
	}
}

func TestChannelName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://t.me/s/xxx", "xxx"},
		{"@xxx", "xxx"},
		{"t.me/xxx/", "xxx"},
		{"https://t.me/xxx", "xxx"},
		{"t.me/s/xxx", "xxx"},
		{"xxx", "xxx"},
		{"xxx/", "xxx"},
	}
	for _, tc := range cases {
		c := &DiscoveryChannel{Channel: tc.in}
		if got := c.ChannelName(); got != tc.want {
			t.Errorf("ChannelName(%q) = %q, 期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestHasLinkRecord(t *testing.T) {
	setupTestDB(t)
	link := "https://pan.quark.cn/s/abc123"
	if HasLinkRecord(link) {
		t.Fatal("空表时 HasLinkRecord 应为 false")
	}
	if err := CreateTransferRecord(&DiscoveryTransferRecord{LinkURL: link, Title: "剧集"}); err != nil {
		t.Fatalf("写入转存记录失败：%v", err)
	}
	if !HasLinkRecord(link) {
		t.Error("已写入的链接 HasLinkRecord 应为 true")
	}
	if HasLinkRecord("https://pan.quark.cn/s/other") {
		t.Error("未写入的链接 HasLinkRecord 应为 false")
	}
}

// seedTR 写入一条转存记录，便于构造多记录场景
func seedTR(t *testing.T, r DiscoveryTransferRecord) *DiscoveryTransferRecord {
	t.Helper()
	if err := CreateTransferRecord(&r); err != nil {
		t.Fatalf("写入转存记录失败：%v", err)
	}
	return &r
}

func TestHasEpisodeRecord(t *testing.T) {
	const subID = uint(7)
	const tmdbID = int64(999)

	t.Run("epKeys 为空退化为整片判断", func(t *testing.T) {
		setupTestDB(t)
		seedTR(t, DiscoveryTransferRecord{SubscriptionID: subID, TMDBID: tmdbID, Season: 1, Episode: "S01E01"})
		if !HasEpisodeRecord(subID, tmdbID, 1, nil) {
			t.Error("有转存记录时 epKeys 为空应返回 true（退化为整片判断）")
		}
		if HasEpisodeRecord(subID, tmdbID, 2, nil) {
			t.Error("另一季无记录，epKeys 为空应返回 false")
		}
	})

	t.Run("跨记录并集命中", func(t *testing.T) {
		setupTestDB(t)
		// 同一季的集号分散在两条记录里（一帖多集），必须求并集才能判定
		seedTR(t, DiscoveryTransferRecord{SubscriptionID: subID, TMDBID: tmdbID, Season: 1, Episode: "S01E01,S01E02"})
		seedTR(t, DiscoveryTransferRecord{SubscriptionID: subID, TMDBID: tmdbID, Season: 1, Episode: " S01E03 "})
		if !HasEpisodeRecord(subID, tmdbID, 1, []string{"S01E01", "S01E03"}) {
			t.Error("并集覆盖全部 epKeys 时应返回 true")
		}
	})

	t.Run("跨季交叉命中", func(t *testing.T) {
		setupTestDB(t)
		seedTR(t, DiscoveryTransferRecord{SubscriptionID: subID, TMDBID: tmdbID, Season: 1, Episode: "S01E10"})
		seedTR(t, DiscoveryTransferRecord{SubscriptionID: subID, TMDBID: tmdbID, Season: 2, Episode: "S02E10"})
		if !HasEpisodeRecord(subID, tmdbID, 1, []string{"S01E10"}) {
			t.Error("季 1 的集号应命中季 1 记录")
		}
		if !HasEpisodeRecord(subID, tmdbID, 2, []string{"S02E10"}) {
			t.Error("季 2 的集号应命中季 2 记录")
		}
		if HasEpisodeRecord(subID, tmdbID, 1, []string{"S02E10"}) {
			t.Error("季 1 查询不应命中季 2 的集号")
		}
	})

	t.Run("部分命中返回 false", func(t *testing.T) {
		setupTestDB(t)
		seedTR(t, DiscoveryTransferRecord{SubscriptionID: subID, TMDBID: tmdbID, Season: 1, Episode: "S01E01"})
		if HasEpisodeRecord(subID, tmdbID, 1, []string{"S01E01", "S01E02"}) {
			t.Error("存在未收录的集号时应返回 false")
		}
	})

	t.Run("superseded 记录被忽略", func(t *testing.T) {
		setupTestDB(t)
		seedTR(t, DiscoveryTransferRecord{SubscriptionID: subID, TMDBID: tmdbID, Season: 1, Episode: "S01E01"})
		seedTR(t, DiscoveryTransferRecord{SubscriptionID: subID, TMDBID: tmdbID, Season: 1, Episode: "S01E02", Status: "superseded"})
		if HasEpisodeRecord(subID, tmdbID, 1, []string{"S01E01", "S01E02"}) {
			t.Error("superseded 记录不应计入已转存集合")
		}
		// 整片判断同样要排除 superseded
		setupTestDB(t)
		seedTR(t, DiscoveryTransferRecord{SubscriptionID: subID, TMDBID: tmdbID, Season: 1, Episode: "S01E01", Status: "superseded"})
		if HasSubscriptionRecord(subID, tmdbID, 1) {
			t.Error("仅有 superseded 记录时 HasSubscriptionRecord 应为 false")
		}
		if HasEpisodeRecord(subID, tmdbID, 1, nil) {
			t.Error("仅有 superseded 记录时 epKeys 为空应返回 false")
		}
	})
}

func TestSupersedeTransferRecords(t *testing.T) {
	setupTestDB(t)
	const subID = uint(3)
	const tmdbID = int64(555)

	old1 := seedTR(t, DiscoveryTransferRecord{SubscriptionID: subID, TMDBID: tmdbID, Season: 1, Episode: "S01E01", Resolution: 1})
	old2 := seedTR(t, DiscoveryTransferRecord{SubscriptionID: subID, TMDBID: tmdbID, Season: 1, Episode: "S01E01", Resolution: 2})
	new1 := seedTR(t, DiscoveryTransferRecord{SubscriptionID: subID, TMDBID: tmdbID, Season: 1, Episode: "S01E01", Resolution: 3})
	otherSeason := seedTR(t, DiscoveryTransferRecord{SubscriptionID: subID, TMDBID: tmdbID, Season: 2, Episode: "S02E01", Resolution: 1})

	n, err := SupersedeTransferRecords(subID, tmdbID, 1, []uint{new1.ID})
	if err != nil {
		t.Fatalf("洗版标记失败：%v", err)
	}
	if n != 2 {
		t.Errorf("受影响行数 = %d, 期望 2", n)
	}

	var kept DiscoveryTransferRecord
	if err := ddb.Db.First(&kept, new1.ID).Error; err != nil {
		t.Fatalf("查询保留记录失败：%v", err)
	}
	if kept.Status != "" {
		t.Errorf("keepIDs 中的记录 status = %q, 期望保持空", kept.Status)
	}
	for _, id := range []uint{old1.ID, old2.ID} {
		var r DiscoveryTransferRecord
		if err := ddb.Db.First(&r, id).Error; err != nil {
			t.Fatalf("查询旧记录 %d 失败：%v", id, err)
		}
		if r.Status != "superseded" {
			t.Errorf("旧记录 %d status = %q, 期望 superseded", id, r.Status)
		}
	}
	var s2 DiscoveryTransferRecord
	if err := ddb.Db.First(&s2, otherSeason.ID).Error; err != nil {
		t.Fatalf("查询另一季记录失败：%v", err)
	}
	if s2.Status != "" {
		t.Errorf("另一季记录不应被标记，status = %q", s2.Status)
	}

	// 已被替换的记录不应重复计入
	n2, err := SupersedeTransferRecords(subID, tmdbID, 1, []uint{new1.ID})
	if err != nil {
		t.Fatalf("二次洗版标记失败：%v", err)
	}
	if n2 != 0 {
		t.Errorf("二次调用受影响行数 = %d, 期望 0", n2)
	}
}

func TestListMonitorRecords(t *testing.T) {
	setupTestDB(t)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local)
	seed := func(source, status, title, channel, result string, offset time.Duration) {
		t.Helper()
		r := &DiscoveryMonitorRecord{
			SourceType:     source,
			TransferStatus: status,
			Title:          title,
			Channel:        channel,
			TransferResult: result,
			TransferTime:   base.Add(offset),
		}
		if err := CreateMonitorTransferRecord(r); err != nil {
			t.Fatalf("写入监控历史失败：%v", err)
		}
	}
	seed("123", MonitorStatusSuccess, "葬送的芙莉莲", "chan_a", "转存 12 个文件", 0)
	seed("123", MonitorStatusFailed, "进击的巨人", "chan_b", "分享链接已失效", time.Minute)
	seed("pan139", MonitorStatusSkipped, "咒术回战", "chan_a", "已存在，跳过", 2*time.Minute)
	seed("123", MonitorStatusSuccess, "胆大党", "chan_c", "转存 3 个文件", 3*time.Minute)

	t.Run("按状态过滤", func(t *testing.T) {
		rs, total, err := ListMonitorRecords("", MonitorStatusSuccess, "", 1, 20)
		if err != nil {
			t.Fatalf("查询失败：%v", err)
		}
		if total != 2 || len(rs) != 2 {
			t.Fatalf("total = %d, len = %d, 期望 2/2", total, len(rs))
		}
		// 按 transfer_time desc
		if rs[0].Title != "胆大党" {
			t.Errorf("首条 = %q, 期望最近写入的「胆大党」", rs[0].Title)
		}
	})

	t.Run("按来源与状态组合过滤", func(t *testing.T) {
		rs, total, err := ListMonitorRecords("123", MonitorStatusSuccess, "", 1, 20)
		if err != nil {
			t.Fatalf("查询失败：%v", err)
		}
		if total != 2 || len(rs) != 2 {
			t.Errorf("total = %d, len = %d, 期望 2/2", total, len(rs))
		}
	})

	t.Run("keyword 匹配标题/频道/结果", func(t *testing.T) {
		if _, total, _ := ListMonitorRecords("", "", "芙莉莲", 1, 20); total != 1 {
			t.Errorf("标题关键词 total = %d, 期望 1", total)
		}
		if _, total, _ := ListMonitorRecords("", "", "chan_a", 1, 20); total != 2 {
			t.Errorf("频道关键词 total = %d, 期望 2", total)
		}
		if _, total, _ := ListMonitorRecords("", "", "失效", 1, 20); total != 1 {
			t.Errorf("结果关键词 total = %d, 期望 1", total)
		}
		if _, total, _ := ListMonitorRecords("", "", "不存在的关键词", 1, 20); total != 0 {
			t.Errorf("无命中关键词 total = %d, 期望 0", total)
		}
	})

	t.Run("分页", func(t *testing.T) {
		rs, total, err := ListMonitorRecords("", "", "", 1, 2)
		if err != nil {
			t.Fatalf("查询失败：%v", err)
		}
		if total != 4 {
			t.Errorf("total = %d, 期望 4（总数不受分页影响）", total)
		}
		if len(rs) != 2 {
			t.Fatalf("第一页 len = %d, 期望 2", len(rs))
		}
		if rs[0].Title != "胆大党" || rs[1].Title != "咒术回战" {
			t.Errorf("第一页 = [%q %q], 期望 [胆大党 咒术回战]", rs[0].Title, rs[1].Title)
		}
		rs2, _, err := ListMonitorRecords("", "", "", 2, 2)
		if err != nil {
			t.Fatalf("查询失败：%v", err)
		}
		if len(rs2) != 2 || rs2[0].Title != "进击的巨人" || rs2[1].Title != "葬送的芙莉莲" {
			t.Errorf("第二页顺序不符合 transfer_time desc")
		}
		// pageSize 超上限回落到默认 20
		rsBig, _, err := ListMonitorRecords("", "", "", 1, 9999)
		if err != nil {
			t.Fatalf("查询失败：%v", err)
		}
		if len(rsBig) != 4 {
			t.Errorf("pageSize 超上限时 len = %d, 期望回落默认分页且返回 4 条", len(rsBig))
		}
	})

	t.Run("来源统计与删除", func(t *testing.T) {
		counts, err := CountMonitorRecordsBySource()
		if err != nil {
			t.Fatalf("统计失败：%v", err)
		}
		if counts["123"] != 3 || counts["pan139"] != 1 {
			t.Errorf("来源统计 = %v, 期望 123→3 / pan139→1", counts)
		}
		rs, _, _ := ListMonitorRecords("pan139", "", "", 1, 20)
		if err := DeleteMonitorRecords([]uint{rs[0].ID}); err != nil {
			t.Fatalf("删除失败：%v", err)
		}
		if _, total, _ := ListMonitorRecords("pan139", "", "", 1, 20); total != 0 {
			t.Errorf("删除后 total = %d, 期望 0", total)
		}
	})
}

func TestChannelCRUD(t *testing.T) {
	setupTestDB(t)
	ch := &DiscoveryChannel{SourceType: "123", Channel: "@demo_chan"}
	if err := SaveChannel(ch); err != nil {
		t.Fatalf("创建频道失败：%v", err)
	}
	if ch.ID == 0 {
		t.Fatal("创建后 ID 不应为 0")
	}
	if !ch.Enabled {
		t.Error("新建频道应默认启用")
	}

	got, err := GetChannelBySource("123", "https://t.me/demo_chan")
	if err != nil || got == nil {
		t.Fatalf("按归一化名查询失败：%v, got=%v", err, got)
	}
	if got.ID != ch.ID {
		t.Errorf("查询到 ID = %d, 期望 %d", got.ID, ch.ID)
	}
	if other, _ := GetChannelBySource("pan139", "demo_chan"); other != nil {
		t.Error("其他网盘不应查到该频道")
	}

	if err := SetChannelEnabled(ch.ID, false); err != nil {
		t.Fatalf("停用失败：%v", err)
	}
	enabled, err := ListEnabledChannels("123")
	if err != nil {
		t.Fatalf("查询启用频道失败：%v", err)
	}
	if len(enabled) != 0 {
		t.Errorf("停用后启用列表 len = %d, 期望 0", len(enabled))
	}

	if err := UpdateChannelCursor(ch.ID, "12345", time.Now()); err != nil {
		t.Fatalf("更新游标失败：%v", err)
	}
	got, _ = GetChannel(ch.ID)
	if got.LastPostID != "12345" {
		t.Errorf("游标 = %q, 期望 12345", got.LastPostID)
	}
	if got.Enabled {
		t.Error("更新游标不应把 enabled 改回 true")
	}
	if got.LastRunAt.IsZero() {
		t.Error("更新游标应同时写入 last_run_at")
	}

	all, err := ListChannels("")
	if err != nil || len(all) != 1 {
		t.Fatalf("全量列表 len = %d (err=%v), 期望 1", len(all), err)
	}
	if err := DeleteChannel(ch.ID); err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	if all, _ = ListChannels("123"); len(all) != 0 {
		t.Errorf("删除后 len = %d, 期望 0", len(all))
	}
}
