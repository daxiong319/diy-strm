package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"litepan/internal/discover/ddb"
	"litepan/internal/domain"
	"litepan/internal/settings"
)

// 本文件锁死 T16 RSS 订阅源的**服务层**行为。
//
// 覆盖任务书验收 ①~⑦ 里的服务端部分：
//
//	① 贴上 feed 能抓到条目并落地
//	② guid/info_hash 去重生效（第二轮不再提交）
//	③ 停机超 12 小时只从最新一条开始，不追补全部历史
//	④ 走同一条订阅执行器（账本 + 词表过滤真被调到）
//	⑤ 非 UTF-8 feed 明确报「不支持」，不静默失败
//	⑦ 一个坏 URL/超时 feed 不影响其它源
//
// 与 rss 包内 parse_test.go 的分工：那边测「怎么解析」，这里测
// 「解析完之后这条链路怎么走」——位点、去重、投递、失败语义。

// ---------------------------------------------------------------------------
// 测试替身
// ---------------------------------------------------------------------------

type fakeRSSSourceStore struct {
	mu      sync.Mutex
	byID    map[int64]domain.RSSSource
	nextID  int64
	marked  map[int64]string
	markMsg map[int64]string
}

func newFakeRSSSourceStore() *fakeRSSSourceStore {
	return &fakeRSSSourceStore{
		byID:    map[int64]domain.RSSSource{},
		nextID:  1,
		marked:  map[int64]string{},
		markMsg: map[int64]string{},
	}
}

func (f *fakeRSSSourceStore) List(_ context.Context, q domain.RSSSourceQuery) ([]domain.RSSSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.RSSSource, 0, len(f.byID))
	for _, s := range f.byID {
		if q.Enabled != nil && s.Enabled != *q.Enabled {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

func (f *fakeRSSSourceStore) Get(_ context.Context, id int64) (domain.RSSSource, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byID[id]
	return s, ok, nil
}

func (f *fakeRSSSourceStore) GetByURL(_ context.Context, u string) (domain.RSSSource, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.byID {
		if s.RssURL == u {
			return s, true, nil
		}
	}
	return domain.RSSSource{}, false, nil
}

func (f *fakeRSSSourceStore) Upsert(_ context.Context, p domain.RSSUpsertPayload) (domain.RSSSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p.ID > 0 {
		s, ok := f.byID[p.ID]
		if !ok {
			return domain.RSSSource{}, errors.New("源不存在")
		}
		if p.Name != "" {
			s.Name = p.Name
		}
		if p.RssURL != "" {
			s.RssURL = p.RssURL
		}
		f.byID[p.ID] = s
		return s, nil
	}
	s := domain.RSSSource{ID: f.nextID, Name: p.Name, RssURL: p.RssURL, Enabled: true}
	f.nextID++
	f.byID[s.ID] = s
	return s, nil
}

func (f *fakeRSSSourceStore) Delete(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.byID, id)
	return nil
}

func (f *fakeRSSSourceStore) MarkSyncResult(_ context.Context, id int64, status, message string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marked[id] = status
	f.markMsg[id] = message
	return nil
}

func (f *fakeRSSSourceStore) ListEnabled(_ context.Context) ([]domain.RSSSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.RSSSource, 0, len(f.byID))
	for _, s := range f.byID {
		if s.Enabled {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeRSSSourceStore) markOf(id int64) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.marked[id]
}

type fakeRSSHistoryStore struct {
	mu     sync.Mutex
	rows   []domain.RSSHistory
	nextID int64
	insErr error
}

func newFakeRSSHistoryStore() *fakeRSSHistoryStore { return &fakeRSSHistoryStore{nextID: 1} }

func (f *fakeRSSHistoryStore) InsertOnce(_ context.Context, h domain.RSSHistory) (domain.RSSHistory, bool, error) {
	if f.insErr != nil {
		return domain.RSSHistory{}, false, f.insErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.rows {
		if row.Guid == h.Guid {
			return row, false, nil
		}
	}
	h.ID = f.nextID
	f.nextID++
	h.CreatedAt = time.Now()
	f.rows = append(f.rows, h)
	return h, true, nil
}

func (f *fakeRSSHistoryStore) HasGuid(_ context.Context, guid string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.rows {
		if row.Guid == guid {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeRSSHistoryStore) List(_ context.Context, _ domain.RSSHistoryQuery) ([]domain.RSSHistory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.RSSHistory(nil), f.rows...), nil
}

func (f *fakeRSSHistoryStore) DeleteBySource(_ context.Context, sourceID int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.rows[:0]
	var n int64
	for _, row := range f.rows {
		if row.SourceID == sourceID {
			n++
			continue
		}
		kept = append(kept, row)
	}
	f.rows = kept
	return n, nil
}

func (f *fakeRSSHistoryStore) DeleteByID(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, row := range f.rows {
		if row.ID == id {
			f.rows = append(f.rows[:i], f.rows[i+1:]...)
			return nil
		}
	}
	return nil
}

// seed 预置一条历史（不走 InsertOnce，好把 created_at 定在任意时刻）。
//
// 位点 LatestProcessedAt 读的就是这张表的 created_at，所以「停机多久」
// 只能靠把行的时间往回拨来模拟 —— 没法真的等 13 小时。
func (f *fakeRSSHistoryStore) seed(sourceID int64, guid, title string, createdAt time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, domain.RSSHistory{
		ID: f.nextID, SourceID: sourceID, SourceName: "seed", Guid: guid, Title: title,
		Status: domain.RSSStatusSuccess, CreatedAt: createdAt,
	})
	f.nextID++
}

func (f *fakeRSSHistoryStore) LatestProcessedAt(_ context.Context, sourceID int64) (time.Time, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var best time.Time
	found := false
	for _, row := range f.rows {
		if row.SourceID != sourceID {
			continue
		}
		if !found || row.CreatedAt.After(best) {
			best, found = row.CreatedAt, true
		}
	}
	return best, found, nil
}

func (f *fakeRSSHistoryStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

func (f *fakeRSSHistoryStore) guids() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.rows))
	for _, row := range f.rows {
		out = append(out, row.Guid)
	}
	return out
}

// ---------------------------------------------------------------------------
// 装配辅助
// ---------------------------------------------------------------------------

// setupRSSServiceTest 装好 RSS 服务层需要的全部依赖，返回源/历史仓储替身。
//
// 少装一样症状都很隐蔽：RSSSourceStore/RSSHistoryStore 缺了
// RSSServicesReady 返回假（每个用例都直接 failed）；设置服务缺了
// rssCatchupGapHours 读不到、追赶分支永远走不到；媒体库探针缺了
// decideTransfer 会去打真 Emby；账本表缺了 ClaimTransferSlot 拿到 nil Db
// 并把每条都当成「抢到」。
func setupRSSServiceTest(t *testing.T, values map[string]string) (*fakeRSSSourceStore, *fakeRSSHistoryStore) {
	t.Helper()
	if ddb.Db == nil {
		if err := ddb.Init(filepath.Join(t.TempDir(), "rss_test.db"), slog.Default()); err != nil {
			t.Fatalf("初始化测试数据库失败：%v", err)
		}
	}
	for _, table := range []string{
		"discovery_transfer_items", "discovery_subscription_items",
		"discovery_subscriptions", "discovery_subscription_rules",
	} {
		if err := ddb.Db.Exec("DROP TABLE IF EXISTS " + table).Error; err != nil {
			t.Fatalf("清理表 %s 失败：%v", table, err)
		}
	}
	if err := ddb.Db.AutoMigrate(&DiscoveryTransferItem{}, &DiscoverySubscriptionItem{},
		&DiscoverySubscription{}, &DiscoverySubscriptionRule{}); err != nil {
		t.Fatalf("建表失败：%v", err)
	}

	merged := map[string]string{}
	for k, v := range values {
		merged[k] = v
	}
	svc, err := settings.New(context.Background(), &identitySettingsRepo{values: merged})
	if err != nil {
		t.Fatalf("构造设置服务失败：%v", err)
	}
	BindSettings(svc)
	ResetMediaLibraryCache()
	setMediaLibraryProbe(fakeLibrary)

	src := newFakeRSSSourceStore()
	hist := newFakeRSSHistoryStore()
	RSSSourceStore = src
	RSSHistoryStore = hist

	t.Cleanup(func() {
		BindSettings(nil)
		ResetMediaLibraryCache()
		setMediaLibraryProbe(nil)
		RSSSourceStore = nil
		RSSHistoryStore = nil
		OfflineLinkFn = nil
		TransferShareFn = nil
	})
	return src, hist
}

// rssTestFeed 生成一份合法的 RSS 2.0 feed。
//
// 刻意**不给 <guid>**：去重键的兜底链是 guid → info_hash → link → 合成键，
// 而 info_hash 那一级才是 BT RSS 的常态（很多源不写 guid），也是 参考实现
// 侧只在 .so 里可见、无法照抄的那一级。用例都从这里过，才能真的测到它。
func rssTestFeed(items ...rssTestItem) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:content="http://purl.org/rss/1.0/modules/content/">
<channel>
<title>Test Feed</title>
<link>https://example.test/rss</link>
<description>test</description>`)
	for i, it := range items {
		b.WriteString("\n<item>\n")
		fmt.Fprintf(&b, "<title>%s</title>\n", it.Title)
		fmt.Fprintf(&b, "<link>https://example.test/item/%d</link>\n", i+1)
		fmt.Fprintf(&b, "<description>%s</description>\n", it.Description)
		fmt.Fprintf(&b, "<pubDate>%s</pubDate>\n", it.Published.UTC().Format(time.RFC1123Z))
		if it.Guid != "" {
			fmt.Fprintf(&b, "<guid isPermaLink=\"false\">%s</guid>\n", it.Guid)
		}
		if it.Resource != "" {
			fmt.Fprintf(&b,
				"<enclosure url=\"%s\" type=\"application/x-bittorrent\" length=\"123456\"/>\n",
				strings.NewReplacer("&", "&amp;", `"`, "&quot;").Replace(it.Resource))
		}
		b.WriteString("</item>")
	}
	b.WriteString("\n</channel>\n</rss>")
	return b.String()
}

type rssTestItem struct {
	Title       string
	Description string
	Published   time.Time
	Guid        string
	Resource    string
}

func magnetFor(hash string) string {
	return "magnet:?xt=urn:btih:" + strings.ToLower(hash) + "&dn=Test+Show"
}

const (
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hashC = "cccccccccccccccccccccccccccccccccccccccc"
)

// rssTestSubmitter 记录提交了什么链接。
type rssTestSubmitter struct {
	mu     sync.Mutex
	links  []string
	paths  []string
	failOn string // 命中该链接则返回错误
}

func (s *rssTestSubmitter) fn() func(ctx context.Context, link, savePath string) error {
	return func(_ context.Context, link, savePath string) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.failOn != "" && strings.Contains(link, s.failOn) {
			return errors.New("离线下载器拒绝了该任务")
		}
		s.links = append(s.links, link)
		s.paths = append(s.paths, savePath)
		return nil
	}
}

func (s *rssTestSubmitter) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.links)
}

func (s *rssTestSubmitter) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.links...)
}

// rssFeedServer 起一个只返回给定 feed 的服务器。
func rssFeedServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// rssAddSource 往仓储替身里塞一个源。
func rssAddSource(t *testing.T, store *fakeRSSSourceStore, name, url string) domain.RSSSource {
	t.Helper()
	src, err := store.Upsert(context.Background(), domain.RSSUpsertPayload{
		Name: name, RssURL: url, MediaType: "tv", Action: domain.RSSActionOffline,
	})
	if err != nil {
		t.Fatalf("新建源 %q 失败：%v", name, err)
	}
	return src
}

// ---------------------------------------------------------------------------
// 验收 ①：贴上 feed 能抓到条目并落地
// ---------------------------------------------------------------------------

func TestRSSSyncFetchesAndSubmitsMagnet(t *testing.T) {
	srcStore, _ := setupRSSServiceTest(t, nil)
	offline := &rssTestSubmitter{}
	OfflineLinkFn = offline.fn()

	srv := rssFeedServer(t, rssTestFeed(rssTestItem{
		Title:       "[Lilith-Raws] 某番剧 S01E01 [Baha][WEB-DL][1080p]",
		Description: "1080P 中文字幕",
		Published:   time.Now(),
		Resource:    magnetFor(hashA),
	}))
	src := rssAddSource(t, srcStore, "某番剧", srv.URL)

	res := SyncRSSSource(context.Background(), src)
	if res.Status != domain.RSSStatusSuccess {
		t.Fatalf("状态 = %q，期望 success（message=%q detail=%+v）", res.Status, res.Message, res.Detail)
	}
	if res.Counters.Added != 1 {
		t.Fatalf("新增 = %d，期望 1", res.Counters.Added)
	}
	if res.Counters.Failed != 0 {
		t.Errorf("失败 = %d，期望 0（%+v）", res.Counters.Failed, res.Detail)
	}
	links := offline.snapshot()
	if len(links) != 1 {
		t.Fatalf("提交次数 = %d，期望 1", len(links))
	}
	if !strings.HasPrefix(links[0], "magnet:?xt=urn:btih:") {
		t.Errorf("提交的链接 = %q，期望是磁力链接", links[0])
	}
	if !strings.Contains(strings.ToLower(links[0]), hashA) {
		t.Errorf("提交的链接丢了 info_hash：%q", links[0])
	}
	if got := srcStore.markOf(src.ID); got != domain.RSSStatusSuccess {
		t.Errorf("回写的源状态 = %q，期望 success（不同步回写的话，列表页永远显示「从未同步」）", got)
	}
}

// TestRSSSyncWritesHistoryWithDedupKey 断言写进历史的 guid 真的是去重键本身。
//
// 这条比看起来重要：guid 由 service 层算一次、跟着 RSSCandidate.Key 一路
// 传到写历史。如果某处改成事后从候选反算，两处算出的值会不同，
// 于是「历史里已有 guid」挡不住下一轮 —— 症状是同一条目被反复提交下载，
// 而日志、唯一约束、状态全都正常，看不出任何异常。
func TestRSSSyncWritesHistoryWithDedupKey(t *testing.T) {
	srcStore, hist := setupRSSServiceTest(t, nil)
	OfflineLinkFn = (&rssTestSubmitter{}).fn()

	srv := rssFeedServer(t, rssTestFeed(rssTestItem{
		Title: "某番剧 S01E02", Published: time.Now(), Resource: magnetFor(hashB),
	}))
	src := rssAddSource(t, srcStore, "番剧源", srv.URL)

	if res := SyncRSSSource(context.Background(), src); res.Counters.Added != 1 {
		t.Fatalf("首轮新增 = %d，期望 1", res.Counters.Added)
	}
	guids := hist.guids()
	if len(guids) != 1 {
		t.Fatalf("历史条数 = %d，期望 1", len(guids))
	}
	// info_hash 兜底：guid 里应能认出这条目的资源身份。
	if guids[0] == "" {
		t.Fatal("写进历史的 guid 为空 —— 去重约束会失效，同一条目会被反复提交")
	}
	if !strings.Contains(guids[0], hashB) && !strings.Contains(guids[0], "synthetic:") {
		t.Errorf("guid = %q，既不是资源身份也不是合成键，兜底链可能走漏了", guids[0])
	}
}

// ---------------------------------------------------------------------------
// 验收 ②：去重生效
// ---------------------------------------------------------------------------

// TestRSSSecondRoundDoesNotResubmit 同一个 feed 连同步两轮，只提交一次。
//
// 注意这测的是**两层去重同时生效**：账本按 info_hash 挡、history 按 guid
// 挡。哪一层被摘掉结果都相同（提交一次），所以这条只证明「整体幂等」。
func TestRSSSecondRoundDoesNotResubmit(t *testing.T) {
	srcStore, hist := setupRSSServiceTest(t, nil)
	offline := &rssTestSubmitter{}
	OfflineLinkFn = offline.fn()

	srv := rssFeedServer(t, rssTestFeed(rssTestItem{
		Title: "某番剧 S01E03", Published: time.Now(), Resource: magnetFor(hashC),
	}))
	src := rssAddSource(t, srcStore, "番剧源", srv.URL)

	first := SyncRSSSource(context.Background(), src)
	if first.Counters.Added != 1 {
		t.Fatalf("首轮新增 = %d，期望 1", first.Counters.Added)
	}
	second := SyncRSSSource(context.Background(), src)
	if second.Counters.Added != 0 {
		t.Errorf("二轮新增 = %d，期望 0（同一个 feed 条目被重复提交下载了）", second.Counters.Added)
	}
	if second.Counters.Failed != 0 {
		t.Errorf("二轮失败 = %d，期望 0（去重不该产生失败）", second.Counters.Failed)
	}
	if got := offline.count(); got != 1 {
		t.Errorf("累计提交 = %d，期望 1", got)
	}
	if got := hist.count(); got != 1 {
		t.Errorf("历史条数 = %d，期望 1", got)
	}
}

// TestRSSSameHashDifferentURLIsStillDeduped 同一部片子在两个源里 URL 不同，
// 必须仍然只提交一次。
//
// 落到 info_hash 这一级才成立：去重键取的是 enclosure 里的磁力哈希，
// 不是 <link>。如果实现改成优先用 link，两个源会各提交一次 —— 同一份
// 资源下了两遍，而用户看到的是两个不同的源，完全合理、不会怀疑去重。
func TestRSSSameHashDifferentURLIsStillDeduped(t *testing.T) {
	srcStore, _ := setupRSSServiceTest(t, nil)
	offline := &rssTestSubmitter{}
	OfflineLinkFn = offline.fn()

	item := rssTestItem{Title: "某番剧 S01E04", Published: time.Now(), Resource: magnetFor(hashA)}
	srcA := rssAddSource(t, srcStore, "源甲", rssFeedServer(t, rssTestFeed(item)).URL)

	// 同一部片子，标题与 link 都不同，只有磁力哈希一样。
	item2 := item
	item2.Title = "某番剧 S01E04 1080p 重制"
	srvB := rssFeedServer(t, rssTestFeed(item2))
	srcB := rssAddSource(t, srcStore, "源乙", srvB.URL)

	SyncRSSSource(context.Background(), srcA)
	resB := SyncRSSSource(context.Background(), srcB)
	if resB.Counters.Added != 0 {
		t.Errorf("源乙新增 = %d，期望 0（同一 info_hash 被提交了两次）", resB.Counters.Added)
	}
	if got := offline.count(); got != 1 {
		t.Errorf("累计提交 = %d，期望 1", got)
	}
}

// TestRSSGuidFromFeedWins 显式 <guid> 优先于 info_hash。
func TestRSSGuidFromFeedWins(t *testing.T) {
	srcStore, hist := setupRSSServiceTest(t, nil)
	OfflineLinkFn = (&rssTestSubmitter{}).fn()

	srv := rssFeedServer(t, rssTestFeed(rssTestItem{
		Title:     "某番剧 S01E05",
		Published: time.Now(),
		Guid:      "mikanani.me/1-1024",
		Resource:  magnetFor(hashA),
	}))
	src := rssAddSource(t, srcStore, "源甲", srv.URL)
	SyncRSSSource(context.Background(), src)

	guids := hist.guids()
	if len(guids) != 1 || guids[0] != "mikanani.me/1-1024" {
		t.Errorf("guid = %v，期望原样用 feed 给的 [mikanani.me/1-1024]", guids)
	}
}

// ---------------------------------------------------------------------------
// 验收 ③：停机超 12 小时从最新消息开始
// ---------------------------------------------------------------------------

func TestRSSShouldCatchUp(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name      string
		lastAt    time.Time
		hasCursor bool
		gapHours  int
		want      bool
	}{
		{"首次同步没有位点不算停机", now, false, 12, false},
		{"刚同步过不算停机", now.Add(-11 * time.Hour), true, 12, false},
		{"超过 12 小时算停机", now.Add(-13 * time.Hour), true, 12, true},
		{"gap=0 关闭追赶", now.Add(-100 * time.Hour), true, 0, false},
		{"gap 很大时不追赶", now.Add(-2 * time.Hour), true, 168, false},
	}
	for _, c := range cases {
		if got := rssShouldCatchUp(c.lastAt, c.hasCursor, c.gapHours); got != c.want {
			t.Errorf("%s：rssShouldCatchUp = %v，期望 %v", c.name, got, c.want)
		}
	}
}

// TestRSSDowntimeOnlyTakesNewestItem 停机 13 小时后同步，只处理最新一条，
// 不把停机期间的历史全部补下（验收 ③）。
func TestRSSDowntimeOnlyTakesNewestItem(t *testing.T) {
	srcStore, hist := setupRSSServiceTest(t, nil)
	offline := &rssTestSubmitter{}
	OfflineLinkFn = offline.fn()

	src := rssAddSource(t, srcStore, "番剧源", "https://example.test/feed")
	// 位点 = 该源最近一条历史的 created_at。往回拨 13 小时即「停机 13 小时」。
	hist.seed(src.ID, "seed-1", "上一次处理过的条目", time.Now().Add(-13*time.Hour))

	srv := rssFeedServer(t, rssTestFeed(
		rssTestItem{Title: "最新条目 S01E10", Published: time.Now(), Resource: magnetFor(hashC)},
		rssTestItem{Title: "历史条目 S01E09", Published: time.Now().Add(-6 * time.Hour), Resource: magnetFor(hashB)},
		rssTestItem{Title: "更老的条目 S01E08", Published: time.Now().Add(-12 * time.Hour), Resource: magnetFor(hashA)},
	))
	src.RssURL = srv.URL

	res := SyncRSSSource(context.Background(), src)
	if !res.Catchup {
		t.Fatalf("走了非追赶模式（message=%q notices=%v）", res.Message, res.Notices)
	}
	if res.Counters.Added != 1 {
		t.Fatalf("新增 = %d，期望 1（停机期间的历史不该被补下）", res.Counters.Added)
	}
	if len(res.Notices) == 0 {
		t.Error("追赶时没有下发放映口径提示 —— 用户会以为漏抓了")
	}
	links := offline.snapshot()
	if len(links) != 1 || !strings.Contains(strings.ToLower(links[0]), hashC) {
		t.Errorf("提交了 %v，期望只有最新那条（含 %s）", links, hashC[:8])
	}
}

// TestRSSFirstSyncDoesNotCatchUp 首次同步（没有位点）必须把 feed 现有条目
// 都过一遍，不能只取最新一条。
//
// 这两条互为反面，一起写才挡得住「把 hasCursor 判断漏掉」这种改法：
// 少了它，用户刚贴上 URL 点同步，看到的就是「新增 0」。
func TestRSSFirstSyncDoesNotCatchUp(t *testing.T) {
	srcStore, _ := setupRSSServiceTest(t, nil)
	offline := &rssTestSubmitter{}
	OfflineLinkFn = offline.fn()

	srv := rssFeedServer(t, rssTestFeed(
		rssTestItem{Title: "条目 S01E01", Published: time.Now(), Resource: magnetFor(hashA)},
		rssTestItem{Title: "条目 S01E02", Published: time.Now(), Resource: magnetFor(hashB)},
		rssTestItem{Title: "条目 S01E03", Published: time.Now(), Resource: magnetFor(hashC)},
	))
	src := rssAddSource(t, srcStore, "新源", srv.URL)

	res := SyncRSSSource(context.Background(), src)
	if res.Catchup {
		t.Error("首次同步走了追赶模式 —— 刚贴 URL 的用户会看到「新增 0」")
	}
	if res.Counters.Added != 3 {
		t.Fatalf("新增 = %d，期望 3（首次同步要把 feed 现有条目都过一遍）", res.Counters.Added)
	}
	if got := offline.count(); got != 3 {
		t.Errorf("提交 = %d，期望 3", got)
	}
}

// ---------------------------------------------------------------------------
// 验收 ⑤：非 UTF-8 feed 明确报「不支持」
// ---------------------------------------------------------------------------

// TestRSSNonUTF8FeedFailsLoudly GBK feed 必须整轮失败并说清原因。
//
// 关键是「失败且可见」，不是「不崩溃」也不是「抓到一半」。
// 最坏的一种改法是让它返回空条目 + status=skipped：用户看到
// 「同步完成：没有新条目」，从此再也不回来看这个源。
func TestRSSNonUTF8FeedFailsLoudly(t *testing.T) {
	srcStore, hist := setupRSSServiceTest(t, nil)
	offline := &rssTestSubmitter{}
	OfflineLinkFn = offline.fn()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml; charset=gbk")
		// 真的用 GBK 字节：标题里放一个双字节汉字（"番"的 GBK 编码）。
		body := `<?xml version="1.0" encoding="GBK"?><rss version="2.0"><channel><title>test</title>` +
			"<item><title>" + string([]byte{0xb7, 0xbd, 0xd0, 0xde}) + "</title>" +
			`<enclosure url="` + magnetFor(hashA) + `"/></item></channel></rss>`
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	src := rssAddSource(t, srcStore, "GBK 源", srv.URL)

	res := SyncRSSSource(context.Background(), src)
	if res.Status != domain.RSSStatusFailed {
		t.Fatalf("状态 = %q，期望 failed（message=%q）", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "UTF-8") {
		t.Errorf("message = %q，期望点明「只支持 UTF-8」—— 用户看不出该换什么源", res.Message)
	}
	if got := srcStore.markOf(src.ID); got != domain.RSSStatusFailed {
		t.Errorf("回写源状态 = %q，期望 failed（否则列表页看起来一切正常）", got)
	}
	if offline.count() != 0 {
		t.Errorf("不支持的编码下提交了 %d 次，期望 0", offline.count())
	}
	if hist.count() != 0 {
		t.Errorf("不支持的编码下写了 %d 条历史，期望 0", hist.count())
	}
}

// TestRSSUnknownCharsetAlsoFails 未声明编码但内容不是 UTF-8 同样要报错。
func TestRSSUnknownCharsetAlsoFails(t *testing.T) {
	srcStore, _ := setupRSSServiceTest(t, nil)
	OfflineLinkFn = (&rssTestSubmitter{}).fn()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		body := `<?xml version="1.0"?><rss version="2.0"><channel><title>test</title>` +
			"<item><title>" + string([]byte{0xb7, 0xbd, 0xd0, 0xde}) + "</title></item></channel></rss>"
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	src := rssAddSource(t, srcStore, "乱码源", srv.URL)

	res := SyncRSSSource(context.Background(), src)
	if res.Status != domain.RSSStatusFailed {
		t.Errorf("状态 = %q，期望 failed", res.Status)
	}
}

// ---------------------------------------------------------------------------
// 验收 ⑦：一个坏源不影响其它源
// ---------------------------------------------------------------------------

// TestRSSBadSourceDoesNotBlockOthers 三个源：好、404 超时、好。
// SyncAllRSSSources 必须让两个好源都真的落地。
func TestRSSBadSourceDoesNotBlockOthers(t *testing.T) {
	srcStore, _ := setupRSSServiceTest(t, map[string]string{"mo_rss_http_timeout_seconds": "2"})
	offline := &rssTestSubmitter{}
	OfflineLinkFn = offline.fn()

	good := func(title, hash string) string {
		return rssTestFeed(rssTestItem{Title: title, Published: time.Now(), Resource: magnetFor(hash)})
	}
	srvA := rssFeedServer(t, good("源甲 条目 S01E01", hashA))
	srvC := rssFeedServer(t, good("源丙 条目 S01E01", hashC))

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	}))
	defer dead.Close()

	rssAddSource(t, srcStore, "源甲", srvA.URL)
	rssAddSource(t, srcStore, "源乙(404)", dead.URL)
	rssAddSource(t, srcStore, "源丙", srvC.URL)

	results := SyncAllRSSSources(context.Background())
	if len(results) != 3 {
		t.Fatalf("返回 %d 个结果，期望 3（一个源失败不该让整轮提前返回）", len(results))
	}
	var okCount, failCount int
	for _, r := range results {
		switch r.Status {
		case domain.RSSStatusSuccess:
			okCount++
		case domain.RSSStatusFailed:
			failCount++
		}
	}
	if okCount != 2 {
		t.Errorf("成功源 = %d，期望 2（坏源把好源带崩了）", okCount)
	}
	if failCount != 1 {
		t.Errorf("失败源 = %d，期望 1", failCount)
	}
	if got := offline.count(); got != 2 {
		t.Errorf("提交 = %d，期望 2（两个好源各一条）", got)
	}
}

// TestRSSConnectionRefusedFailsOnlyThatSource 抓不到（连不上）只影响自己。
func TestRSSConnectionRefusedFailsOnlyThatSource(t *testing.T) {
	srcStore, _ := setupRSSServiceTest(t, nil)
	OfflineLinkFn = (&rssTestSubmitter{}).fn()

	// 先起一个服务器拿到一个必然空闲的端口，然后立刻关掉。
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	srv := rssFeedServer(t, rssTestFeed(rssTestItem{
		Title: "好源条目", Published: time.Now(), Resource: magnetFor(hashA),
	}))
	rssAddSource(t, srcStore, "连不上的源", deadURL)
	good := rssAddSource(t, srcStore, "好源", srv.URL)

	results := SyncAllRSSSources(context.Background())
	var bad, good1 bool
	for _, r := range results {
		if r.Source.ID == good.ID && r.Status == domain.RSSStatusSuccess {
			good1 = true
		}
		if r.Source.ID != good.ID && r.Status == domain.RSSStatusFailed {
			bad = true
		}
	}
	if !good1 {
		t.Error("好源没有成功 —— 一个连不上的源把整轮拖垮了")
	}
	if !bad {
		t.Error("连不上的源没有标记为 failed（连不上必须可见，不能静默跳过）")
	}
}

// ---------------------------------------------------------------------------
// 过滤
// ---------------------------------------------------------------------------

// TestRSSIncludeExcludeRegex 正则过滤在投递之前生效（验收里的「命中才下载」）。
func TestRSSIncludeExcludeRegex(t *testing.T) {
	srcStore, _ := setupRSSServiceTest(t, nil)
	offline := &rssTestSubmitter{}
	OfflineLinkFn = offline.fn()

	srv := rssFeedServer(t, rssTestFeed(
		rssTestItem{Title: "[Group] Show S01E01 [1080p]", Published: time.Now(), Resource: magnetFor(hashA)},
		rssTestItem{Title: "[Group] Show S01E02 [720p]", Published: time.Now(), Resource: magnetFor(hashB)},
		rssTestItem{Title: "[Group] Show S01E03 [2160p]", Published: time.Now(), Resource: magnetFor(hashC)},
	))
	src := rssAddSource(t, srcStore, "过滤源", srv.URL)
	src.IncludeRegex = `(?i)2160p`
	src.ExcludeRegex = `(?i)Group2`

	res := SyncRSSSource(context.Background(), src)
	if res.Counters.Added != 1 {
		t.Fatalf("新增 = %d，期望 1（含过滤应该只剩 2160p 那条）", res.Counters.Added)
	}
	links := offline.snapshot()
	if len(links) != 1 || !strings.Contains(links[0], hashC) {
		t.Errorf("提交了 %v，期望只有 2160p 那条（含 %s）", links, hashC[:8])
	}
}

// TestRSSInvalidRegexFailsRound 正则不合法时整轮失败并说清是哪个框。
func TestRSSInvalidRegexFailsRound(t *testing.T) {
	srcStore, _ := setupRSSServiceTest(t, nil)
	OfflineLinkFn = (&rssTestSubmitter{}).fn()

	srv := rssFeedServer(t, rssTestFeed(rssTestItem{
		Title: "条目", Published: time.Now(), Resource: magnetFor(hashA),
	}))
	src := rssAddSource(t, srcStore, "坏正则源", srv.URL)
	src.IncludeRegex = "(" // 未闭合的分组

	res := SyncRSSSource(context.Background(), src)
	if res.Status != domain.RSSStatusFailed {
		t.Fatalf("状态 = %q，期望 failed", res.Status)
	}
	if !strings.Contains(res.Message, "包含") {
		t.Errorf("message = %q，期望点明是「包含过滤」那一栏", res.Message)
	}
}

// TestRSSStaleItemsAreSkipped 过期条目（早于 mo_rss_stale_after_minutes）不提交。
func TestRSSStaleItemsAreSkipped(t *testing.T) {
	srcStore, _ := setupRSSServiceTest(t, map[string]string{"mo_rss_stale_after_minutes": "60"})
	offline := &rssTestSubmitter{}
	OfflineLinkFn = offline.fn()

	srv := rssFeedServer(t, rssTestFeed(
		rssTestItem{Title: "新鲜的", Published: time.Now(), Resource: magnetFor(hashA)},
		rssTestItem{Title: "六小时前的", Published: time.Now().Add(-6 * time.Hour), Resource: magnetFor(hashB)},
	))
	src := rssAddSource(t, srcStore, "过期源", srv.URL)

	res := SyncRSSSource(context.Background(), src)
	if res.Counters.Added != 1 {
		t.Fatalf("新增 = %d，期望 1（6 小时前的条目超过 60 分钟阈值）", res.Counters.Added)
	}
	if got := offline.count(); got != 1 {
		t.Errorf("提交 = %d，期望 1", got)
	}
}

// TestRSSMissingPubDateIsNotTreatedAsStale feed 不给 pubDate 时不能当成过期。
//
// 这是个真实会踩的坑：now.Sub(零值) 是 2000 多年，不判零值的话
// 整个 feed 会被当成过期，用户看到「同步完成：新增 0」。
func TestRSSMissingPubDateIsNotTreatedAsStale(t *testing.T) {
	srcStore, _ := setupRSSServiceTest(t, map[string]string{"mo_rss_stale_after_minutes": "60"})
	offline := &rssTestSubmitter{}
	OfflineLinkFn = offline.fn()

	// 注意 enclosure 的 url 要转义 & ：磁力链接里的 & 直接写进属性
	// 会让整个 XML 解析失败（magnet:?xt=…&dn=…），症状是「新增 0」。
	enclosure := `<enclosure url="` +
		strings.NewReplacer("&", "&amp;", `"`, "&quot;").Replace(magnetFor(hashA)) +
		`" type="application/x-bittorrent"/>`
	srv := rssFeedServer(t, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>t</title>
<item><title>没有 pubDate 的条目</title>`+enclosure+`
</item></channel></rss>`)
	src := rssAddSource(t, srcStore, "无日期源", srv.URL)

	res := SyncRSSSource(context.Background(), src)
	if res.Counters.Added != 1 {
		t.Fatalf("新增 = %d，期望 1（没有 pubDate 不该被当过期；status=%q message=%q）",
			res.Counters.Added, res.Status, res.Message)
	}
	if got := offline.count(); got != 1 {
		t.Errorf("提交 = %d，期望 1", got)
	}
}

// ---------------------------------------------------------------------------
// 失败语义
// ---------------------------------------------------------------------------

// TestRSSSubmitFailureIsRetryable 提交失败的条目本轮失败、下一轮还能重试。
//
// 「下一轮还能重试」靠两件事：失败不写历史（口径 3）+ 失败时
// FailLedgerEntry 把账本退回可抢状态。不做后者的话，一个临时失败
// 会被 3 小时保护期挡掉 —— 表现为「重启一次就再也不下了」。
func TestRSSSubmitFailureIsRetryable(t *testing.T) {
	srcStore, hist := setupRSSServiceTest(t, nil)
	offline := &rssTestSubmitter{failOn: hashA}
	OfflineLinkFn = offline.fn()

	srv := rssFeedServer(t, rssTestFeed(rssTestItem{
		Title: "会失败的条目", Published: time.Now(), Resource: magnetFor(hashA),
	}))
	src := rssAddSource(t, srcStore, "失败源", srv.URL)

	first := SyncRSSSource(context.Background(), src)
	if first.Status != domain.RSSStatusFailed || first.Counters.Failed != 1 {
		t.Fatalf("首轮 = %+v，期望 failed/1", first.Counters)
	}
	if hist.count() != 0 {
		t.Errorf("失败写了 %d 条历史，期望 0（下一轮就再也重试不了了）", hist.count())
	}

	// 修好离线下载器后重试：必须真的重下。
	OfflineLinkFn = (&rssTestSubmitter{}).fn()
	second := SyncRSSSource(context.Background(), src)
	if second.Counters.Added != 1 {
		t.Errorf("二轮新增 = %d，期望 1（临时失败被 3 小时保护期挡住了）", second.Counters.Added)
	}
}

// TestRSSPartialStatus 部分成功时状态是 partial 而不是 success。
func TestRSSPartialStatus(t *testing.T) {
	srcStore, _ := setupRSSServiceTest(t, nil)
	off2 := &rssTestSubmitter{failOn: hashB}
	OfflineLinkFn = off2.fn()

	srv := rssFeedServer(t, rssTestFeed(
		rssTestItem{Title: "能成的", Published: time.Now(), Resource: magnetFor(hashA)},
		rssTestItem{Title: "失败的", Published: time.Now(), Resource: magnetFor(hashB)},
	))
	src := rssAddSource(t, srcStore, "半成功源", srv.URL)

	res := SyncRSSSource(context.Background(), src)
	if res.Status != domain.RSSStatusPartial {
		t.Errorf("状态 = %q，期望 partial", res.Status)
	}
	if res.Counters.Added != 1 || res.Counters.Failed != 1 {
		t.Errorf("计数 = %+v，期望 1/1", res.Counters)
	}
}

// TestRSSNoResourceItemsDoNotOccupyGuid 更新通知类条目（无链接）不占去重键。
func TestRSSNoResourceItemsDoNotOccupyGuid(t *testing.T) {
	srcStore, _ := setupRSSServiceTest(t, nil)
	offline := &rssTestSubmitter{}
	OfflineLinkFn = offline.fn()

	srv := rssFeedServer(t, rssTestFeed(
		rssTestItem{Title: "站点维护通知", Published: time.Now()},
		rssTestItem{Title: "真有资源", Published: time.Now(), Resource: magnetFor(hashA)},
	))
	src := rssAddSource(t, srcStore, "混合源", srv.URL)

	res := SyncRSSSource(context.Background(), src)
	if res.Counters.Added != 1 {
		t.Fatalf("新增 = %d，期望 1", res.Counters.Added)
	}
	if res.Counters.Failed != 0 {
		t.Errorf("无资源条目被算成失败：%+v", res.Counters)
	}
	if got := offline.count(); got != 1 {
		t.Errorf("提交 = %d，期望 1", got)
	}
}

// TestRSSUnreadyServiceFailsLoudly 依赖没装时必须报「未就绪」而不是空结果。
func TestRSSUnreadyServiceFailsLoudly(t *testing.T) {
	setupRSSServiceTest(t, nil)
	RSSSourceStore = nil
	res := SyncRSSSource(context.Background(), domain.RSSSource{ID: 1, Name: "x", RssURL: "https://example.test/f"})
	if res.Status != domain.RSSStatusFailed || !strings.Contains(res.Message, "未就绪") {
		t.Errorf("= %+v，期望 failed 且说清「未就绪」", res)
	}
}
