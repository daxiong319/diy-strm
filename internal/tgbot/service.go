package tgbot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Logger 是 Bot 的日志出口。
type Logger interface {
	Debugf(format string, args ...any)
	Warnf(format string, args ...any)
}

// Service 是入站 Bot 的核心：消息进来 → 准入 → 命令路由 → 调现有 service → 回话。
type Service struct {
	cfg        Config
	registry   *Registry
	client     *Client
	runner     Runner
	search     Searcher
	link       LinkTransferer
	status     StatusProvider
	duplicate  DuplicateChecker
	recognizer Recognizer
	categories CategoryLister
	strm       StrmRunner
	subscriber Subscriber
	log        Logger
	tasks      *taskStore
	// pushOverride 只在测试里替换，用于断言「完成后主动推送」。
	superLookup  SuperLookup
	pushOverride func(ctx context.Context, chatID int64, text string) error
	// enabledHook 让装配层在配置变更时同步 webhook，测试里置 nil。
	enabledHook func(ctx context.Context, cfg Config) error
	now         func() time.Time

	// webhookSecret 是本地校验 Telegram 回调用的共享口令。
	//
	// **必须由本服务自己生成并持有**，不能拿请求头本身当期望值
	// （那等于「自己对自己比较」，任何人都能通过）。
	// 生成一次后保持不变：改了它，Telegram 侧还带着旧值发过来，
	// 所有消息都会被 401 拒掉，而且从 Telegram 的角度看毫无异常。
	secretMu sync.RWMutex
	secret   string
}

// Searcher 是 `/search` 需要的资源搜索能力。
//
// 定义成接口而不是直接依赖 discover 的具体 service：Bot 只需要「关键词 → 结果行」，
// 绑定到具体实现会让这层在每次搜索服务重构时都要跟着改。
type Searcher interface {
	Search(ctx context.Context, keyword string, limit int) ([]SearchHit, error)
}

// SearchHit 是一条搜索结果。Bot 只展示它，不解释它从哪来。
type SearchHit struct {
	Title    string
	Source   string
	Size     string
	ShareURL string
}

// Options 是 Bot 的装配选项。
type Options struct {
	Config     Config
	Client     *Client
	Runner     Runner
	Search     Searcher
	Link       LinkTransferer
	Status     StatusProvider
	Duplicate  DuplicateChecker
	Recognizer Recognizer
	Categories CategoryLister
	Strm       StrmRunner
	Subscriber Subscriber
	Log        Logger
	// Now 可注入时钟。
	Now func() time.Time
	// WebhookSecret 覆盖自动生成的回调口令，仅测试用。
	WebhookSecret string
	// RegisterCommands 允许测试注入一套精简命令表。
	RegisterCommands func(r *Registry)
}

// New 建 Bot 服务。
func New(opts Options) *Service {
	s := &Service{
		cfg:        opts.Config,
		client:     opts.Client,
		runner:     opts.Runner,
		search:     opts.Search,
		link:       opts.Link,
		status:     opts.Status,
		duplicate:  opts.Duplicate,
		recognizer: opts.Recognizer,
		categories: opts.Categories,
		strm:       opts.Strm,
		subscriber: opts.Subscriber,
		log:        opts.Log,
		tasks:      newTaskStore(),
		now:        opts.Now,
	}
	s.pushOverride = func(ctx context.Context, chatID int64, text string) error {
		if s.client == nil {
			return fmt.Errorf("tgbot: 未配置 Bot 客户端")
		}
		return s.client.SendMessage(ctx, chatID, text, SendMessageOpts{})
	}
	if s.now == nil {
		s.now = time.Now
	}
	if opts.WebhookSecret != "" {
		s.secret = opts.WebhookSecret
	} else if sec, err := newWebhookSecret(); err == nil {
		s.secret = sec
	}
	s.registry = NewRegistry()
	register := opts.RegisterCommands
	if register == nil {
		register = registerAll
	}
	register(s.registry)
	return s
}

// SetRunner 回填任务执行器。装配层需要 Bot 的 push 能力，所以 runner 晚于 New 构造。
func (s *Service) SetRunner(r Runner) { s.runner = r }

// Registry 暴露命令表，供 /help 与设置页展示。
func (s *Service) Registry() *Registry { return s.registry }

// Config 暴露当前配置。
func (s *Service) Config() Config { return s.cfg }

// Client 暴露 Bot API 客户端（装配层用它同步 webhook）。
func (s *Service) Client() *Client { return s.client }

// SyncConfig 换一份配置并同步 webhook。
//
// 关开关时必须主动 deleteWebhook：留着旧 webhook，Telegram 会继续把消息推过来，
// 每次都撞在「Bot 未启用」的拒绝分支上，用户看到的现象是「关了还一直回话」。
func (s *Service) SyncConfig(ctx context.Context, cfg Config) error {
	wasReady := s.cfg.Ready()
	s.cfg = cfg
	if s.enabledHook == nil {
		return nil
	}
	if cfg.Ready() {
		return s.enabledHook(ctx, cfg)
	}
	if wasReady && s.client != nil {
		if err := s.client.DeleteWebhook(ctx); err != nil {
			// 摘不掉不算致命：本地已经不放行任何消息了。
			s.warnf("tgbot: 关闭 Bot 时删除 webhook 失败: %v", err)
		}
	}
	return nil
}

// SetWebhookURL 让装配层告诉 Bot webhook 该指向哪里。
func (s *Service) SetWebhookURL(fn func(cfg Config) string) {
	s.enabledHook = func(ctx context.Context, cfg Config) error {
		url := fn(cfg)
		if url == "" || s.client == nil {
			return nil
		}
		return s.client.SetWebhook(ctx, url, s.WebhookSecret())
	}
}

// WebhookSecret 返回本地校验回调用的口令。
func (s *Service) WebhookSecret() string {
	s.secretMu.RLock()
	defer s.secretMu.RUnlock()
	return s.secret
}

// SetWebhookSecret 覆盖口令，仅供装配层从既有配置恢复时使用。
func (s *Service) SetWebhookSecret(secret string) {
	s.secretMu.Lock()
	s.secret = strings.TrimSpace(secret)
	s.secretMu.Unlock()
}

// newWebhookSecret 生成一个随机回调口令。
func newWebhookSecret() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// WebhookInfo 读 webhook 状态，供配置页显示「到底挂上没有」。
func (s *Service) WebhookInfo(ctx context.Context) (webhookInfo, error) {
	if s.client == nil {
		return webhookInfo{}, fmt.Errorf("tgbot: 未配置 Bot 客户端")
	}
	return s.client.WebhookInfo(ctx)
}

// push 主动推一条消息。
func (s *Service) push(ctx context.Context, chatID int64, text string) error {
	return s.pushOverride(ctx, chatID, text)
}

// Push 主动推一条消息，供装配层在异步任务完成后回推结果。
//
// Bot 侧只承诺「先回 task_id，完成后主动推」，这半句由装配层用轮询兑现 ——
// 站内没有「媒体整理完成」事件可订阅（internal/eventbus/events.go 里只有
// FileMutated / OfflineDownloadCompleted 等），这是既有事实，不在 T18 范围内改。
func (s *Service) Push(ctx context.Context, chatID int64, text string) error {
	return s.push(ctx, chatID, text)
}

// debugf / warnf 是带前缀的日志。
func (s *Service) debugf(format string, args ...any) {
	if s.log == nil {
		return
	}
	s.log.Debugf("tgbot: "+format, args...)
}

func (s *Service) warnf(format string, args ...any) {
	if s.log == nil {
		return
	}
	s.log.Warnf("tgbot: "+format, args...)
}

// reply 回一条消息。
func (s *Service) reply(ctx context.Context, chatID int64, text string) {
	if err := s.push(ctx, chatID, text); err != nil {
		s.warnf("回复失败 chat=%d: %v", chatID, err)
	}
}

// HandleUpdate 处理一次入站更新，返回是否被处理。
//
// 这就是 webhook 处理器的全部：解析 JSON、准入、路由、回话。
func (s *Service) HandleUpdate(ctx context.Context, u Update) bool {
	// 频道消息同样能触发（频道往订阅里转发分享链接的场景），但**编辑过的消息一律不跑**：
	// Telegram 改个错别字就重跑一次整理是灾难，而且重跑会再占一次网盘调用。
	msg := u.Message
	if msg == nil {
		msg = u.ChannelPost
	}
	if msg == nil {
		s.debugf("忽略编辑过的消息 update=%d：改个错别字不该重跑一次任务", u.UpdateID)
		return false
	}
	body := strings.TrimSpace(msg.body())
	if body == "" {
		return false
	}

	if decision := CheckAccess(s.cfg, *msg); !decision.Allowed {
		if decision.Anonymous || decision.Reason == "" {
			// 静默拒绝：群里白名单外的人发链接，一个字的回应都不给。
			s.debugf("忽略未授权消息 chat=%d user=%d", msg.Chat.ID, msg.From.ID)
			return false
		}
		s.reply(ctx, msg.Chat.ID, decision.Reason)
		return false
	}

	p, isCommand := parseText(body)
	if !isCommand {
		return s.handleBareLink(ctx, *msg, body)
	}

	spec, ok := s.registry.Lookup(p.Command)
	if !ok {
		s.reply(ctx, msg.Chat.ID, unknownCommandReply(p.Command))
		return false
	}

	actor := Actor{
		TGUserID:     msg.From.ID,
		TGChatID:     msg.Chat.ID,
		ChatType:     msg.Chat.Type,
		Username:     msg.From.Username,
		IsSuperAdmin: s.isSuperAdmin(ctx, msg),
	}
	if !checkTier(spec.Tier, actor) {
		s.reply(ctx, msg.Chat.ID, forbiddenReply(spec.Name))
		return false
	}
	if !spec.Supported || spec.Handler == nil {
		s.reply(ctx, msg.Chat.ID, notSupportedReply(spec.Name, spec.Summary))
		return false
	}

	s.debugf("执行命令 /%s tier=%s user=%s chat=%d", spec.Name, spec.Tier, actor.Username, msg.Chat.ID)
	text, err := spec.Handler(s, ctx, u, p.Args)
	if err != nil {
		s.reply(ctx, msg.Chat.ID, errorReply(err))
		return true
	}
	if text != "" {
		s.reply(ctx, msg.Chat.ID, text)
	}
	return true
}

// checkTier 判定权限档位。
//
// 超管档始终只有超管：Bot 里没有「谁创建了这个 Bot」的概念，
// 所以权限只能由站内 RBAC 注入，而不是由 TG 的身份推断。
func checkTier(t Tier, actor Actor) bool {
	switch t {
	case TierSuper:
		return actor.IsSuperAdmin
	default:
		return true
	}
}

// isSuperAdmin 读站内 RBAC 判定。默认没有注入任何判据时一律当普通用户 ——
// 「拿不到权限信息」不等于「是超管」。
func (s *Service) isSuperAdmin(ctx context.Context, msg *Message) bool {
	if s.superLookup == nil {
		return false
	}
	return s.superLookup(ctx, msg)
}

// SetSuperLookup 注入站内超管判定。
func (s *Service) SetSuperLookup(fn SuperLookup) { s.superLookup = fn }

// SuperLookup 由装配层注入：给定一条消息，判断发消息的人在站内是不是超管。
type SuperLookup func(ctx context.Context, msg *Message) bool

// handleBareLink 处理群里的裸链接与磁力。
//
// 只有「群在白名单 + 开了群链接开关 + 本人在用户白名单」三条同时成立才会走到这里，
// 这里再判一次是不是可转存的内容。
func (s *Service) handleBareLink(ctx context.Context, msg Message, body string) bool {
	link, ok := ExtractTransferLink(body)
	if !ok {
		return false
	}
	actor := Actor{TGUserID: msg.From.ID, TGChatID: msg.Chat.ID, ChatType: msg.Chat.Type}
	text, err := s.startLinkTransfer(ctx, actor, link)
	if err != nil {
		s.reply(ctx, msg.Chat.ID, errorReply(err))
		return true
	}
	s.reply(ctx, msg.Chat.ID, text)
	return true
}

// ExtractTransferLink 从一段文本里提取分享链接或磁力。
func ExtractTransferLink(body string) (string, bool) {
	for _, field := range strings.Fields(body) {
		field = strings.Trim(field, "，。！!？?、,.;；:：\"'()（）[]【】<>")
		if field == "" {
			continue
		}
		if strings.HasPrefix(field, "magnet:") {
			return field, true
		}
		if !strings.HasPrefix(field, "http://") && !strings.HasPrefix(field, "https://") {
			continue
		}
		// 只认看起来像分享的链接：网盘域名或带提取码参数的。
		if looksLikeShareLink(field) {
			return field, true
		}
	}
	return "", false
}

// knownShareHosts 是 litepan 已支持转存的网盘域名片段。
//
// 白名单而不是「任意 http 链接都转存」：Bot 在群里是个外部入口，
// 见到 URL 就去调网盘 API，等于给任何能发言的人一个无鉴权的下载器。
var knownShareHosts = []string{
	"115.com", "115cdn.com", "123pan.com", "123684.com", "123865.com",
	"139.com", "yun.139.com", "189.cn", "cloud.189.cn",
	"baidu.com", "pan.baidu.com", "quark.cn", "pan.quark.cn",
	"aliyundrive.com", "alipan.com", "xunlei.com",
}

// looksLikeShareLink 判断一个 URL 像不像网盘分享链接。
func looksLikeShareLink(raw string) bool {
	lower := strings.ToLower(raw)
	// 只看 host 部分：路径里的 query（提取码）不该影响域名判断。
	if i := strings.IndexByte(lower, '?'); i >= 0 {
		host := lower[:i]
		for _, h := range knownShareHosts {
			if strings.Contains(host, h) {
				return true
			}
		}
	}
	for _, h := range knownShareHosts {
		if strings.Contains(lower, h) {
			return true
		}
	}
	return false
}

// errorReply 把错误翻成人话。
func errorReply(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(err.Error(), "暂未支持") {
		return err.Error()
	}
	return "执行失败：" + err.Error()
}

// unknownCommandReply 是没注册的命令的回话。
func unknownCommandReply(name string) string {
	return fmt.Sprintf("不认识「/%s」。\n用 /help 看当前支持的命令。", name)
}

// forbiddenReply 是权限不足的回话。
//
// 刻意不区分「命令不存在」和「你不能用」：这两者在探测者眼里是一回事，
// 但在普通用户眼里必须分开 —— 他要知道是自己级别不够，而不是 Bot 坏了。
func forbiddenReply(name string) string {
	return fmt.Sprintf("「/%s」只有管理员可以使用。\n你的权限由站内角色决定，Telegram 这边不做独立配置。", name)
}

// Handler 返回 HTTP 处理器：Telegram webhook 的入口。
//
// 返回 200 是刻意的：Telegram 只看 HTTP 状态码，返回 5xx 会让它无限重投同一条更新。
// 「处理失败」必须体现在给用户的回话上，而不是体现在 HTTP 状态上。
// Handler 是 webhook 入口。
//
// secret 由调用方给出，但**它必须是服务端持有的期望值**，不是从请求里取的：
// 拿请求头本身当期望值等于「自己对自己比较」，任何人都能通过。
// 装配层传 s.WebhookSecret()（Bot 启动时生成、setWebhook 时一并登记给 Telegram）。
func (s *Service) Handler(secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.Enabled {
			// 未启用时 404：用户要能区分「Bot 没开」和「这个地址不存在」。
			http.NotFound(w, r)
			return
		}
		if secret == "" {
			// 没有期望值就等于不鉴权。这条路不能开：webhook 是公网入口，
			// 不鉴权的 Bot 等于把「转存我的网盘」开放给任何人。
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if got := strings.TrimSpace(r.Header.Get("X-Telegram-Bot-Api-Secret-Token")); got != secret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var u Update
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&u); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.HandleUpdate(r.Context(), u)
		w.WriteHeader(http.StatusOK)
	})
}

// actorOf 从一条 Update 里取出发起人。
func actorOf(u Update) Actor {
	msg := u.Message
	if msg == nil {
		msg = u.ChannelPost
	}
	if msg == nil {
		msg = u.EditedMessage
	}
	if msg == nil {
		return Actor{}
	}
	actor := Actor{TGUserID: msg.From.ID, TGChatID: msg.Chat.ID, ChatType: msg.Chat.Type, Username: msg.From.Username}
	return actor
}
