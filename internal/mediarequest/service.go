package mediarequest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"litepan/internal/discover/discovery"
	"litepan/internal/settings"
)

// Actor 一个发起求片或执行审核的身份。
//
// UserID = 0 且 IsSuper = true 表示超管：T08 把超管虚拟化了（他在 rbac_users
// 里没有行），所以全仓都用 0 这个哨兵，这里沿用同一套。
type Actor struct {
	UserID   int64
	Username string
	IsSuper  bool
}

// ErrNotFound 求片单不存在。
var ErrNotFound = errors.New("求片单不存在")

// ErrNotPending 这条求片单已经不处于待审状态（已被别人审过）。
var ErrNotPending = errors.New("这条求片单已被处理过")

// ErrNotApproved 这条求片单不是「已通过」状态（不能标入库）。
var ErrNotApproved = errors.New("只有已通过的求片单能标记入库")

// ErrAlreadyPending 同一部作品已经有待审单。
var ErrAlreadyPending = errors.New("这部作品已经有一条待审的求片单了")

// ErrAlreadyLinked 这条求片单已经关联过订阅了（并发对账时可能出现）。
var ErrAlreadyLinked = errors.New("这条求片单已经关联过订阅")

// InvalidError 用户输入不合法。
//
// 单独一种类型而不是随手 errors.New，原因在接口层：那里要把错误分成
// 「用户改一下就能重试」（400）与「系统出问题了」（500）。
// 分不出来的话，「片名不能为空」会被报成 500 —— 手机上弹出「服务内部错误」，
// 而真正的毛病是他刚才把片名删空了。
type InvalidError struct{ Message string }

func (e *InvalidError) Error() string { return e.Message }

// Invalid 造一个「用户输入不合法」的错误。
func Invalid(format string, a ...any) error {
	return &InvalidError{Message: fmt.Sprintf(format, a...)}
}

// IsInvalid 判断错误是不是用户输入问题。
func IsInvalid(err error) bool {
	var ie *InvalidError
	return errors.As(err, &ie)
}

// SubscriptionSaver 建资源订阅。
//
// 只依赖这一个方法而不是整个 *discovery.Service，是为了让 Service 能在测试里
// 用假实现验证「审核通过到底有没有把订阅建出来」—— 而不启动整个订阅流水线。
// 生产实现就是 *discovery.Service 本身。
type SubscriptionSaver interface {
	SaveSubscription(payload *discovery.SubscriptionUpsertPayload) (*discovery.DiscoverySubscription, string, error)
}

// ItemLister 读订阅条目，用于对账「东西到底入库了没」。
type ItemLister interface {
	ListSubscriptionItems(subscriptionID uint, status string, limit int) ([]discovery.DiscoverySubscriptionItem, error)
}

// Searcher 求片站搜索用。只依赖 SearchMedia 一个方法，理由同 SubscriptionSaver。
type Searcher interface {
	SearchMedia(ctx context.Context, q, mediaType string, page int, force bool) (*discovery.ActorsPage, error)
}

// Logger 求片服务要打的日志，接口化是为了测试里能收集。
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

type nopLogger struct{}

func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

// Service 求片中心的业务逻辑。
type Service struct {
	store  *Store
	cfg    Settings
	subs   SubscriptionSaver
	items  ItemLister
	search Searcher
	log    Logger
	now    func() time.Time
}

// Settings 求片要读的配置项。settings.Service 直接满足它。
//
// 刻意不给 context 也不给 fallback：settings.Service 内部已经按 registry 的
// 默认值回落过一遍（见 registry.go 里那六个 KeyMOMediaRequest* 常量的注释），
// 这里再兜一层默认值就成了第二个真相来源。
type Settings interface {
	String(key string) string
	Bool(key string) bool
	Int(key string) int
}

// Params 组装 Service 需要的依赖。
type Params struct {
	Store   *Store
	Cfg     Settings
	Subs    SubscriptionSaver
	Items   ItemLister
	Search  Searcher
	Log     Logger
	NowFunc func() time.Time
}

// NewService 按 Params 构造。Params 里为 nil 的依赖会让对应能力不可用，
// 调用时返回明确错误而不是 panic —— 装配顺序出错时要能一眼看出是什么问题。
func NewService(p Params) *Service {
	log := p.Log
	if log == nil {
		log = nopLogger{}
	}
	now := p.NowFunc
	if now == nil {
		now = time.Now
	}
	return &Service{
		store:  p.Store,
		cfg:    p.Cfg,
		subs:   p.Subs,
		items:  p.Items,
		search: p.Search,
		log:    log,
		now:    now,
	}
}

// SetClock 换掉时间源（测试用）。
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// Enabled 求片中心是否启用。
func (s *Service) Enabled() bool {
	return s != nil && s.cfg != nil && s.cfg.Bool(settings.KeyMOMediaRequestEnabled)
}

// RequireReview 是否需要过审。
//
// 「审核关」有三个来源，任一为真就免审：全局开关、命中的规则 auto_approve、
// 提交者自己是超管。最后一条是必须的：否则管理员自己想求个片还得等别人审，
// 而且「审核不可用时只有超管能建订阅」（任务书原话）也就没法执行了。
func (s *Service) RequireReview(a Actor) bool {
	if !s.cfg.Bool(settings.KeyMOMediaRequestRequireReview) {
		return false
	}
	if a.IsSuper {
		return false
	}
	rule := s.resolveRule(a, "")
	if rule != nil && rule.AutoApprove {
		return false
	}
	return true
}

// SubmitInput 一次求片提交。
type SubmitInput struct {
	Requester     Actor
	TMDBID        int64
	Title         string
	OriginalTitle string
	MediaType     string
	Season        int
	Year          int
	PosterURL     string
	Notes         string
	// Tags 入库标签，可为空。传进来的字符串会被清洗（去空白去重）。
	Tags []string
}

// SubmitResult 提交结果。
type SubmitResult struct {
	Request *Request
	// Warning 建订阅时的告警（比如没配保存目录）。
	//
	// 与错误分开：SaveSubscription 对「没配目标目录」只给 warning 不给 error
	// （这是它的既有语义，见 subscriptions.go）。所以「提交成功但可能转存不了」
	// 必须显式传给家人，而不是让他们等三天才发现。
	Warning string
}

// Submit 提交一条求片。
//
// 顺序刻意是「先查上限 → 再写库 → 最后建订阅」：
//   - 先查上限是为了让「今天超了」这种最常见的情况不留下垃圾行；
//   - 建订阅放在最后，是为了让「写不进库」和「建不成订阅」两件事的先后关系
//     与界面上显示的顺序一致（先出现在「我的求片」里，再变成已生效）。
func (s *Service) Submit(ctx context.Context, in SubmitInput) (*SubmitResult, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("求片服务未初始化")
	}
	// 求片关着就不接受新提交。求片站那一侧本来就不监听（见 listener.go），
	// 这一层是为了堵住管理台那条路：功能关着却在后台能点「通过」，
	// 会凭空建出一堆没人要的订阅。
	if !s.Enabled() {
		return nil, Invalid("求片功能未启用")
	}
	req, err := s.prepare(ctx, in)
	if err != nil {
		return nil, err
	}

	now := s.now().UTC()
	req.CreatedAt = now
	if s.RequireReview(in.Requester) {
		req.Status = StatusPending
	} else {
		req.Status = StatusApproved
		req.ReviewedAt = now
		// 免审时审核人就是提交人自己。写 0 而不是留 NULL，理由是
		// 「这条已经生效了」这件事在管理台列表里要一眼看得出来。
		req.ReviewerID = in.Requester.UserID
		req.ReviewerName = in.Requester.Username
	}

	id, err := s.store.Insert(ctx, req)
	if err != nil {
		return nil, wrapStoreErr(err)
	}
	req.ID = id

	if err := s.store.bump(ctx, localDay(s.now()), req.RequesterID, req.MediaType, "submitted_count", 1); err != nil {
		// 统计写不进去不该让求片失败 —— 求片是用户主动发起的动作，
		// 统计只是副产品。记日志继续。
		s.log.Warn("求片统计写入失败", "err", err, "request_id", req.ID)
	}

	if len(in.Tags) > 0 {
		if err := s.saveTags(ctx, req, in.Tags); err != nil {
			// 标签入库失败同样不该让求片失败：片还没求到，标签还有机会补。
			s.log.Warn("入库标签写入失败", "err", err, "request_id", req.ID)
		}
	}

	if req.Status == StatusApproved {
		sub, warn, err := s.attachSubscription(ctx, req)
		if err != nil {
			return nil, err
		}
		if sub != nil && sub.ID > 0 {
			req.SubscriptionID = int64(sub.ID)
			// ⚠️ 这一步不能省。漏掉它，后果不是「显示少了列」：
			// ① 对账器的第一步只把 subscription_id 为空的已通过单当成
			//    「还没建订阅」，于是它每 15 分钟都会重跑一遍 SaveSubscription；
			// ② 第二步只查 subscription_id 非空的单，所以这条求片
			//    **永远**不会被标成「已转存」，家人看到的还是「一直卡在已通过」；
			// ③ ReviewWarning 会一直提示「订阅还没建起来」。
			// 写不进去只记日志：对账器会兜底，而 SaveSubscription 按 entity_key
			// UPSERT，重复调用是安全的。
			if err := s.store.AttachSubscriptionID(ctx, req.ID, req.SubscriptionID); err != nil &&
				!errors.Is(err, ErrAlreadyLinked) {
				s.log.Warn("回写求片订阅关联失败，将由对账重试", "err", err, "request_id", req.ID)
			}
		}
		return &SubmitResult{Request: req, Warning: warn}, nil
	}
	return &SubmitResult{Request: req}, nil
}

// prepare 校验入参并查上限，产出还没落库的 Request。
func (s *Service) prepare(ctx context.Context, in SubmitInput) (*Request, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, Invalid("片名不能为空")
	}
	if in.TMDBID <= 0 {
		return nil, Invalid("缺少作品 ID，无法确认是哪部片")
	}
	mediaType := normalizeMediaType(in.MediaType)
	if mediaType == "" {
		return nil, Invalid("只支持电影和电视剧的求片")
	}
	season := in.Season
	if mediaType == "movie" {
		// 电影没有季。传了也不报错，静默归零 —— 手机端把剧和电影混在
		// 一个选择列表里时很容易带错值。
		season = 0
	}
	if season < 0 || season > 200 {
		return nil, Invalid("季数需要在 0 到 200 之间")
	}

	rule, err := s.resolveRuleErr(ctx, in.Requester, mediaType)
	if err != nil {
		return nil, err
	}
	needReview := s.RequireReview(in.Requester)

	// 每天上限。起点取**本地**零点：storage 里的时间戳是 UTC，
	// 用 UTC 零点算的话，晚上 8 点之后（东八区）提交就已经算「明天」了。
	dailyLimit := s.dailyLimit(rule)
	if dailyLimit > 0 {
		dayStart := startOfLocalDay(s.now())
		n, err := s.store.CountSince(ctx, in.Requester.UserID, dayStart)
		if err != nil {
			return nil, wrapStoreErr(err)
		}
		if n >= dailyLimit {
			return nil, &LimitError{Kind: LimitKindDaily, Limit: dailyLimit, Actual: n + 1}
		}
	}

	// 同时待审上限。只由规则表控制（没有全局配置项）：
	// 任务书点名要的上限只有「每人每天」这一条，待审上限是防「一次提 50 条
	// 淹掉审核队列」用的，各家口味差太多，交给规则按需配置，
	// 没配就是不设限 —— 默认 3 的话，管理员哪天想一次补齐一整部剧的 8 季会被挡住。
	if rule != nil && rule.PendingLimit > 0 && needReview {
		n, err := s.store.CountPendingByRequester(ctx, in.Requester.UserID)
		if err != nil {
			return nil, wrapStoreErr(err)
		}
		if n >= rule.PendingLimit {
			return nil, &LimitError{Kind: LimitKindPending, Limit: rule.PendingLimit, Actual: n + 1}
		}
	}

	// 待审期间的同一部重复提交。迁移里的 partial unique index 是最后一道防线，
	// 这里先查是为了给出「这部已经有人在求了」这种能看懂的话，
	// 而不是把 SQLITE_CONSTRAINT_UNIQUE 原文甩给手机用户。
	if needReview {
		dup, err := s.store.PendingByIdentity(ctx, in.TMDBID, mediaType)
		if err != nil {
			return nil, wrapStoreErr(err)
		}
		if dup != nil {
			return nil, fmt.Errorf("%w（提交人：%s）", ErrAlreadyPending, displayName(dup.RequesterName, dup.RequesterID))
		}
	}

	tags, err := s.cleanTags(in.Tags)
	if err != nil {
		return nil, err
	}

	return &Request{
		RequesterID:   in.Requester.UserID,
		RequesterName: displayName(in.Requester.Username, in.Requester.UserID),
		TMDBID:        in.TMDBID,
		Title:         title,
		OriginalTitle: strings.TrimSpace(in.OriginalTitle),
		MediaType:     mediaType,
		Season:        season,
		Year:          in.Year,
		PosterURL:     strings.TrimSpace(in.PosterURL),
		Status:        StatusPending,
		Notes:         strings.TrimSpace(in.Notes),
		Tags:          tags,
	}, nil
}

// saveTags 写入标签（替换语义，见 Store.UpsertTags）。
func (s *Service) saveTags(ctx context.Context, req *Request, tags []string) error {
	clean, err := s.cleanTags(tags)
	if err != nil {
		return err
	}
	return s.store.UpsertTags(ctx, req.TMDBID, req.MediaType, req.RequesterID, clean)
}

// cleanTags 按当前上限清洗并校验标签。
func (s *Service) cleanTags(tags []string) ([]string, error) {
	clean := NormalizeTags(tags)
	if err := ValidateTags(clean, s.tagMaxPerUser(), s.tagMaxLength()); err != nil {
		return nil, err
	}
	return clean, nil
}

func (s *Service) tagMaxPerUser() int {
	if s.cfg == nil {
		return DefaultTagMaxPerUser
	}
	return s.cfg.Int(settings.KeyMOMediaRequestTagMaxPerUser)
}

func (s *Service) tagMaxLength() int {
	if s.cfg == nil {
		return DefaultTagMaxLength
	}
	return s.cfg.Int(settings.KeyMOMediaRequestTagMaxLength)
}

func (s *Service) dailyLimit(rule *Rule) int {
	if rule != nil && rule.DailyLimit > 0 {
		return rule.DailyLimit
	}
	if s.cfg == nil {
		return 0
	}
	return s.cfg.Int(settings.KeyMOMediaRequestDailyLimit)
}

// ReviewResult 审核结果。
type ReviewResult struct {
	Request *Request
	// Warning 审核通过但订阅侧有告警时非空（例如没配保存目录）。
	//
	// 之所以要单独带出来：这时求片单**已经是**已通过状态了，
	// 界面上会显示「已生效」，如果不把告警原样传给管理员，
	// 他会以为一切正常，然后三天后疑惑「为什么什么都没转存」。
	Warning string
}

// Review 审核一条求片。
//
// 顺序是「先建订阅、后落审核结论」，反过来的话会出现「审核通过但没有订阅」
// 这种最难查的状态（界面上明明显示已通过，却什么都没有发生）。
// 并发下两个人同时点「通过」时，后一个会在 UpdateReview 上拿到 ErrNotPending；
// 而它之前建的那次 SaveSubscription 是幂等的（同 entity_key 走更新分支），
// 所以不会留下第二条订阅。宁可报错让人重试，也不要静默覆盖同事的结论。
func (s *Service) Review(ctx context.Context, reviewer Actor, id int64, approve bool, reason, note string) (*ReviewResult, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("求片服务未初始化")
	}
	if !s.Enabled() {
		return nil, Invalid("求片功能未启用")
	}
	req, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, wrapStoreErr(err)
	}
	if req.Status != StatusPending {
		return nil, ErrNotPending
	}

	now := s.now().UTC()
	req.ReviewedAt = now
	req.ReviewerID = reviewer.UserID
	req.ReviewerName = reviewer.Username

	warning := ""
	if approve {
		sub, warn, err := s.attachSubscription(ctx, req)
		if err != nil {
			return nil, err
		}
		if sub != nil {
			req.SubscriptionID = int64(sub.ID)
		}
		warning = warn
		req.Status = StatusApproved
		req.RejectReason = ""
	} else {
		req.Status = StatusRejected
		req.RejectReason = strings.TrimSpace(reason)
	}
	req.ReviewedNote = strings.TrimSpace(note)

	if err := s.store.UpdateReview(ctx, req); err != nil {
		return nil, wrapStoreErr(err)
	}

	column := "approved_count"
	if !approve {
		column = "rejected_count"
	}
	if err := s.store.bump(ctx, localDay(s.now()), req.RequesterID, req.MediaType, column, 1); err != nil {
		s.log.Warn("求片统计写入失败", "err", err, "request_id", req.ID)
	}
	return &ReviewResult{Request: req, Warning: warning}, nil
}

// attachSubscription 为一条求片建资源订阅。
//
// 走的是 discovery.SaveSubscription —— 与管理台手工加订阅是**同一个入口**，
// 所以 T03 身份校验 / T04 转存账本 / T05 多源搜索全都自动生效。
// 绝不能在这里另写一套「直接插一行 discovery_subscriptions」的逻辑：
// 那条路绕过身份校验，会把「同一部剧重复入库」重新引进来。
func (s *Service) attachSubscription(ctx context.Context, req *Request) (*discovery.DiscoverySubscription, string, error) {
	if s.subs == nil {
		return nil, "", errors.New("订阅服务未接入，求片无法建立订阅")
	}
	if req.SubscriptionID > 0 {
		// 已经建过：对账重试时走这里，直接返回「已生效」。
		return &discovery.DiscoverySubscription{ID: uint(req.SubscriptionID)}, "", nil
	}

	payload := &discovery.SubscriptionUpsertPayload{
		Source:        "request",
		EntityType:    req.MediaType,
		TMDBID:        req.TMDBID,
		MediaType:     req.MediaType,
		Title:         req.Title,
		OriginalTitle: req.OriginalTitle,
		Poster:        req.PosterURL,
		Metadata: map[string]any{
			"request_id":     req.ID,
			"requester_id":   req.RequesterID,
			"requester_name": req.RequesterName,
			// season 只作为「这条求片点名了第几季」的上下文存下来。
			// 订阅本身是整部剧粒度的（entity_key=tmdb:tv:<id>），
			// 所以这里**不能**拿它去改订阅的季数过滤 ——
			// 那样第 2 季的求片会让第 1 季从此不再检查。
			"season": req.Season,
			"tags":   req.Tags,
		},
	}
	sub, warning, err := s.subs.SaveSubscription(payload)
	if err != nil {
		return nil, "", fmt.Errorf("建立资源订阅失败：%w", err)
	}
	if sub == nil {
		return nil, "", errors.New("建立资源订阅失败：订阅服务没有返回记录")
	}
	return sub, warning, nil
}

// Reconcile 对账：把「审核通过但订阅没建起来」和「东西已经入库了」这两件事补上。
//
// 这是**对账**不是驱动：订阅照常按自己的周期跑，多一行求片不会让它多跑一次。
// 之所以需要它，是因为 attachSubscription 失败时我们**故意**没有把求片标成已通过
// —— 但那之后如果管理员配好了保存目录，总得有人把它捡起来。
//
// 返回本次处理掉的条数，供日志和测试断言。
func (s *Service) Reconcile(ctx context.Context) (int, error) {
	if s == nil || s.store == nil {
		return 0, nil
	}
	handled := 0

	orphans, err := s.store.ListApprovedWithoutSubscription(ctx, 100)
	if err != nil {
		return handled, err
	}
	for i := range orphans {
		req := &orphans[i]
		sub, _, err := s.attachSubscription(ctx, req)
		if err != nil {
			s.log.Warn("求片补建订阅失败，将在下轮重试", "err", err, "request_id", req.ID)
			continue
		}
		if err := s.store.AttachSubscriptionID(ctx, req.ID, int64(sub.ID)); err != nil {
			if !errors.Is(err, ErrAlreadyLinked) {
				s.log.Warn("回写求片订阅关联失败", "err", err, "request_id", req.ID)
				continue
			}
		}
		handled++
	}

	if s.items == nil {
		return handled, nil
	}
	approved, err := s.store.ListApprovedWithSubscription(ctx, 200)
	if err != nil {
		return handled, err
	}
	for i := range approved {
		req := &approved[i]
		items, err := s.items.ListSubscriptionItems(uint(req.SubscriptionID), "transferred", 1)
		if err != nil || len(items) == 0 {
			continue
		}
		if err := s.store.MarkFulfilled(ctx, req.ID, req.SubscriptionID); err != nil {
			if errors.Is(err, ErrNotApproved) {
				continue
			}
			s.log.Warn("标记求片入库失败", "err", err, "request_id", req.ID)
			continue
		}
		if err := s.store.bump(ctx, localDay(s.now()), req.RequesterID, req.MediaType, "fulfilled_count", 1); err != nil {
			s.log.Warn("求片统计写入失败", "err", err, "request_id", req.ID)
		}
		handled++
	}
	return handled, nil
}

// ---------------------------------------------------------------------------
// 查询
// ---------------------------------------------------------------------------

// Get 取一条求片单。
func (s *Service) Get(ctx context.Context, id int64) (*Request, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("求片服务未初始化")
	}
	r, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, wrapStoreErr(err)
	}
	return r, nil
}

// ListPending 取待审队列。
func (s *Service) ListPending(ctx context.Context, limit int) ([]Request, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("求片服务未初始化")
	}
	return s.store.ListPending(ctx, limit)
}

// ListByStatus 按状态取求片单（管理台的历史列表用）。
//
// status 为空时取全部。**只允许四种已知状态**，不把用户传来的串直接拼进 SQL
// —— 就算它来自自己的查询参数也一样。
func (s *Service) ListByStatus(ctx context.Context, status string, limit int) ([]Request, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("求片服务未初始化")
	}
	switch Status(strings.TrimSpace(status)) {
	case "":
		return s.store.ListAll(ctx, limit)
	case StatusPending, StatusApproved, StatusRejected, StatusFulfilled:
		return s.store.ListByStatus(ctx, Status(strings.TrimSpace(status)), limit)
	default:
		return nil, Invalid("不支持的状态：%s", status)
	}
}

// UsedToday 今天（本地零点起）某人已提交几条。
func (s *Service) UsedToday(ctx context.Context, a Actor) (int, error) {
	if s == nil || s.store == nil {
		return 0, errors.New("求片服务未初始化")
	}
	return s.store.CountSince(ctx, a.UserID, startOfLocalDay(s.now()).UTC())
}

// DailyLimit 当前生效的每人每天求片上限；0 表示不限。
//
// 走 resolveRule 而不是直接读设置，是为了让「按人/按类型调上限」成为可能 ——
// 规则表存在就是为了这个。
func (s *Service) DailyLimit() int {
	return s.dailyLimit(s.resolveRule(Actor{}, ""))
}

// TagLimits 返回标签的两个上限（个数、单条字数）。0 表示不限。
func (s *Service) TagLimits() (perUser, length int) {
	return s.tagMaxPerUser(), s.tagMaxLength()
}

// ReviewWarning 取「已通过但订阅还没建起来」的告警文案。
//
// 与 ReviewResult.Warning 分开是有意的：那个是审核当下的快照，
// 这个是**随时**可查的现状 —— 求片单通过之后管理员可能过了几天才来补订阅，
// 详情页要能一眼看出「这条还没生效」。
func (s *Service) ReviewWarning(ctx context.Context, id int64) (string, error) {
	req, err := s.store.Get(ctx, id)
	if err != nil {
		return "", wrapStoreErr(err)
	}
	if req.Status != StatusApproved || req.SubscriptionID > 0 {
		return "", nil
	}
	return "这条已通过，但订阅还没建起来（通常是没配保存目录或目标网盘）。求片中心会每 15 分钟自动重试一次。", nil
}

// SaveRules 整份覆盖规则表。
//
// 与「先删再逐条建」的做法相比，这样做的理由是原子性：
// 逐条保存的中间态会让某个用户的求片**临时**变成要审核的，
// 而对家人来说「我刚才那条怎么突然要审核了」是个很难解释的现象。
func (s *Service) SaveRules(ctx context.Context, rules []Rule) ([]Rule, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("求片服务未初始化")
	}
	kept := make(map[string]struct{}, len(rules))
	out := make([]Rule, 0, len(rules))
	seen := map[string]bool{}

	// 先读现有规则：整份覆盖时要用它把「同钥匙」的旧行认出来（见下面的
	// ID 继承）。不读的话，客户端只要没把 id 原样带回来（换了台电脑、
	// 页面刷新过、或者别的调用方直接调 SaveRules），SaveRule 就会 INSERT
	// 出一条**一模一样**的新行，而删除未保留行时按钥匙算 kept，
	// 于是这两行都算「保留」——规则表就这么悄悄翻倍了。
	existing, err := s.store.ListRules(ctx, false)
	if err != nil {
		return nil, wrapStoreErr(err)
	}
	existingID := make(map[string]int64, len(existing))
	for i := range existing {
		existingID[ruleKey(existing[i])] = existing[i].ID
	}

	for i := range rules {
		rule := rules[i]
		if rule.DailyLimit < 0 || rule.PendingLimit < 0 {
			return nil, Invalid("上限不能为负")
		}
		if rule.MediaType != "" {
			mt := normalizeMediaType(rule.MediaType)
			if mt == "" {
				return nil, Invalid("规则类型只支持 movie / tv，或留空表示不限")
			}
			rule.MediaType = mt
		}
		key := ruleKey(rule)
		if seen[key] {
			// 同一把钥匙只能有一条规则；重复提交时后者覆盖前者。
			// 不报错是因为整份覆盖的语义下「重复」通常是误操作，
			// 而报错会让界面必须做去重才能保存。
			for j := range out {
				if ruleKey(out[j]) == key {
					out[j] = rule
				}
			}
			continue
		}
		seen[key] = true
		if rule.ID == 0 {
			rule.ID = existingID[key]
		}
		id, err := s.store.SaveRule(ctx, &rule)
		if err != nil {
			return nil, wrapStoreErr(err)
		}
		rule.ID = id
		kept[ruleKey(rule)] = struct{}{}
		out = append(out, rule)
	}
	for i := range existing {
		if _, ok := kept[ruleKey(existing[i])]; ok {
			continue
		}
		if err := s.store.DeleteRule(ctx, existing[i].ID); err != nil && !errors.Is(err, ErrNotFound) {
			return nil, wrapStoreErr(err)
		}
	}
	return out, nil
}

// ruleKey 规则的唯一钥匙：这条规则管谁 + 管什么。
//
// AppliesToUserID=0 表示「所有人」，所以它作为钥匙的一部分是安全的：
// 「给张三放宽上限」和「所有人放宽」本来就是两条不同的规则。
func ruleKey(r Rule) string {
	return strconv.FormatInt(r.AppliesToUserID, 10) + "|" + r.MediaType
}

// ListMine 取某人自己提过的求片。
//
// 超管（UserID=0）在这里看到的是「我自己提的那些」，不是全部 —— 管理台另有
// ListPending 给审核者。两者混在一起会让超管的「我的求片」永远是全量列表。
func (s *Service) ListMine(ctx context.Context, requesterID int64, limit int) ([]Request, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("求片服务未初始化")
	}
	return s.store.ListMine(ctx, requesterID, limit)
}

// TagsForWork 取某作品当前的入库标签。
//
// ⚠️ **season 参数是故意不用的。**
//
// 它存在的唯一理由是让调用方（洗版/入库那条链上按季工作的代码）不必自己判断
// 「这是不是剧」，于是不会有人顺手写出「按季取标签」的分支。
// 标签刻意挂在整部剧上（migrations/0041_media_request.sql 里有完整论证），
// 所以第 1 季和第 3 季入库时读到的是**同一份**标签。
// 传 season 进来只会被忽略 —— 这一点由 TestTagsForWorkIgnoresSeason 钉住。
func (s *Service) TagsForWork(ctx context.Context, tmdbID int64, mediaType string, season int) ([]Tag, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("求片服务未初始化")
	}
	_ = season
	return s.store.ListTags(ctx, tmdbID, normalizeMediaType(mediaType))
}

// TagsOfUser 取某个人对某作品的标签（求片站回显用户已选的那几个）。
func (s *Service) TagsOfUser(ctx context.Context, tmdbID int64, mediaType string, requesterID int64) ([]string, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("求片服务未初始化")
	}
	return s.store.ListTagsOfUser(ctx, tmdbID, normalizeMediaType(mediaType), requesterID)
}

// SaveTags 替换某个人对某作品的标签（求片站的「管理我的标签」入口）。
func (s *Service) SaveTags(ctx context.Context, tmdbID int64, mediaType string, requesterID int64, tags []string) error {
	if s == nil || s.store == nil {
		return errors.New("求片服务未初始化")
	}
	clean, err := s.cleanTags(tags)
	if err != nil {
		return err
	}
	return s.store.UpsertTags(ctx, tmdbID, normalizeMediaType(mediaType), requesterID, clean)
}

// Search 求片站搜索。
//
// 只放开 movie / tv：person（人物）在求片语境下没有「求某个人」这个动作，
// 放进去只会让家人搜到一堆人名然后不知道该点什么。
func (s *Service) Search(ctx context.Context, q, mediaType string, page int) (*discovery.ActorsPage, error) {
	if s == nil || s.search == nil {
		return nil, errors.New("搜索服务未接入")
	}
	mt := normalizeMediaType(mediaType)
	if mt == "" {
		return nil, Invalid("搜索只支持电影和电视剧")
	}
	strings.TrimSpace(q)
	if page <= 0 {
		page = 1
	}
	return s.search.SearchMedia(ctx, strings.TrimSpace(q), mt, page, false)
}

// Rules 列出全部规则（含未启用的）。
func (s *Service) Rules(ctx context.Context) ([]Rule, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("求片服务未初始化")
	}
	return s.store.ListRules(ctx, false)
}

// SaveRule 新建或更新一条规则。
func (s *Service) SaveRule(ctx context.Context, r *Rule) (int64, error) {
	if s == nil || s.store == nil {
		return 0, errors.New("求片服务未初始化")
	}
	if r.DailyLimit < 0 || r.PendingLimit < 0 {
		return 0, Invalid("上限不能为负")
	}
	if r.MediaType != "" {
		mt := normalizeMediaType(r.MediaType)
		if mt == "" {
			return 0, Invalid("规则类型只支持 movie / tv，或留空表示不限")
		}
		r.MediaType = mt
	}
	return s.store.SaveRule(ctx, r)
}

// DeleteRule 删一条规则。
func (s *Service) DeleteRule(ctx context.Context, id int64) error {
	if s == nil || s.store == nil {
		return errors.New("求片服务未初始化")
	}
	return s.store.DeleteRule(ctx, id)
}

// Stats 取最近 days 天的求片统计。
func (s *Service) Stats(ctx context.Context, days int) ([]AnalyticsRow, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("求片服务未初始化")
	}
	if days <= 0 || days > 400 {
		days = 30
	}
	now := s.now().Local()
	to := localDay(now)
	from := localDay(now.AddDate(0, 0, -(days - 1)))
	return s.store.ListAnalytics(ctx, from, to, 1000)
}

// resolveRuleErr 取命中的规则，数据库出错时返回错误。
// resolveRuleErr 找命中的规则。mediaType 传空串表示「还不知道是哪一类」。
//
// ⚠️ 空串是**通配**，不是「只匹配 media_type 为空的规则」。
// RequireReview 与 DailyLimit 都在不知道类型的地方调用它（/api/me 里
// 「这条要不要审核」总得先给个说法，那时用户还没选片），如果把空串当成
// 「只匹配不限类型的规则」，那么按 movie/tv 分别配的规则就永远匹配不上 ——
// 界面上配了「电影免审」，点下去照样要等审核，而且没有任何报错。
// 真正决定要不要审核的地方（prepare/Submit）拿到的是确切的类型。
func (s *Service) resolveRuleErr(ctx context.Context, a Actor, mediaType string) (*Rule, error) {
	if s.store == nil {
		return nil, nil
	}
	rules, err := s.store.ListRules(ctx, true)
	if err != nil {
		return nil, wrapStoreErr(err)
	}
	for i := range rules {
		if mediaType != "" && rules[i].MediaType != "" && rules[i].MediaType != mediaType {
			continue
		}
		if rules[i].AppliesToUserID > 0 && rules[i].AppliesToUserID != a.UserID {
			continue
		}
		return &rules[i], nil
	}
	return nil, nil
}

// resolveRule 取命中的规则，错误只记日志。
func (s *Service) resolveRule(a Actor, mediaType string) *Rule {
	rule, err := s.resolveRuleErr(context.Background(), a, mediaType)
	if err != nil {
		s.log.Warn("读取求片规则失败，按默认上限处理", "err", err)
		return nil
	}
	return rule
}

// ---------------------------------------------------------------------------

// wrapStoreErr 把底层错误翻译成调用方看得懂的话。
//
// 特别是「同一部已有待审单」：迁移里的 partial unique index 会以
// sqlite3.Error 的形式冒上来，直接返回的话手机上是
// 「UNIQUE constraint failed: media_requests.tmdb_id, media_requests.media_type」——
// 家人看到这句话只会以为程序坏了。
func wrapStoreErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "idx_media_requests_pending_identity") ||
		strings.Contains(msg, "UNIQUE constraint failed: media_requests.tmdb_id") {
		return ErrAlreadyPending
	}
	if strings.Contains(msg, "no such table: media_requests") ||
		strings.Contains(msg, "no such table: media_request_tags") {
		return errors.New("求片功能未完成数据库迁移，请升级到最新版本")
	}
	return err
}

func normalizeMediaType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "movie", "电影":
		return "movie"
	case "tv", "series", "tv_series", "电视剧", "剧集":
		return "tv"
	}
	return ""
}

// displayName 姓名回填。
func displayName(name string, userID int64) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	if userID == 0 {
		return "管理员"
	}
	return fmt.Sprintf("用户#%d", userID)
}

// localDay 本地日期 YYYY-MM-DD。
//
// ⚠️ 用本地时区而不是 UTC：晚上 23 点求片的人，他的「今天」按 UTC 算会落到明天，
// 于是「今天还能求几条」这个数会在半夜自己跳一次。
func localDay(t time.Time) string {
	return t.Local().Format("2006-01-02")
}

// startOfLocalDay 本地零点。
func startOfLocalDay(t time.Time) time.Time {
	l := t.Local()
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, l.Location())
}

// slogLogger 把 slog 适配成 Logger。
type slogLogger struct{ l *slog.Logger }

func (s slogLogger) Info(msg string, args ...any)  { s.l.Info(msg, args...) }
func (s slogLogger) Warn(msg string, args ...any)  { s.l.Warn(msg, args...) }
func (s slogLogger) Error(msg string, args ...any) { s.l.Error(msg, args...) }

// NewSlogLogger 构造 slog 适配器。
func NewSlogLogger(l *slog.Logger) Logger {
	if l == nil {
		return nopLogger{}
	}
	return slogLogger{l: l}
}

// sortRulesByPriority 供测试与展示复用：优先级高的在前。
func sortRulesByPriority(rules []Rule) {
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Priority != rules[j].Priority {
			return rules[i].Priority > rules[j].Priority
		}
		return rules[i].ID > rules[j].ID
	})
}
