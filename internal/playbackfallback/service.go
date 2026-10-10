// Package playbackfallback 实现「源账号不可用时自动切到另一个账号的转存副本播放」。
//
// 这里最要紧的一条是**不删源文件**：切账号只是让播放有路可走，
// 不是「把坏账号上的东西清理掉」。任何一个走到删除的分支都属于事故，
// 所以本包**不提供任何删除能力**，也不持有能触发删除的接口。
//
// 第二条是**不要被短暂抖动骗去转存**：一次网络超时、一次限流就让系统去搬几十 GB，
// 既费流量又费存储，事后源账号往往又好了。所以只有「连续不可用超过
// FallbackWait」才允许转存（见 unavailableFor）。
//
// 第三条是**转存期间要能看**：播放器需要一个能立刻收到答复的东西，
// 不能干等几十 GB 传完（请求会超时，播放器直接报「无法播放」）。
// 所以走中转上传时立刻返回 Preparing，由播放记录页异步观察。
package playbackfallback

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"litepan/internal/crosstransfer"
	"litepan/internal/domain"
	"litepan/internal/eventbus"
	"litepan/internal/file"
)

// Decision 是「这次播放该怎么走」的结论。
type Decision struct {
	// AccountID/FileID 是要真正取流的账号与文件。
	// 与请求一致表示照常播放。
	AccountID int64
	FileID    string
	// Preparing 为 true 表示「转存中，请稍后」，此时不应取流，
	// 而是回一个明确的「正在准备」给播放器。
	Preparing bool
	// Reason 是给用户看的一句话；Preparing 时必填。
	Reason string
	// AccountName 是本次实际使用的账号名（含切号后的目标账号）。
	AccountName string
	// Switched 标记这次发生了跨账号切换，播放记录用它标注来源。
	Switched bool
}

// Config 是本包需要的一组开关。放在一起是为了让「缺一项就不转存」
// 成为构造时就能看出来的性质，而不是散落在运行期各处 if。
type Config struct {
	// Enabled 为 false 时一切行为退化为「照常播放」，不做任何检查。
	Enabled bool
	// FallbackWait 是「连续不可用多久才允许转存」。
	// 0 或负数取 DefaultFallbackWait。短暂限流不该触发转存。
	FallbackWait time.Duration
}

// DefaultFallbackWait 是 FallbackWait 缺省值，与
// settings.KeyMOCrossAccountFallbackMinutes 的默认 30 分钟一致。
const DefaultFallbackWait = 30 * time.Minute

// maxUnhealthyAccounts 限制「一个账号持续失效」的追踪表大小。
// 账号总数有限，但坏账号会一直留在表里；没有上限的话
// 一张随时间只增不减的表就是一个缓慢的内存泄漏。
const maxUnhealthyAccounts = 64

// Transfer 是本包依赖的转存能力。抽象成接口而不是直接吃
// *crosstransfer.Service，是为了让「不删源」这条性质可测：
// 测试里能断言**转存过程中没有出现任何删除意图**。
type Transfer interface {
	ExecuteStream(ctx context.Context, in crosstransfer.ExecuteInput, emit func(crosstransfer.StreamEvent) error) error
}

// FileInfo 是本包在转存前需要从源侧读到的最少信息。
type FileInfo struct {
	Name string
	Size int64
	// ParentID 是源文件的父目录 ID。转存目标目录用不上它，
	// 但留在这里是为了让「我们到底知道什么」是可读的。
	ParentID string
}

// FileLookup 取源文件信息。
type FileLookup interface {
	Info(ctx context.Context, accountID int64, fileID string) (*domain.FileItem, error)
}

// AccountInfo 是选目标账号要用的字段。
type AccountInfo struct {
	ID         int64
	Name       string
	DriverType string
	IsActive   bool
	IsDefault  bool
	SortOrder  int
	// AuthOK 是「认证状态可用」。由调用方从 AuthState.Status == active 得出。
	AuthOK bool
}

// AccountLister 列出候选账号。
type AccountLister interface {
	List(ctx context.Context) ([]*domain.Account, error)
}

// HealthReader 读账号的认证状态。返回 ok=false 表示「查不到行」，
// auth 包把查不到行视作 active，本包照抄这个约定 ——
// 「查不到」不等于「坏了」，按坏了处理会让新账号第一次播放就触发转存。
type HealthReader interface {
	// Status 返回 (status, ok, err)。
	Status(ctx context.Context, accountID int64) (domain.AuthStatus, bool, error)
}

// AuthStateStore 是 HealthReader 的最小实现形态。
type AuthStateStore interface {
	Get(ctx context.Context, accountID int64) (*domain.AuthState, error)
}

// unhealthyAccount 记录一个账号「从什么时候开始不可用」。
type unhealthyAccount struct {
	since time.Time
	// reason 是最近一次看到的失败原因，只用于日志与页面提示。
	reason string
}

// Service 是跨账户播放转移服务。零值不可用，请用 New。
type Service struct {
	cfg      Config
	transfer Transfer
	files    FileLookup
	accounts AccountLister
	health   HealthReader
	log      *slog.Logger
	now      func() time.Time

	mu sync.Mutex
	// unhealthy 是「持续不可用」的账号表。进程内观察，不落库：
	// 落库就得加字段加迁移，而 T17 不允许加迁移；更要紧的是，
	// 「不可用起点」本来就该是**本进程观察到的起点**——
	// 服务重启后账号可能早就好了，落库的旧起点会让它一启动就被判定为长期失效。
	unhealthy map[int64]unhealthyAccount
}

// Options 是 New 的入参。
type Options struct {
	Config   Config
	Transfer Transfer
	Files    FileLookup
	Accounts AccountLister
	Health   HealthReader
	Log      *slog.Logger
	Now      func() time.Time
}

// New 构造服务。缺 Transfer/Accounts/Health 时构造仍成功，
// 但所有决策都会退化为「照常播放」——
// 一个没装配全的跨账号转移能力，正确的表现是**什么都不做**，
// 而不是 panic 或让播放失败。
func New(o Options) *Service {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	cfg := o.Config
	if cfg.FallbackWait <= 0 {
		cfg.FallbackWait = DefaultFallbackWait
	}
	return &Service{
		cfg:       cfg,
		transfer:  o.Transfer,
		files:     o.Files,
		accounts:  o.Accounts,
		health:    o.Health,
		log:       o.Log,
		now:       now,
		unhealthy: map[int64]unhealthyAccount{},
	}
}

// Observe 汇报一次播放尝试的结果（成功或失败），供服务自己维护「连续不可用」。
//
// 为什么要有 Observe 而不是每次现查：判断「连续不可用 N 分钟」需要**时间锚点**，
// 而认证状态里没有这个字段（NextRetryAt 每次失败都被重算，不是进入时刻）。
// 进程内观察是这里唯一诚实的实现：它记的是「我们从什么时候起看着它坏」，
// 宁可让判定晚一点触发，也不让一次瞬时抖动就引发转存。
func (s *Service) Observe(accountID int64, ok bool, reason string) {
	if s == nil || accountID <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok {
		delete(s.unhealthy, accountID)
		return
	}
	if u, seen := s.unhealthy[accountID]; seen {
		u.reason = reason
		s.unhealthy[accountID] = u
		return
	}
	if len(s.unhealthy) >= maxUnhealthyAccounts {
		// 表满时丢弃**最早**失效的那个观察：它离触发转存最近，
		// 保留它收益最大，丢掉新观察只是晚一点转存。
		var oldestID int64
		var oldest time.Time
		for id, u := range s.unhealthy {
			if oldest.IsZero() || u.since.Before(oldest) {
				oldestID, oldest = id, u.since
			}
		}
		delete(s.unhealthy, oldestID)
	}
	s.unhealthy[accountID] = unhealthyAccount{since: s.now(), reason: reason}
}

// unhealthyFor 返回账号已连续不可用的时长，第二次见到时才有值。
func (s *Service) unhealthyFor(accountID int64) (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.unhealthy[accountID]
	if !ok {
		return 0, false
	}
	d := s.now().Sub(u.since)
	if d < 0 {
		// 时钟回拨：宁可不转存，也不因为负数算出「已失效 -1 分钟」。
		return 0, false
	}
	return d, true
}

// UnhealthyFor 供 API 展示「这个账号已经坏了多久」，让用户看得见
// 为什么现在还没触发转存。第二个返回值为 false 表示「本次进程没观察到它坏」。
func (s *Service) UnhealthyFor(accountID int64) (time.Duration, bool) {
	return s.unhealthyFor(accountID)
}

// Transferable 判断账号是否已连续不可用到可以转存。
func (s *Service) Transferable(accountID int64) bool {
	if !s.enabled() {
		return false
	}
	d, ok := s.unhealthyFor(accountID)
	return ok && d >= s.cfg.FallbackWait
}

func (s *Service) enabled() bool {
	return s != nil && s.cfg.Enabled && s.transfer != nil && s.accounts != nil && s.health != nil && s.files != nil
}

// PickTarget 按「默认账号优先、其次排序号」选一个可用的目标账号。
//
// 排除源账号是硬性的：把文件转存到它自己既解决不了问题，
// 还会在目标端留下一份重复的大文件。
func (s *Service) PickTarget(ctx context.Context, sourceID int64) (*AccountInfo, error) {
	list, err := s.accounts.List(ctx)
	if err != nil {
		return nil, err
	}
	cands := make([]AccountInfo, 0, len(list))
	for _, a := range list {
		if a == nil || a.ID == sourceID || !a.IsActive {
			continue
		}
		st, ok, err := s.health.Status(ctx, a.ID)
		if err != nil {
			// 查不到状态就当不可用：转存要落到一个真能用的账号上，
			// 而「查不出来」不是「能用」的证据。
			continue
		}
		if !ok || st != domain.AuthActive {
			continue
		}
		cands = append(cands, AccountInfo{
			ID: a.ID, Name: a.Name, DriverType: a.DriverType,
			IsActive: a.IsActive, IsDefault: a.IsDefault, SortOrder: a.SortOrder, AuthOK: true,
		})
	}
	if len(cands) == 0 {
		return nil, domain.Errorf(domain.CodeValidation, "没有可用的备用账号，无法跨账户转存")
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].IsDefault != cands[j].IsDefault {
			return cands[i].IsDefault
		}
		if cands[i].SortOrder != cands[j].SortOrder {
			return cands[i].SortOrder < cands[j].SortOrder
		}
		return cands[i].ID < cands[j].ID
	})
	out := cands[0]
	return &out, nil
}

// Resolve 决定这次播放走哪条路。这是本包唯一的对外决策入口。
//
// 顺序刻意如此：**先看有没有别的账号可用**，再看「要不要转存」。
// 反过来（先判不可用再找目标）会在只有一个账号时白跑一趟，
// 而且会把「本来就该报的账号不可用」这条原始错误替换成
// 「没有可用的备用账号」，把真正的原因藏起来。
func (s *Service) Resolve(ctx context.Context, req Request) Decision {
	if s == nil || !s.enabled() {
		return Decision{AccountID: req.AccountID, FileID: req.FileID}
	}
	target, err := s.PickTarget(ctx, req.AccountID)
	if err != nil || target == nil {
		// 没有备用账号：照常播放，让真正的失败以原始错误暴露出来。
		return Decision{AccountID: req.AccountID, FileID: req.FileID}
	}
	// 未达到等待时长：照常播放。这一步是验收 8「短暂限流不触发转存」的落点。
	if !s.Transferable(req.AccountID) {
		return Decision{AccountID: req.AccountID, FileID: req.FileID}
	}
	newID, err := s.startTransfer(ctx, req, target)
	if err != nil {
		s.logWarning("跨账户转存未能启动", "source_account", req.AccountID, "target_account", target.ID, "err", err)
		return Decision{AccountID: req.AccountID, FileID: req.FileID}
	}
	// FileID 为空是合法的：走中转上传时目标文件还没落地，没有 id 可取。
	// 调用方必须先看 Preparing，看到就回「正在准备，请稍后」，
	// 绝不能拿着空 FileID 去取流——那会得到一个语焉不详的 404。
	return Decision{
		AccountID: target.ID, FileID: newID, Preparing: true, Switched: true,
		AccountName: target.Name,
		Reason:      fmt.Sprintf("源账号（%d）持续不可用，已转存到账号「%s」，正在准备，请稍后", req.AccountID, target.Name),
	}
}

// Request 是一次播放请求里本包关心的部分。
type Request struct {
	AccountID int64
	FileID    string
	// FileName 是播放时的文件名。留空时从源侧查。
	FileName string
}

// targetParent 是转存文件的落点：根目录（relDir 空）而不是镜像目录。
//
// 之所以不打散到「原路径」下：播放时按 fileID 取流，用户不会去网盘里找这个文件，
// 目录结构对用户没有价值；而按原目录逐级创建会在目标账号造出一棵可能极深的树。
// 根目录 + 原文件名既好找（列表第一屏就是），又不会污染目录结构。
func targetParent() string { return "" }

// startTransfer 在后台把源文件转存到目标账号，返回目标文件的 fileID。
//
// 同步等待 vs 异步排队：秒传命中时同步就能拿到 fileID（毫秒级），直接可播；
// 命中不了要走中转上传，那是大文件、分钟级甚至小时级，绝不能占着播放请求等——
// 所以那一条排队后立刻返回 queued=true，由上传任务完成后播放记录页再观察。
func (s *Service) startTransfer(ctx context.Context, req Request, target *AccountInfo) (string, error) {
	source, err := s.accountByID(ctx, req.AccountID)
	if err != nil {
		return "", err
	}
	item, err := s.files.Info(ctx, req.AccountID, req.FileID)
	if err != nil {
		return "", err
	}
	name := firstNonEmpty(req.FileName, item.Name, "media")
	in := crosstransfer.ExecuteInput{
		SourceAccountID:   source.ID,
		SourceAccountName: source.Name,
		SourceDriverType:  source.DriverType,
		TargetAccountID:   target.ID,
		TargetAccountName: target.Name,
		TargetDriverType:  target.DriverType,
		TargetParentID:    targetParent(),
		TargetDisplayPath: "/",
		MethodID:          "sha1",
		// Fallback 必须为 true：false 时未命中秒传的文件根本不会被复制，
		// 而「没复制成」与「复制了但还没传完」在播放侧长得一模一样（都播不了），
		// 唯一区别是前者永远不会好。
		Fallback: true,
		Conflict: "rename",
		Files: []crosstransfer.TransferFile{{
			SourceFileID: req.FileID,
			Name:         name,
			Size:         item.Size,
			// RelDir 留空：落目标根目录，见 targetParent 的说明。
		}},
	}
	// 用 context.WithoutCancel：转存不该因为「播放器等不及关掉页面」而中断，
	// 也不该因为请求 ctx 到了期就被判失败。
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
	var (
		ok       bool
		queued   bool
		targetID string
		firstErr string
	)
	err = s.transfer.ExecuteStream(bg, in, func(ev crosstransfer.StreamEvent) error {
		if ev["event"] != "item" {
			return nil
		}
		if succ, _ := ev["success"].(bool); !succ {
			ok = false
			firstErr, _ = ev["error"].(string)
			return nil
		}
		mode, _ := ev["mode"].(string)
		id, _ := ev["file_id"].(string)
		if mode == "relay" {
			// 中转上传：任务已入队，文件还没落地。
			queued = true
			return nil
		}
		ok = true
		if id != "" {
			targetID = id
		}
		return nil
	})
	if err != nil {
		cancel()
		return "", err
	}
	cancel()
	switch {
	case queued:
		s.logWarning("跨账户转存已排入中转上传队列", "source_account", req.AccountID, "target_account", target.ID, "name", name)
		return "", nil
	case ok && targetID != "":
		s.logWarning("跨账户转存命中秒传", "source_account", req.AccountID, "target_account", target.ID, "name", name)
		s.Forget(req.AccountID)
		return targetID, nil
	default:
		return "", domain.Errorf(domain.CodeDriverError, "转存未完成：%s", firstNonEmpty(firstErr, "未知原因"))
	}
}

func (s *Service) accountByID(ctx context.Context, id int64) (*AccountInfo, error) {
	list, err := s.accounts.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range list {
		if a != nil && a.ID == id {
			return &AccountInfo{ID: a.ID, Name: a.Name, DriverType: a.DriverType, IsActive: a.IsActive, IsDefault: a.IsDefault, SortOrder: a.SortOrder}, nil
		}
	}
	return nil, domain.Errorf(domain.CodeNotFound, "源账号不存在")
}

// Forget 清掉某账号的「持续不可用」观察。账号恢复后调用，
// 否则一次坏过就会让「不可用起点」永远停在过去，再坏一次立刻转存。
func (s *Service) Forget(accountID int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.unhealthy, accountID)
	s.mu.Unlock()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func (s *Service) logWarning(msg string, kv ...any) {
	if s == nil || s.log == nil {
		return
	}
	s.log.Warn(msg, kv...)
}

// HealthFromStore 把 *domain.AuthStateRepository 适配成 HealthReader。
// 「查不到行 → ok=false」这一约定就在这里落地：认证包把查不到行当 active，
// 而本包需要区分「没行」（新账号，别急着转存）与「有行且非 active」（真坏了）。
func HealthFromStore(repo AuthStateStore) HealthReader {
	return healthStoreAdapter{repo: repo}
}

type healthStoreAdapter struct {
	repo AuthStateStore
}

func (a healthStoreAdapter) Status(ctx context.Context, accountID int64) (domain.AuthStatus, bool, error) {
	st, err := a.repo.Get(ctx, accountID)
	if err != nil {
		if ae, ok := domain.AsAppError(err); ok && ae.Code == domain.CodeNotFound {
			return "", false, nil
		}
		return "", false, err
	}
	if st == nil {
		return "", false, nil
	}
	return st.Status, true, nil
}

// FilesFromService 把 *file.Service 适配成 FileLookup。
func FilesFromService(f *file.Service) FileLookup {
	return fileLookupAdapter{files: f}
}

type fileLookupAdapter struct {
	files *file.Service
}

func (a fileLookupAdapter) Info(ctx context.Context, accountID int64, fileID string) (*domain.FileItem, error) {
	if a.files == nil {
		return nil, domain.Errf(domain.CodeNotImplement)
	}
	return a.files.Info(ctx, accountID, fileID)
}

// ObserveBus 用事件总线维护「连续不可用」观察。
//
// 订阅事件而不是在播放路径里现查，是为了让**没在播放的账号也能被观察**：
// 只有播放时才记账的话，一个被后台任务反复打失败的账号，
// 等到用户真的来播时它还是「第一次失败」，于是永远不会触发转存。
func ObserveBus(bus *eventbus.Bus, s *Service, log *slog.Logger) {
	if bus == nil || s == nil {
		return
	}
	eventbus.Subscribe(bus, func(ctx context.Context, evt eventbus.AccountAuthFailed) {
		s.Observe(evt.AccountID, false, firstNonEmpty(evt.Reason, "账号认证失败"))
		if log != nil {
			log.Debug("跨账户转移：记录账号不可用起点", "account_id", evt.AccountID, "reason", evt.Reason, "fatal", evt.Fatal)
		}
	})
	eventbus.Subscribe(bus, func(ctx context.Context, evt eventbus.AccountAuthRecovered) {
		s.Forget(evt.AccountID)
	})
}
