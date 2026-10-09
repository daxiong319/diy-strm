package mediarequest

// 求片中心的业务单测。
//
// 这些用例盯的是**行为契约**，不是实现细节。挑的几条都对应一个
// 已经想清楚的失败模式：
//
//   - 审核通过到底有没有把订阅建出来（最贵的故障：界面上「已通过」但什么都没发生）；
//   - 超限时错误是 LimitError 且 Kind 正确（API 层靠 Kind 决定 429 还是 400，
//     靠中文文案判断的话，微调一下文案就静默变成 400 了）；
//   - 标签挂在整部剧上而不是某一季（任务书点名的行为）；
//   - 免审时超管不必等自己审核。

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	// 这里不空导入 modernc.org/sqlite：internal/store 已经间接引入它，
	// 再导入一次会 panic（sql: Register called twice for driver sqlite）。
	"litepan/internal/discover/discovery"
	"litepan/internal/settings"
	"litepan/internal/store"
)

// ---------------------------------------------------------------- 测试脚手架

// fakeCfg 是 Settings 的最小实现。用 map 而不是 settings.Service 是为了
// 让「这个键没人配」和「配成 0」能区分开。
type fakeCfg map[string]any

func (f fakeCfg) String(key string) string {
	if v, ok := f[key].(string); ok {
		return v
	}
	return ""
}

func (f fakeCfg) Bool(key string) bool {
	if v, ok := f[key].(bool); ok {
		return v
	}
	return false
}

func (f fakeCfg) Int(key string) int {
	if v, ok := f[key].(int); ok {
		return v
	}
	return 0
}

// defaultCfg 是「求片开着、每天 5 条、要审核、标签 20/100」。
func defaultCfg() fakeCfg {
	return fakeCfg{
		settings.KeyMOMediaRequestEnabled:       true,
		settings.KeyMOMediaRequestRequireReview: true,
		settings.KeyMOMediaRequestDailyLimit:    5,
		settings.KeyMOMediaRequestTagMaxPerUser: DefaultTagMaxPerUser,
		settings.KeyMOMediaRequestTagMaxLength:  DefaultTagMaxLength,
	}
}

// capturingSaver 记住每次 SaveSubscription 的入参。
//
// 它存在的理由和接口存在的一样：不启动整个订阅流水线就能断言
// 「审核通过到底有没有建订阅、建的订阅 season 是多少、source 是不是 request」。
type capturingSaver struct {
	payloads []*discovery.SubscriptionUpsertPayload
	nextID   uint
	err      error
	warning  string
	nilSub   bool
}

func (c *capturingSaver) SaveSubscription(p *discovery.SubscriptionUpsertPayload) (*discovery.DiscoverySubscription, string, error) {
	if c.err != nil {
		return nil, "", c.err
	}
	c.payloads = append(c.payloads, p)
	if c.nilSub {
		return nil, c.warning, nil
	}
	c.nextID++
	return &discovery.DiscoverySubscription{ID: c.nextID, Title: p.Title}, c.warning, nil
}

func (c *capturingSaver) last() *discovery.SubscriptionUpsertPayload {
	if len(c.payloads) == 0 {
		return nil
	}
	return c.payloads[len(c.payloads)-1]
}

// fakeItems 控制「东西到底入库了没」。
type fakeItems struct {
	transferred map[uint]bool
}

func (f fakeItems) ListSubscriptionItems(id uint, status string, limit int) ([]discovery.DiscoverySubscriptionItem, error) {
	if status == "transferred" && f.transferred[id] {
		return []discovery.DiscoverySubscriptionItem{{ID: 1, SubscriptionID: id, Status: "transferred"}}, nil
	}
	return nil, nil
}

type fixture struct {
	t     *testing.T
	svc   *Service
	store *Store
	subs  *capturingSaver
	cfg   fakeCfg
	now   time.Time
}

func newFixture(t *testing.T, cfg fakeCfg) *fixture {
	t.Helper()
	if cfg == nil {
		cfg = defaultCfg()
	}
	dir := t.TempDir()
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(dir, "req.db")})
	if err != nil {
		t.Fatalf("打开测试库失败：%v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("迁移测试库失败：%v", err)
	}
	st := NewStore(db.WriteHandle(), db.ReadHandle())
	subs := &capturingSaver{}
	f := &fixture{t: t, subs: subs, cfg: cfg}
	f.now = time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC)
	f.store = st
	f.svc = NewService(Params{
		Store:   st,
		Cfg:     cfg,
		Subs:    subs,
		Items:   fakeItems{},
		NowFunc: func() time.Time { return f.now },
	})
	return f
}

func (f *fixture) advance(d time.Duration) { f.now = f.now.Add(d) }

func family(id int64, name string) Actor {
	return Actor{UserID: id, Username: name}
}

func baseInput(a Actor, tmdbID int64, title string) SubmitInput {
	return SubmitInput{Requester: a, TMDBID: tmdbID, Title: title, MediaType: "tv"}
}

// ---------------------------------------------------------------- 提交

func TestSubmitNeedsReviewIsStoredAsPending(t *testing.T) {
	f := newFixture(t, nil)
	res, err := f.svc.Submit(context.Background(), baseInput(family(7, "小明"), 1001, "孤独摇滚"))
	if err != nil {
		t.Fatalf("提交失败：%v", err)
	}
	if res.Request.Status != StatusPending {
		t.Fatalf("状态 = %q，开了审核就该是 pending", res.Request.Status)
	}
	if res.Request.SubscriptionID != 0 {
		t.Fatalf("待审期间不该建订阅，却拿到了 subscription_id=%d", res.Request.SubscriptionID)
	}
	if len(f.subs.payloads) != 0 {
		t.Fatalf("待审期间不该调 SaveSubscription，实际调了 %d 次", len(f.subs.payloads))
	}
	if res.Request.RequesterName != "小明" {
		t.Fatalf("申请人名 = %q", res.Request.RequesterName)
	}
}

func TestSubmitWithoutReviewCreatesSubscriptionImmediately(t *testing.T) {
	cfg := defaultCfg()
	cfg[settings.KeyMOMediaRequestRequireReview] = false
	f := newFixture(t, cfg)
	res, err := f.svc.Submit(context.Background(), baseInput(family(7, "小明"), 1002, "孤独摇滚"))
	if err != nil {
		t.Fatalf("提交失败：%v", err)
	}
	if res.Request.Status != StatusApproved {
		t.Fatalf("免审时状态 = %q，应该是 approved", res.Request.Status)
	}
	if len(f.subs.payloads) != 1 {
		t.Fatalf("免审应立刻建订阅，实际调用 %d 次", len(f.subs.payloads))
	}
	p := f.subs.last()
	if p.Source != "request" {
		t.Fatalf("订阅 source = %q，必须是 request（否则迁移/来源筛选会漏掉它）", p.Source)
	}
	if res.Request.SubscriptionID == 0 {
		t.Fatal("免审建了订阅却没把 subscription_id 回写到求片单")
	}
	if res.Request.ReviewerID != 7 {
		t.Fatalf("免审时审核人应是提交人本人，实际 %d", res.Request.ReviewerID)
	}
	// 落库那一半同样要钉住：漏掉它，对账器每 15 分钟都会重跑一次建订阅，
	// 而且这条单永远不会被标成「已转存」。
	got, err := f.store.Get(context.Background(), res.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SubscriptionID != res.Request.SubscriptionID {
		t.Fatalf("免审单的 subscription_id 没落库：库里 %d，返回 %d",
			got.SubscriptionID, res.Request.SubscriptionID)
	}
}

// TestSubmitWarningReachesCaller 固化 SubmitResult.Warning 的用途：
// SaveSubscription 对「没配保存目录」只 warn 不 error，
// 如果前端只显示 message，家人会以为已经在盯着资源了。
func TestSubmitWarningReachesCaller(t *testing.T) {
	cfg := defaultCfg()
	cfg[settings.KeyMOMediaRequestRequireReview] = false
	f := newFixture(t, cfg)
	f.subs.warning = "没有配置目标目录"
	res, err := f.svc.Submit(context.Background(), baseInput(family(7, "小明"), 1003, "孤独摇滚"))
	if err != nil {
		t.Fatalf("提交不该因为 warning 而失败：%v", err)
	}
	if res.Warning == "" {
		t.Fatal("SaveSubscription 给了 warning，但 SubmitResult.Warning 是空的")
	}
}

func TestSuperBypassesReview(t *testing.T) {
	f := newFixture(t, nil)
	super := Actor{UserID: 0, Username: "admin", IsSuper: true}
	in := baseInput(super, 1004, "管理员想看的片")
	in.Requester = super
	res, err := f.svc.Submit(context.Background(), in)
	if err != nil {
		t.Fatalf("超管提交失败：%v", err)
	}
	if res.Request.Status != StatusApproved {
		t.Fatalf("超管在「开着审核」的机器上也得免审，实际状态 %q", res.Request.Status)
	}
}

func TestSubmitRejectsMissingFields(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	cases := []struct {
		name string
		mut  func(in *SubmitInput)
	}{
		{"空片名", func(in *SubmitInput) { in.Title = "   " }},
		{"没有作品 ID", func(in *SubmitInput) { in.TMDBID = 0 }},
		{"媒体类型不认识", func(in *SubmitInput) { in.MediaType = "anime" }},
		{"季数离谱", func(in *SubmitInput) { in.Season = 999 }},
	}
	for _, c := range cases {
		in := baseInput(family(7, "小明"), 2000, "片子")
		c.mut(&in)
		if _, err := f.svc.Submit(ctx, in); err == nil {
			t.Errorf("%s：应该被拒绝，却提交成功了", c.name)
		}
	}
}

func TestSubmitZeroesSeasonForMovie(t *testing.T) {
	f := newFixture(t, nil)
	in := baseInput(family(7, "小明"), 2001, "某电影")
	in.MediaType = "movie"
	in.Season = 5 // 手机端把剧和电影混在一个列表里，很容易带错
	res, err := f.svc.Submit(context.Background(), in)
	if err != nil {
		t.Fatalf("提交失败：%v", err)
	}
	if res.Request.Season != 0 {
		t.Fatalf("电影带进来的季数应该被清零，实际 %d", res.Request.Season)
	}
}

func TestSubmitRejectsDuplicatePending(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	if _, err := f.svc.Submit(ctx, baseInput(family(7, "小明"), 3001, "孤独摇滚")); err != nil {
		t.Fatalf("首次提交失败：%v", err)
	}
	_, err := f.svc.Submit(ctx, baseInput(family(8, "小红"), 3001, "孤独摇滚"))
	if !errors.Is(err, ErrAlreadyPending) {
		t.Fatalf("同一部待审中重复提交应报 ErrAlreadyPending，实际 %v", err)
	}
	// 错误文案要带上提交人 —— 「已经有人在求了」比裸错误有用得多。
	if !strings.Contains(err.Error(), "小明") {
		t.Fatalf("错误文案里没有提交人：%v", err)
	}
}

// ---------------------------------------------------------------- 上限

func TestDailyLimitProducesDailyKind(t *testing.T) {
	cfg := defaultCfg()
	cfg[settings.KeyMOMediaRequestDailyLimit] = 2
	f := newFixture(t, cfg)
	ctx := context.Background()
	for i, tmdb := range []int64{4001, 4002} {
		if _, err := f.svc.Submit(ctx, baseInput(family(7, "小明"), tmdb, "片子"+string(rune('A'+i)))); err != nil {
			t.Fatalf("第 %d 次提交失败：%v", i+1, err)
		}
	}
	_, err := f.svc.Submit(ctx, baseInput(family(7, "小明"), 4003, "第三部"))
	var le *LimitError
	if !errors.As(err, &le) {
		t.Fatalf("超每日上限应返回 *LimitError，实际 %v", err)
	}
	if le.Kind != LimitKindDaily {
		t.Fatalf("LimitError.Kind = %q，API 层靠它决定 429，别改", le.Kind)
	}
}

func TestDailyLimitResetsAtLocalMidnight(t *testing.T) {
	cfg := defaultCfg()
	cfg[settings.KeyMOMediaRequestDailyLimit] = 1
	f := newFixture(t, cfg)
	ctx := context.Background()
	if _, err := f.svc.Submit(ctx, baseInput(family(7, "小明"), 5001, "第一部")); err != nil {
		t.Fatalf("首次提交失败：%v", err)
	}
	if _, err := f.svc.Submit(ctx, baseInput(family(7, "小明"), 5002, "第二部")); err == nil {
		t.Fatal("同一天第二次提交应该被挡住")
	}
	// 跨过本地零点就该放行。
	// 存储里的时间戳是 UTC，如果按 UTC 零点算，这里在东八区会提前 8 小时放行。
	f.advance(14 * time.Hour)
	if _, err := f.svc.Submit(ctx, baseInput(family(7, "小明"), 5003, "第三部")); err != nil {
		t.Fatalf("跨过本地零点后应放行，实际：%v", err)
	}
}

func TestZeroDailyLimitMeansUnlimited(t *testing.T) {
	cfg := defaultCfg()
	cfg[settings.KeyMOMediaRequestDailyLimit] = 0
	f := newFixture(t, cfg)
	ctx := context.Background()
	for i := int64(0); i < 8; i++ {
		if _, err := f.svc.Submit(ctx, baseInput(family(7, "小明"), 6000+i, "片子")); err != nil {
			t.Fatalf("配置 0 时第 %d 次被挡了：%v", i+1, err)
		}
	}
}

func TestPendingLimitComesFromRule(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	if _, err := f.svc.SaveRules(ctx, []Rule{{
		Name: "只许攒一条", MediaType: "tv",
		PendingLimit: 1, AppliesToUserID: 7, Enabled: true, Priority: 10,
	}}); err != nil {
		t.Fatalf("保存规则失败：%v", err)
	}
	if _, err := f.svc.Submit(ctx, baseInput(family(7, "小明"), 7001, "第一")); err != nil {
		t.Fatalf("首次提交失败：%v", err)
	}
	_, err := f.svc.Submit(ctx, baseInput(family(7, "小明"), 7002, "第二"))
	var le *LimitError
	if !errors.As(err, &le) || le.Kind != LimitKindPending {
		t.Fatalf("第二条应报 LimitKindPending，实际 %v", err)
	}
	// 规则只对 7 号生效，8 号不受影响。
	if _, err := f.svc.Submit(ctx, baseInput(family(8, "小红"), 7003, "别人的")); err != nil {
		t.Fatalf("规则不该波及其他用户，实际：%v", err)
	}
}

// ---------------------------------------------------------------- 审核

func TestReviewApproveCreatesSubscriptionBeforePersisting(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	sub, err := f.svc.Submit(ctx, baseInput(family(7, "小明"), 8001, "孤独摇滚"))
	if err != nil {
		t.Fatalf("提交失败：%v", err)
	}
	res, err := f.svc.Review(ctx, family(0, "admin"), sub.Request.ID, true, "", "看过了")
	if err != nil {
		t.Fatalf("审核失败：%v", err)
	}
	if res.Request.Status != StatusApproved {
		t.Fatalf("审核后状态 = %q", res.Request.Status)
	}
	if res.Request.SubscriptionID == 0 {
		t.Fatal("审核通过却没有订阅关联 —— 界面上会显示「已通过」但什么都没发生")
	}
	// 落库的关联号必须与 SaveSubscription 返回的一致。
	got, err := f.store.Get(ctx, sub.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SubscriptionID != res.Request.SubscriptionID {
		t.Fatalf("落库 subscription_id = %d，返回的却是 %d", got.SubscriptionID, res.Request.SubscriptionID)
	}
}

func TestReviewRejectsSecondReview(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	sub, _ := f.svc.Submit(ctx, baseInput(family(7, "小明"), 8002, "片子"))
	if _, err := f.svc.Review(ctx, family(0, "admin"), sub.Request.ID, true, "", ""); err != nil {
		t.Fatalf("首次审核失败：%v", err)
	}
	_, err := f.svc.Review(ctx, family(0, "admin"), sub.Request.ID, true, "", "")
	if !errors.Is(err, ErrNotPending) {
		t.Fatalf("第二次审核应报 ErrNotPending，实际 %v", err)
	}
	// 并发点通过两次：第二次虽然报错，但 SaveSubscription 是幂等的，
	// 不应该留下第二条订阅。
	if len(f.subs.payloads) != 1 {
		t.Fatalf("SaveSubscription 被调了 %d 次，同一 entity_key 应只建一次", len(f.subs.payloads))
	}
}

func TestReviewRejectStoresReason(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	sub, _ := f.svc.Submit(ctx, baseInput(family(7, "小明"), 8003, "片子"))
	res, err := f.svc.Review(ctx, family(0, "admin"), sub.Request.ID, false, "  片名写错了  ", "")
	if err != nil {
		t.Fatalf("驳回失败：%v", err)
	}
	if res.Request.RejectReason != "片名写错了" {
		t.Fatalf("驳回理由 = %q（首尾空白没去掉）", res.Request.RejectReason)
	}
	if len(f.subs.payloads) != 0 {
		t.Fatal("驳回不该建订阅")
	}
}

// TestSubscriptionSeasonIsContextOnly 固化一条最容易踩的边界：
// 求片单点名「第 2 季」只是上下文，绝不能拿去改订阅的季数过滤 ——
// 否则第 2 季的求片会让第 1 季从此不再检查。
func TestSubscriptionSeasonIsContextOnly(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	in := baseInput(family(7, "小明"), 8004, "八尺大人")
	in.Season = 2
	sub, err := f.svc.Submit(ctx, in)
	if err != nil {
		t.Fatalf("提交失败：%v", err)
	}
	if _, err := f.svc.Review(ctx, family(0, "admin"), sub.Request.ID, true, "", ""); err != nil {
		t.Fatalf("审核失败：%v", err)
	}
	p := f.subs.last()
	season, ok := p.Metadata["season"]
	if !ok {
		t.Fatal("订阅 metadata 里没有 season 上下文")
	}
	if s, _ := season.(int); s != 2 {
		t.Fatalf("metadata.season = %v", season)
	}
	// 订阅本身必须保持「整部剧」粒度。
	if p.TMDBID != 8004 || p.MediaType != "tv" {
		t.Fatalf("订阅粒度不对：tmdb=%d type=%s", p.TMDBID, p.MediaType)
	}
	for _, r := range p.Rules {
		if strings.Contains(strings.ToLower(r.Name), "season") {
			t.Fatalf("求片建出来的订阅规则里出现了季数过滤：%q", r.Name)
		}
		if r.MustContain != nil && strings.Contains(strings.ToLower(strings.Join(r.MustContain, ",")), "s02") {
			t.Fatalf("订阅规则里写进了季数限定：%v", r.MustContain)
		}
	}
}

// ---------------------------------------------------------------- 标签

func TestTagsHangOnTheWholeShow(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	if err := f.svc.SaveTags(ctx, 9001, "tv", 7, []string{"4K", " 中文字幕 "}); err != nil {
		t.Fatalf("保存标签失败：%v", err)
	}
	// 第 1 季和第 2 季读到的是同一批标签 —— 验收 ⑦。
	for _, season := range []int{0, 1, 2} {
		tags, err := f.svc.TagsForWork(ctx, 9001, "tv", season)
		if err != nil {
			t.Fatalf("读季 %d 的标签失败：%v", season, err)
		}
		if len(tags) != 2 {
			t.Fatalf("季 %d 读到 %d 个标签：%v", season, len(tags), tags)
		}
	}
}

func TestSaveTagsReplacesInsteadOfAppends(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	if err := f.svc.SaveTags(ctx, 9002, "tv", 7, []string{"A", "B"}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SaveTags(ctx, 9002, "tv", 7, []string{"C"}); err != nil {
		t.Fatalf("覆盖保存失败：%v", err)
	}
	got, err := f.svc.TagsForWork(ctx, 9002, "tv", 0)
	if err != nil {
		t.Fatalf("回读标签失败：%v", err)
	}
	if len(got) != 1 || got[0].Tag != "C" {
		t.Fatalf("替换语义被破坏了，返回 %v", got)
	}
}

func TestTagOverLimitIsAnErrorNotTruncation(t *testing.T) {
	cfg := defaultCfg()
	cfg[settings.KeyMOMediaRequestTagMaxPerUser] = 2
	f := newFixture(t, cfg)
	err := f.svc.SaveTags(context.Background(), 9003, "tv", 7, []string{"A", "B", "C"})
	var le *LimitError
	if !errors.As(err, &le) {
		t.Fatalf("超标签数应返回 *LimitError，实际 %v", err)
	}
	if le.Kind != LimitKindTagCount {
		t.Fatalf("Kind = %q", le.Kind)
	}
	// 没写进去就是没写进去 —— 静默截断会让用户以为标签全存上了。
	tags, _ := f.svc.TagsForWork(context.Background(), 9003, "tv", 0)
	if len(tags) != 0 {
		t.Fatalf("被拒的保存却留下了 %v", tags)
	}
}

func TestNormalizeTagsDedupeCaseInsensitively(t *testing.T) {
	got := NormalizeTags([]string{"4K", " 4k ", "中文", "中文"})
	if len(got) != 2 {
		t.Fatalf("去重结果 %v", got)
	}
	if got[0] != "4K" || got[1] != "中文" {
		t.Fatalf("应保留首次出现的写法：%v", got)
	}
}

// ---------------------------------------------------------------- 规则

func TestSaveRulesIsWholeSetReplace(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	first, err := f.svc.SaveRules(ctx, []Rule{
		{Name: "给孩子", AppliesToUserID: 7, MediaType: "tv", AutoApprove: true, Enabled: true, Priority: 1},
		{Name: "给我自己", AppliesToUserID: 0, MediaType: "movie", Enabled: true, Priority: 2},
	})
	if err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	if len(first) != 2 {
		t.Fatalf("应有 2 条规则，实际 %d", len(first))
	}
	second, err := f.svc.SaveRules(ctx, []Rule{{Name: "只剩一条", AppliesToUserID: 7, MediaType: "tv", Enabled: true}})
	if err != nil {
		t.Fatalf("覆盖保存失败：%v", err)
	}
	if len(second) != 1 || second[0].Name != "只剩一条" {
		t.Fatalf("整份覆盖失败：%+v", second)
	}
	rules, _ := f.svc.Rules(ctx)
	if len(rules) != 1 {
		t.Fatalf("库里还有 %d 条规则，没删掉未保留的", len(rules))
	}
	// 逐条保存的中间态会让某个用户的求片「临时变成要审核」而他不知道。
	rulesByUser := f.svc.resolveRule(family(7, "小明"), "tv")
	if rulesByUser == nil || rulesByUser.Name != "只剩一条" {
		t.Fatalf("覆盖后规则解析错了：%+v", rulesByUser)
	}
}

func TestRuleAutoApproveSkipsReview(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	if _, err := f.svc.SaveRules(ctx, []Rule{
		{Name: "孩子不用等", AppliesToUserID: 8, MediaType: "tv", AutoApprove: true, Enabled: true, Priority: 1},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.Submit(ctx, baseInput(family(8, "小红"), 9004, "片子"))
	if err != nil {
		t.Fatalf("提交失败：%v", err)
	}
	if res.Request.Status != StatusApproved {
		t.Fatalf("命中自动通过规则就该免审，实际 %q", res.Request.Status)
	}
}

func TestDisabledRuleIsIgnored(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	if _, err := f.svc.SaveRules(ctx, []Rule{
		{Name: "关掉的", AppliesToUserID: 8, MediaType: "tv", AutoApprove: true, Enabled: false, Priority: 1},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.Submit(ctx, baseInput(family(8, "小红"), 9005, "片子"))
	if err != nil {
		t.Fatalf("提交失败：%v", err)
	}
	if res.Request.Status != StatusPending {
		t.Fatalf("禁用规则不该生效，实际状态 %q", res.Request.Status)
	}
}

// ---------------------------------------------------------------- 对账

func TestReconcilePicksUpApprovedWithoutSubscription(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	// 手工造一条「已通过但没关联订阅」：这正是 SaveSubscription 失败后
	// 管理员又配好了保存目录的情形。
	res, err := f.svc.Submit(ctx, baseInput(family(7, "小明"), 9100, "片子"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpdateReview(ctx, &Request{
		ID: res.Request.ID, Status: StatusApproved, ReviewedAt: f.now,
		ReviewerID: 0, ReviewerName: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	n, err := f.svc.Reconcile(ctx)
	if err != nil {
		t.Fatalf("对账失败：%v", err)
	}
	if n != 1 {
		t.Fatalf("对账处理了 %d 条，应为 1", n)
	}
	got, _ := f.store.Get(ctx, res.Request.ID)
	if got.SubscriptionID == 0 {
		t.Fatal("对账后仍然没有关联订阅")
	}
	// 第二轮不该重复建。
	if n, _ := f.svc.Reconcile(ctx); n != 0 {
		t.Fatalf("第二轮又处理了 %d 条，对账不幂等", n)
	}
}

func TestReconcileMarksFulfilledWhenTransferred(t *testing.T) {
	cfg := defaultCfg()
	cfg[settings.KeyMOMediaRequestRequireReview] = false
	f := newFixture(t, cfg)
	f.svc.items = fakeItems{transferred: map[uint]bool{1: true}}
	ctx := context.Background()
	res, err := f.svc.Submit(ctx, baseInput(family(7, "小明"), 9101, "片子"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Request.SubscriptionID != 1 {
		t.Fatalf("订阅号 = %d", res.Request.SubscriptionID)
	}
	if n, err := f.svc.Reconcile(ctx); err != nil || n != 1 {
		t.Fatalf("对账没把已入库的标成完成：n=%d err=%v", n, err)
	}
	got, _ := f.store.Get(ctx, res.Request.ID)
	if got.Status != StatusFulfilled {
		t.Fatalf("状态 = %q", got.Status)
	}
	// 统计也要跟着走。
	stats, err := f.svc.Stats(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range stats {
		if s.FulfilledCount == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("fulfilled_count 没记上：%+v", stats)
	}
}

func TestReconcileWithoutItemListerDoesNotMarkFulfilled(t *testing.T) {
	f := newFixture(t, nil)
	f.svc.items = nil
	ctx := context.Background()
	if _, err := f.svc.Submit(ctx, baseInput(family(7, "小明"), 9102, "片子")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Reconcile(ctx); err != nil {
		t.Fatalf("没有 ItemLister 时对账不应报错：%v", err)
	}
}

// ---------------------------------------------------------------- 边界

// TestDisabledServiceDoesNotWrite 验收 ⑧ 的存储侧：
// 求片关掉时 Submit 必须直接拒绝，且一行都不留下。
//
// 端口那一半（「根本不该监听，而不是监听了回 403」）在
// internal/mediarequest/listener.go 与 internal/app/wire_http.go 那一侧。
func TestDisabledServiceDoesNotWrite(t *testing.T) {
	cfg := defaultCfg()
	cfg[settings.KeyMOMediaRequestEnabled] = false
	f := newFixture(t, cfg)
	if f.svc.Enabled() {
		t.Fatal("配置关了就不该 Enabled")
	}
	if _, err := f.svc.Submit(context.Background(), baseInput(family(7, "小明"), 9200, "片子")); err == nil {
		t.Fatal("求片关着时不该接受提交")
	}
	list, err := f.store.ListAll(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("拒绝后却写进了 %d 行", len(list))
	}
}

// TestServiceWithoutStoreDoesNotPanic 固化「装配顺序出问题时能看清是什么问题」：
// 没有存储句柄时返回可读的错误，而不是 panic 打穿 HTTP 服务器。
func TestServiceWithoutStoreDoesNotPanic(t *testing.T) {
	s := NewService(Params{Store: NewStore(nil, nil), Cfg: defaultCfg()})
	if _, err := s.ListPending(context.Background(), 10); err == nil {
		t.Fatal("没接存储时应返回错误而不是 panic")
	}
}

func TestWrapStoreErrMapsMissingTable(t *testing.T) {
	got := wrapStoreErr(errors.New("no such table: media_requests"))
	if !strings.Contains(got.Error(), "数据库迁移") {
		t.Fatalf("未迁移时的提示 = %q", got.Error())
	}
	got = wrapStoreErr(errors.New("UNIQUE constraint failed: idx_media_requests_pending_identity"))
	if !errors.Is(got, ErrAlreadyPending) {
		t.Fatalf("唯一索引冲突应映射成 ErrAlreadyPending，实际 %v", got)
	}
}

func TestClampPortalPort(t *testing.T) {
	cases := map[string]struct {
		in   int
		want int
	}{
		"正常":   {7812, 7812},
		"太小":   {10, 0},
		"太大":   {70000, 0},
		"零值":   {0, 0},
		"下界之上": {1024, 1024},
	}
	for name, c := range cases {
		if got := ClampPortalPort(c.in); got != c.want {
			t.Errorf("%s：ClampPortalPort(%d) = %d，期望 %d", name, c.in, got, c.want)
		}
	}
}
