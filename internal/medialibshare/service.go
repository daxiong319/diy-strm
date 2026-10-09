package medialibshare

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"

	"litepan/internal/settings"
	"litepan/pkg/security"
)

// 对外的错误。分开的目的是让 API 层能给出不同的状态码与文案：
// 用户看到「设备数超了」和「令牌无效」必须是两种提示，否则他只会反复刷新。
var (
	// ErrDisabled 功能总开关关闭。
	ErrDisabled = errors.New("免登录分享未启用")
	// ErrPasswordRequired 这条分享设了访问口令。
	ErrPasswordRequired = errors.New("这条分享需要访问口令")
	// ErrBadPassword 口令不对。
	ErrBadPassword = errors.New("访问口令不正确")
	// ErrBadToken 令牌缺失、错误或已过期。
	ErrBadToken = errors.New("分享令牌无效或已过期，请重新打开分享链接")
	// ErrDeviceLimit 设备数超限（验收第 6 条）。
	ErrDeviceLimit = errors.New("这条分享的设备数已用满，无法在新设备上打开")
	// ErrInvalid 输入不合法（用户能改正的那种）。
	ErrInvalid = errors.New("参数不合法")
)

// ExpireDayOptions 是任务书第 5 条要求的五种有效期。
//
// 取值域照搬 参考实现（.so 符号 + 前端 chunk 得到，高置信但未实测）。
// 0 = 永久分享，expires_at 落 NULL —— 这是唯一能在库里表示「没有期限」的写法。
var ExpireDayOptions = []int{1, 3, 7, 30, 0}

// DefaultExpireDays / DefaultMaxDevices 是建分享时不指定时的兜底。
const (
	DefaultExpireDays = 7
	DefaultMaxDevices = 5
)

// NormalizeExpireDays 把用户传的「几天」收敛到合法取值域。
//
// 非法值**回落到默认**而不是报错：有效期这个字段来自下拉框，
// 正常路径不会传错值，而报错会让老界面/脚本整个功能不可用。
// 返回默认值 7 而不是 0 —— 回落成永久分享是安全方向的错误（见下）。
func NormalizeExpireDays(v int) int {
	for _, d := range ExpireDayOptions {
		if v == d {
			return v
		}
	}
	return DefaultExpireDays
}

// ExpireAt 由天数算出到期时刻。0 = 永久（返回 nil）。
func ExpireAt(days int, now time.Time) *time.Time {
	if NormalizeExpireDays(days) == 0 {
		return nil
	}
	t := now.UTC().Add(time.Duration(days) * 24 * time.Hour)
	return &t
}

// NormalizeMaxDevices 收敛设备数上限。
//
// 0 视为「用默认」而不是「不限」：设备数上限必须是有限值，
// 否则一个把链接转发到论坛的人就能在一台服务器后面刷出无限并发。
func NormalizeMaxDevices(v int) int {
	if v <= 0 {
		return DefaultMaxDevices
	}
	if v > 100 {
		return 100
	}
	return v
}

// CreateInput 是创建分享的入参。
type CreateInput struct {
	AccountID    int64
	FileID       string
	Title        string
	Season       int
	Comment      string
	Password     string
	ExpireDays   int
	MaxDevices   int
	CreatedBy    int64
	MaxTitleLen  int
	MaxCommentLn int
}

// Created 是创建结果的出参。
type Created struct {
	Share       Share
	Code        string
	HasPassword bool
}

// Settings 是本包读配置需要的窄接口。
//
// 刻意不给 context 也不给 fallback：settings.Service 内部已经按 registry 的
// 默认值回落过一遍，这里再兜一层默认值就成了第二个真相来源。
type Settings interface {
	String(key string) string
	Bool(key string) bool
	Int(key string) int
}

// 本包用到的配置键**直接引用 internal/settings 里的常量**，
// 不在这里另抄一份字符串：常量名和键值一旦各写各的，
// 就会出现「服务里读的是 mo_library_share_enabled，登记表里写的是另一个拼法」
// 这类只在运行时才暴露的坑（同 internal/mediarequest 的做法）。
const (
	configKeyEnabled       = settings.KeyMOLibraryShareEnabled
	configKeyExpireDays    = settings.KeyMOLibraryShareDefaultExpireDays
	configKeyMaxDevices    = settings.KeyMOLibraryShareDefaultMaxDevices
	configKeyDefaultPasswd = settings.KeyMOLibraryShareDefaultPassword
)

// Logger 是本包需要的日志能力，用窄接口而不是直接依赖 slog（便于测试替身）。
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}

// Service 是分享功能的业务层。
//
// 依赖用窄接口注入（Settings、Logger）而不是直接依赖具体服务，
// 与 internal/mediarequest 同一套纪律：少一处「必须记得赋值」的义务。
type Service struct {
	store *Store
	cfg   Settings
	log   Logger
	now   func() time.Time
}

// Params 是 Service 的构造参数。
type Params struct {
	Store *Store
	Cfg   Settings
	Log   Logger
	// Now 供测试注入时钟。不传则用 time.Now().UTC()。
	Now func() time.Time
}

// NewService 构造 Service。store 或 cfg 为 nil 时返回一个所有方法都报错的空服务，
// 避免装配层漏填后在真机上 panic。
func NewService(p Params) *Service {
	svc := &Service{store: p.Store, cfg: p.Cfg, log: p.Log, now: func() time.Time { return time.Now().UTC() }}
	if p.Now != nil {
		svc.now = func() time.Time { return p.Now().UTC() }
	}
	return svc
}

// SetClock 替换时钟，仅测试用。
func (s *Service) SetClock(fn func() time.Time) {
	if fn != nil {
		s.now = func() time.Time { return fn().UTC() }
	}
}

func (s *Service) clock() time.Time { return s.now() }

// Enabled 读总开关。开关或服务都没就绪时返回 false（fail-closed：
// 「读不到配置」绝不能被当成「功能是开的」）。
func (s *Service) Enabled() bool {
	if s == nil || s.store == nil || s.cfg == nil {
		return false
	}
	return s.cfg.Bool(configKeyEnabled)
}

// DefaultExpireDays 读配置的默认有效期。
func (s *Service) DefaultExpireDays() int {
	if s == nil || s.cfg == nil {
		return DefaultExpireDays
	}
	return NormalizeExpireDays(s.cfg.Int(configKeyExpireDays))
}

// DefaultMaxDevices 读配置的默认设备数。
func (s *Service) DefaultMaxDevices() int {
	if s == nil || s.cfg == nil {
		return DefaultMaxDevices
	}
	return NormalizeMaxDevices(s.cfg.Int(configKeyMaxDevices))
}

func (s *Service) DefaultPassword() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	return strings.TrimSpace(s.cfg.String(configKeyDefaultPasswd))
}

// Create 创建一条分享，返回给创建者一次性的链接信息。
func (s *Service) Create(ctx context.Context, in CreateInput) (Created, error) {
	if !s.Enabled() {
		return Created{}, ErrDisabled
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return Created{}, fmt.Errorf("%w：必须给分享起个名字", ErrInvalid)
	}
	fileID := strings.TrimSpace(in.FileID)
	if fileID == "" {
		return Created{}, fmt.Errorf("%w：必须指定要分享的文件", ErrInvalid)
	}
	// 有效期的三态：>0 具体天数、0 永久、负数「未指定，走配置默认」。
	// 负数这个约定写在 NormalizeCreateDays 里 —— 用 0 表示「未指定」的话，
	// 显式选永久的调用方会被当成没填，把永久分享变成 7 天后失效。
	days := NormalizeCreateDays(in.ExpireDays, s.DefaultExpireDays())
	maxDev := in.MaxDevices
	if maxDev <= 0 {
		maxDev = s.DefaultMaxDevices()
	}
	password := strings.TrimSpace(in.Password)
	if password == "" {
		password = s.DefaultPassword()
	}
	passwordHash := ""
	if password != "" {
		passwordHash = security.HashPassword(password)
	}
	now := s.clock()
	sh := Share{
		ID:           uuid.NewString(),
		AccountID:    in.AccountID,
		FileID:       fileID,
		Title:        maskLabel(title),
		Season:       in.Season,
		Comment:      maskLabel(in.Comment),
		PasswordHash: passwordHash,
		ExpiresAt:    ExpireAt(days, now),
		MaxDevices:   NormalizeMaxDevices(maxDev),
		CreatedBy:    in.CreatedBy,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	code, err := newCode()
	if err != nil {
		return Created{}, err
	}
	sh.Code = code
	created, err := s.store.Insert(ctx, sh)
	if err != nil {
		return Created{}, err
	}
	if s.log != nil {
		// ⚠️ 这里**不能**打印 code 或 passwordHash：code 进了日志就等于把门牌号公开，
		// 而门牌号会出现在分享链接里，日志常常比链接传得远。
		s.log.Info("创建免登录分享", "share_id", created.ID, "title", created.Title)
	}
	return Created{Share: created, Code: code, HasPassword: passwordHash != ""}, nil
}

// NormalizeCreateDays 处理「未指定有效期」与「显式选永久」的区分。
//
// 0 在 UI 上是「永久」，所以不能用它同时表示「没传」——
// 那样显式选永久的调用方会被当成没填。约定：负数表示未指定，走配置默认。
func NormalizeCreateDays(v, fallback int) int {
	if v < 0 {
		v = fallback
	}
	return NormalizeExpireDays(v)
}

// List 列出分享，最新的在前。
//
// 顺带回一张「有效设备数」的表：管理端列表要显示「几台设备在用」，
// 而 max_devices 的限制只在这时才有意义 —— 一个数字看不出是否已顶满。
func (s *Service) List(ctx context.Context, limit int) ([]Share, map[string]int, error) {
	if s == nil || s.store == nil {
		return nil, nil, ErrDisabled
	}
	if limit <= 0 {
		limit = 200
	}
	shares, err := s.store.List(ctx, limit)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]string, 0, len(shares))
	for _, sh := range shares {
		ids = append(ids, sh.ID)
	}
	active, err := s.store.ActiveDevicesByShare(ctx, ids, s.clock())
	if err != nil {
		// 设备数只是展示用的附加信息。拿不到就让它缺席（前端显示 0），
		// 不能因为一个统计数字把整个列表接口搞挂 —— 那是拿可用性换好看。
		s.log.Warn("统计分享有效设备数失败", "err", err.Error())
		return shares, map[string]int{}, nil
	}
	return shares, active, nil
}

// Get 按主键取一条分享，供管理端改期限后回读。
//
// 单独开这个口而不是让调用方直接碰 Store：管理端是唯一一个
// 「不按短码、只按主键」访问分享的角色，把它挡在 Service 外面
// 就等于开了一条绕过开关与撤销判定的后门。
func (s *Service) Get(ctx context.Context, id string) (Share, error) {
	if s == nil || s.store == nil {
		return Share{}, ErrDisabled
	}
	return s.store.Get(ctx, id)
}

// Delete 撤销一条分享。
func (s *Service) Delete(ctx context.Context, id string) error {
	if s == nil || s.store == nil {
		return ErrDisabled
	}
	return s.store.Revoke(ctx, id, s.clock())
}

// SetExpiry 改有效期。days<0 = 永久，>=0 按天数。
func (s *Service) SetExpiry(ctx context.Context, id string, days int) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	if s == nil || s.store == nil {
		return ErrDisabled
	}
	var exp *time.Time
	if days >= 0 {
		d := NormalizeExpireDays(days)
		if d != 0 {
			t := s.clock().Add(time.Duration(d) * 24 * time.Hour)
			exp = &t
		}
	}
	return s.store.UpdateExpiry(ctx, id, exp, s.clock())
}

// Stats 取统计。
func (s *Service) Stats(ctx context.Context, id string, recent int) (Stats, error) {
	if s == nil || s.store == nil {
		return Stats{}, ErrDisabled
	}
	if recent <= 0 {
		recent = 20
	}
	return s.store.Stats(ctx, id, s.clock(), recent)
}

// LookupByCode 按分享链接里的短码取分享，并做开关与有效性判定。
//
// 这是访客侧所有端点的共同入口，所以「开关关了返回什么」在这里定一次：
// 统一返回 ErrNotFound，而不是 ErrDisabled ——
// 开关关闭时对外应当表现为「这条分享不存在」，而不是「这个功能存在但没开」。
func (s *Service) LookupByCode(ctx context.Context, code string) (Share, error) {
	if !s.Enabled() {
		return Share{}, ErrNotFound
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return Share{}, ErrNotFound
	}
	sh, err := s.store.GetByCodeHash(ctx, HashToken(code))
	if err != nil {
		return Share{}, ErrNotFound
	}
	now := s.clock()
	if !sh.Usable(now) {
		return Share{}, ErrNotFound
	}
	return sh, nil
}

// IssueToken 为一个访客签发（或复用）令牌。
//
// 流程（对应任务书 POST /share-play/{code}/token）：
//  1. 短码找分享（已做开关/有效期判定）。
//  2. 若分享有口令，校验口令。
//  3. 找这个访客在 24 小时内的会话；有则复用（不换令牌）。
//  4. 没有则建新会话，并在**设备数未超**时签发令牌。
//
// 第 3 步复用而不是每次发新令牌：每次刷新页面都换令牌会让正在播放的视频
// 突然 401，用户看到的就是「播到一半卡住了」。
func (s *Service) IssueToken(ctx context.Context, code, password, visitorID, ip, ua string) (IssueResult, error) {
	sh, err := s.LookupByCode(ctx, code)
	if err != nil {
		return IssueResult{}, err
	}
	if sh.HasPassword() {
		if !s.checkPassword(sh, password) {
			if strings.TrimSpace(password) == "" {
				return IssueResult{}, ErrPasswordRequired
			}
			return IssueResult{}, ErrBadPassword
		}
	}
	visitorID = strings.TrimSpace(visitorID)
	if visitorID == "" {
		return IssueResult{}, fmt.Errorf("%w：缺少访客标识", ErrInvalid)
	}
	now := s.clock()
	ipMasked := MaskIP(ip)
	uaSnippet := UserAgentSnippet(ua)
	if v, ok, err := s.store.FindVisitByVisitor(ctx, sh.ID, visitorID, now); err != nil {
		return IssueResult{}, err
	} else if ok {
		// 已有活跃会话：直接放行，不查设备数。
		if err := s.countVisitorOnce(ctx, sh.ID, v.ID); err != nil {
			return IssueResult{}, err
		}
		// 复用的会话不返回明文令牌（那一次的明文早给过了，且不持久化）。
		// 但前端需要知道「你有令牌」，所以返回 has_token=true。
		return IssueResult{Share: sh, Visit: v, HasToken: true, IsNewVisit: false}, nil
	}
	// 新访客：先查设备数，满则拒。
	devices, err := s.store.CountDevices(ctx, sh.ID, now)
	if err != nil {
		return IssueResult{}, err
	}
	if devices >= sh.MaxDevices {
		return IssueResult{}, ErrDeviceLimit
	}
	token, err := newToken()
	if err != nil {
		return IssueResult{}, err
	}
	// ⚠️ UpsertVisit 返回的 created 必须当真：并发首访时只有一个请求真的插进去，
	// 另一个撞唯一约束走 DO UPDATE —— 那一支的 DO UPDATE 不写 token_hash，
	// 所以这个新签发的明文令牌**根本没进库**，回传给前端就是一枚永远 401 的假令牌。
	// 用户症状是「点开链接提示成功，播放器一直报需要重新打开」。
	v, created, err := s.store.UpsertVisit(ctx, sh.ID, visitorID, HashToken(token), ipMasked, uaSnippet, now)
	if err != nil {
		return IssueResult{}, err
	}
	if err := s.countVisitorOnce(ctx, sh.ID, v.ID); err != nil {
		return IssueResult{}, err
	}
	if !created {
		// 撞车了：库里的会话是别人的（同一个 visitor_id 的并发请求），
		// 明文令牌拿不回来，只能让前端沿用它已经有的那一枚。
		return IssueResult{Share: sh, Visit: v, HasToken: true, IsNewVisit: false}, nil
	}
	return IssueResult{Share: sh, Visit: v, Token: token, HasToken: true, IsNewVisit: true}, nil
}

// countVisitorOnce 落实验收第 4 条：同一个访客在一个 24 小时窗口里
// 无论打开多少次，visitor_count 只加一。
//
// 加计数的条件挂在 MarkVisitCounted 的 RowsAffected 上，而不是「先查 counted 再决定」：
// 两个并发请求会同时查到 counted=0，然后双双加一，计数就翻倍了。
func (s *Service) countVisitorOnce(ctx context.Context, shareID string, visitID int64) error {
	first, err := s.store.MarkVisitCounted(ctx, visitID)
	if err != nil {
		return err
	}
	if !first {
		return nil
	}
	return s.store.BumpShareCounters(ctx, shareID, 0, 0, 1)
}

// IssueResult 是签发令牌的结果。
type IssueResult struct {
	Share Share
	Visit Visit
	// Token 是明文令牌，**只在 IsNewVisit 为真时非空** —— 复用会话时前端本地已有它。
	Token string
	// HasToken 告诉前端「你可以带 X-Share-Token 了」，无论 Token 是否回传。
	HasToken   bool
	IsNewVisit bool
}

// AuthorizeToken 校验 X-Share-Token 头，返回该令牌对应的分享与访客。
//
// 播放取流走这个函数。它做三件事：
//  1. 令牌哈希查库（查不到 = 无效）。
//  2. 分享仍可用（没撤销、没过期）。
//  3. 会话仍活跃（last_seen 在 24 小时内）。
//
// 第 3 条容易被忽略但很关键：一个访客的令牌如果永远有效，
// 那「设备数上限」就形同虚设 —— 访客只要存下令牌，隔一个月再拿来也能过。
func (s *Service) AuthorizeToken(ctx context.Context, token string) (Share, Visit, error) {
	if !s.Enabled() {
		return Share{}, Visit{}, ErrNotFound
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return Share{}, Visit{}, ErrBadToken
	}
	v, ok, err := s.store.FindVisitByToken(ctx, HashToken(token))
	if err != nil {
		return Share{}, Visit{}, err
	}
	if !ok {
		return Share{}, Visit{}, ErrBadToken
	}
	sh, err := s.store.Get(ctx, v.ShareID)
	if err != nil {
		return Share{}, Visit{}, ErrBadToken
	}
	// ⚠️ 分享本身失效（过期/撤销）时返回 ErrNotFound 而不是 ErrBadToken。
	//
	// 两者对访客是完全不同的处境：401「令牌无效或已过期」会把前端引去
	// 重新换令牌，而 LookupByCode 同样会拒绝这条分享，于是多跑一趟还是失败，
	// 用户看到的是「链接好好的但一直说令牌有问题」。404「分享不存在或已失效」
	// 才是访客真正需要知道的那句话。
	//
	// 这不会变成信息泄露：能走到这一行说明调用方**已经持有这条分享的有效令牌**，
	// 对手本来就知道分享存在；拿不到令牌的请求在上一行就返回 ErrBadToken，
	// 两种错误码对外没有可枚举的差异。
	if !sh.Usable(s.clock()) {
		return Share{}, Visit{}, ErrNotFound
	}
	if !v.LastSeenAt.After(s.clock().Add(-VisitorCountWindow)) {
		// 会话太老了：令牌作废。下一次打开分享页会重新签发一个新的。
		return Share{}, Visit{}, ErrBadToken
	}
	return sh, v, nil
}

// RecordPlay 记一次取流。
//
// 这是 play_count 的**唯一**落点：只有真的开始取流（拿到令牌、解析出直链）
// 才算一次播放。前端上报的 play 事件只留流水不计数，原因见 RecordEvent。
func (s *Service) RecordPlay(ctx context.Context, sh Share, v Visit, ip, method, label string) error {
	if sh.ID == "" {
		return nil
	}
	if err := s.insertPlayRow(ctx, sh, v, ip, method, label); err != nil {
		return err
	}
	// 流水写成功才计数。否则统计里会出现「计了 N 次播放但一条流水都没有」，
	// 而明细页一打开就露馅，比数字偏小更难解释。
	return s.store.BumpShareCounters(ctx, sh.ID, 0, 1, 0)
}

// insertPlayRow 只写流水，不动 play_count。
//
// 前端上报的 play 事件走这里：访客点了播放那一刻可能还在缓冲、可能直接
// 被人关掉，真开始取流的路径（ShareStream）才是播放次数的口径。
func (s *Service) insertPlayRow(ctx context.Context, sh Share, v Visit, ip, method, label string) error {
	if sh.ID == "" {
		return nil
	}
	if method == "" {
		method = "direct"
	}
	return s.store.InsertPlay(ctx, Play{
		ID:        uuid.NewString(),
		ShareID:   sh.ID,
		VisitID:   v.ID,
		VisitorID: v.VisitorID,
		FileID:    sh.FileID,
		ItemLabel: maskLabel(label),
		Method:    method,
		IPMasked:  MaskIP(ip),
		StartedAt: s.clock(),
	})
}

// RecordEvent 记一次前端上报的事件（open / play / visitor）。
//
// 这是给「统计」用的旁路，不是安全边界：真正的计数在 RecordPlay 和 IssueToken 里。
// 前端上报只是让「打开页面」也被计一次 view（否则纯浏览的人不留痕）。
func (s *Service) RecordEvent(ctx context.Context, code, visitorID, ip, event string, label string) error {
	sh, err := s.LookupByCode(ctx, code)
	if err != nil {
		return err
	}
	switch EventType(strings.TrimSpace(event)) {
	case EventOpen:
		return s.store.BumpShareCounters(ctx, sh.ID, 1, 0, 0)
	case EventPlay:
		// 前端上报 play 只留流水，不计 play_count —— 真正的播放计数由取流时
		// 的 RecordPlay 负责（这里是 insertPlayRow，不是 RecordPlay）。
		// 访客点了播放那一刻可能还在缓冲、也可能直接关掉，那不该算一次播放。
		return s.insertPlayRow(ctx, sh, Visit{VisitorID: strings.TrimSpace(visitorID)}, ip, "direct", label)
	case EventVisitor:
		return nil // visitor 已在 IssueToken 里计过，这里不重复计
	}
	return nil
}

func (s *Service) checkPassword(sh Share, password string) bool {
	return security.CheckPasswordHash(sh.PasswordHash, strings.TrimSpace(password))
}

// DaysUntil 距离 expires_at 还有几天。已过期返回负数，永久（nil）返回 0。
//
// 服务端算而不是前端解析时间戳，是因为前端拿到的是 UTC RFC3339、
// 要自己处理时区与舍入，两边算出差一天用户也不觉得是 bug。
// 这里是 UTC 整日差，语义粗到「够用」但不会跨天错。
func DaysUntil(exp *time.Time, now time.Time) int {
	if exp == nil || exp.IsZero() {
		return 0
	}
	return int(math.Round(exp.Sub(now).Hours() / 24))
}
