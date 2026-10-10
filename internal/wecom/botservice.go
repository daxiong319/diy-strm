package wecom

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"litepan/internal/inboundbot"
	"litepan/internal/settings"
)

// 企微智能机器人入站。
//
// ## 接入方式
//
// 企微智能机器人的 API 模式只有两种接收方式、互斥二选一：
// Webhook 短连接（需公网 URL、需加解密）与 WebSocket 长连接。
// 这里实现的是 **Webhook 模式**：
//
//   - GET  ：企微拿 msg_signature/timestamp/nonce/echostr 来校验 URL 配没配对，
//            校验通过后要把 echostr **解密后的明文原样返回**（不能加引号、换行）。
//   - POST ：body 是 {"encrypt":"…"}，解密后拿到消息。
//
// 长连接模式（wss://openws.work.weixin.qq.com）官方文档可查但需要常驻连接，
// 与仓库其它地方「NAS 场景不占长连接」的判断一致，本期不做。
//
// ## 加解密
//
// 密文解不开就什么都做不了，而这一层没有任何现成实现可抄，所以单独成文件并带自测。

// BotConfig 是机器人入站的运行配置。
type BotConfig struct {
	// Enabled 总开关。关掉时回调端点直接 404。
	Enabled bool
	// Token 是自建应用的 Secret，用于算签名。
	Token string
	// AESKey 是 EncodingAESKey，用于加解密。
	AESKey string
	// AllowedChats 是允许使用的群 id 逗号分隔。
	AllowedChats []int64
	// AllowedUsers 是允许使用的成员 id 逗号分隔（明文 userid）。
	AllowedUsers []int64
	// GroupLinkEnabled 控制群里的裸链接/磁力是否触发转存。
	GroupLinkEnabled bool
	// CorpID/AgentID 是自建应用信息，主动推送回复时用得上。
	CorpID  string
	AgentID string
}

// BotConfigFromSettings 读出机器人配置。
func BotConfigFromSettings(s *settings.Service) BotConfig {
	if s == nil {
		return BotConfig{}
	}
	return BotConfig{
		Enabled:          s.Bool(settings.KeyMOWecomBotEnabled),
		Token:            strings.TrimSpace(s.StringAllowEmpty(settings.KeyMOWecomBotToken)),
		AESKey:           strings.TrimSpace(s.StringAllowEmpty(settings.KeyMOWecomBotAESKey)),
		AllowedChats:     ParseIDList(s.StringAllowEmpty(settings.KeyMOWecomBotAllowedChats)),
		AllowedUsers:     ParseIDList(s.StringAllowEmpty(settings.KeyMOWecomBotAllowedUsers)),
		GroupLinkEnabled: s.Bool(settings.KeyMOWecomBotGroupLinkEnabled),
		CorpID:           strings.TrimSpace(s.StringAllowEmpty(settings.KeyMOWecomBotCorpID)),
		AgentID:          strings.TrimSpace(s.StringAllowEmpty(settings.KeyMOWecomBotAgentID)),
	}
}

// Ready 报告这份配置能不能真正接收消息。
//
// 与 Telegram 一样：空白名单 = 拒绝一切，不是「不限制」。
// 群里发个链接就能动用你的 115 账号，这个代价不能靠「默认开着」来试。
func (c BotConfig) Ready() bool {
	return c.Enabled && c.Token != "" && c.AESKey != "" &&
		(len(c.AllowedChats) > 0 || len(c.AllowedUsers) > 0)
}

// ParseIDList 解析逗号分隔的 ID 列表（群 id 是负数，按 int64 解析）。
func ParseIDList(raw string) []int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]int64, 0, len(parts))
	seen := map[int64]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// FormatIDList 把 ID 列表拼回设置值的写法。
func FormatIDList(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, ",")
}

// AccessDecision 是一次入站消息的准入裁决。
type AccessDecision struct {
	Allowed   bool
	Reason    string
	Anonymous bool
}

// Allow 返回放行。
func Allow() AccessDecision { return AccessDecision{Allowed: true} }

// Deny 返回一个会回话的拒绝。
func Deny(reason string) AccessDecision { return AccessDecision{Allowed: false, Reason: reason} }

// DenyQuietly 返回静默拒绝：不回复，也不留下痕迹。
func DenyQuietly() AccessDecision { return AccessDecision{Allowed: false, Anonymous: true} }

// CheckAccess 判定一条企微消息能不能被处理。
//
// 权限模型照抄 muvyo：显式白名单（配置里列出的群 id / 成员 id），
// 私聊与群聊分开 —— 私聊按 from.userid，**群聊按 chatid**。
//
// 群聊按 chatid 而不是 userid 有一个务实的理由：企微在「机器人创建者不是超管」的
// 企业里给出的 from.userid 是**加密的 open_userid**，照抄原值没人能配出白名单。
// 按 chatid 判则完全可配 —— 用户能在群里看到自己的群 id。
//
// 代价是群里的每个人都能用（只要群在白名单里）。这是「加群即授权」的取舍，
// 与 Telegram 侧「群命令放行、群链接要额外开关」一致：
// 允许群发命令 ≠ 允许群里发链接就转存，后者额外要 GroupLinkEnabled。
func CheckAccess(cfg BotConfig, msg CallbackMessage) AccessDecision {
	if !cfg.Enabled {
		return Deny("机器人未启用，请先在管理台打开企业微信机器人。")
	}
	if cfg.Token == "" || cfg.AESKey == "" {
		return Deny("企业微信机器人的 Secret 或 EncodingAESKey 未配置。")
	}

	if msg.IsGroup() {
		chatID, ok := msg.ChatIDInt64()
		// 读不出 chatid 就按「不是白名单里的群」处理，绝不猜。
		if !ok || !containsID(cfg.AllowedChats, chatID) {
			return DenyQuietly()
		}
		if !isCommandText(msg.Text()) {
			// 群里的裸链接/磁力：更严格的开关 + 人也要在白名单里。
			if !cfg.GroupLinkEnabled {
				return DenyQuietly()
			}
			if uid, ok := msg.UserIDInt64(); ok && !containsID(cfg.AllowedUsers, uid) {
				return DenyQuietly()
			}
		}
		return Allow()
	}

	// 单聊：必须是人，且人得在白名单里。
	uid, ok := msg.UserIDInt64()
	if !ok || uid == 0 {
		return Deny("无法识别发消息的人。")
	}
	if len(cfg.AllowedUsers) == 0 {
		return Deny("未配置允许使用的成员 ID 白名单，当前所有单聊都被拒绝。")
	}
	if containsID(cfg.AllowedUsers, uid) {
		return Allow()
	}
	return DenyQuietly()
}

// isCommandText 判断一条文本是不是「命令」而不是裸链接。
//
// 必须容忍前导的 @提及：群里触发命令的标准写法就是「@小助手 /organize」，
// 而这个入口要决定「放行命令」还是「放行裸链接」。
// 只认首个字符是 '/' 的话，群里所有 @机器人的命令都会被当成裸链接处理，
// 于是在「群链接」开关关着时被静默丢掉 —— 用户看到的是「@了也没反应」。
func isCommandText(text string) bool {
	for _, f := range strings.Fields(text) {
		if f == "@" || (strings.HasPrefix(f, "@") && !strings.HasPrefix(f, "/")) {
			continue
		}
		return strings.HasPrefix(f, "/")
	}
	return false
}

func containsID(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// Bot 是企微入站服务。
type Bot struct {
	cfg      BotConfig
	deps     *inboundbot.Deps
	registry *inboundbot.Registry
	platform string
	log      Logger

	// respondOverride 只在测试里替换，用于断言「回复内容」。
	respondOverride func(ctx context.Context, reply ReplyTarget, text string) error

	// pushOverride 只在测试里替换，用于断言「异步完成后主动推」。
	pushOverride func(ctx context.Context, target ReplyTarget, text string) error

	// superLookup 由装配层注入：给定一个成员 id，判断他在站内是不是超管。
	superLookup func(ctx context.Context, userID int64) bool

	now func() time.Time
	mu  sync.RWMutex
}

// ReplyTarget 描述一条回复该发给谁。
type ReplyTarget struct {
	// ChatID 非空表示群聊；为空表示单聊，用 UserID。
	ChatID int64
	UserID int64
	Group  bool
	// ResponseURL 是本次回调带来的主动回复地址（一次性、1 小时有效）。
	ResponseURL    string
	ResponseCode   string
	RequestToken   string
	RequestNonce   string
	RequestTimeStr string
}

// BotOptions 是装配选项。
type BotOptions struct {
	Config BotConfig
	// Title 是 /help 抬头用的平台名，默认「企业微信机器人」。
	//
	// 抬头必须由平台给：共用层里硬编码 Telegram 的话，
	// 企微用户的 /help 会写着「Telegram Bot 命令」。
	Title  string
	Deps   *inboundbot.Deps
	Logger Logger
	Now    func() time.Time
	// RegisterCommands 登记命令表；为空时用 Telegram 侧同一份元数据。
	RegisterCommands func(r *inboundbot.Registry)
}

// NewBot 建企微入站服务。
func NewBot(opts BotOptions) *Bot {
	reg := inboundbot.NewRegistry()
	register := opts.RegisterCommands
	if register == nil {
		register = defaultCommands
	}
	register(reg)
	if opts.Deps == nil {
		opts.Deps = &inboundbot.Deps{}
	}
	opts.Deps.Registry = reg
	if opts.Deps.Title == "" {
		opts.Deps.Title = strings.TrimSpace(opts.Title)
	}
	if opts.Deps.Title == "" {
		opts.Deps.Title = "企业微信机器人"
	}
	if opts.Now != nil {
		opts.Deps.Now = opts.Now
	}
	if opts.Deps.Tasks == nil {
		opts.Deps.Tasks = inboundbot.NewTaskStore()
	}
	b := &Bot{
		cfg:      opts.Config,
		deps:     opts.Deps,
		registry: reg,
		platform: "企业微信机器人",
		log:      opts.Logger,
		now:      opts.Now,
	}
	if b.now == nil {
		b.now = time.Now
	}
	return b
}

// defaultCommands 登记命令表。
//
// 命令元数据与 Telegram 侧**共用同一份**（RegisterCommandSpecs）：
// 两个平台的命令名、档位、用法一字不差，因为用户是同一批人。
// 私有的那条 /setprivacy 不挂：那是 Telegram 的 BotFather 概念，企微没有。
func defaultCommands(r *inboundbot.Registry) {
	for _, spec := range inboundbot.SharedCommandSpecs() {
		if spec.Name == "setprivacy" {
			continue
		}
		r.Register(spec)
	}
}

// Config 暴露当前配置。
func (b *Bot) Config() BotConfig {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.cfg
}

// Registry 暴露命令表。
func (b *Bot) Registry() *inboundbot.Registry { return b.registry }

// Deps 暴露业务能力集合，供装配层回填 runner 等晚到的依赖。
func (b *Bot) Deps() *inboundbot.Deps { return b.deps }

// SetSuperLookup 注入站内超管判定。
func (b *Bot) SetSuperLookup(fn func(ctx context.Context, userID int64) bool) { b.superLookup = fn }

// SyncConfig 换一份配置。
func (b *Bot) SyncConfig(cfg BotConfig) {
	b.mu.Lock()
	b.cfg = cfg
	b.mu.Unlock()
}

func (b *Bot) debugf(format string, args ...any) {
	if b.log == nil {
		return
	}
	b.log.Debugf("wecombot: "+format, args...)
}

func (b *Bot) warnf(format string, args ...any) {
	if b.log == nil {
		return
	}
	b.log.Warnf("wecombot: "+format, args...)
}

func (b *Bot) isSuperAdmin(ctx context.Context, userID int64) bool {
	// 拿不到权限信息 ≠ 是超管，所以默认 false。
	if b.superLookup == nil || userID == 0 {
		return false
	}
	return b.superLookup(ctx, userID)
}

// ---- HTTP 入口 ----

// Handler 返回企微回调的 HTTP 处理器。
//
// URL 上的校验（GET）与消息处理（POST）都在这里；两条路都必须先过签名：
// 这是**公网**入口，不鉴权等于把「转存我的网盘」开放给任何能访问这个地址的人。
//
// POST 恒返回 200：企微在回调失败时会重投，而 5xx 会让它无限重投同一条消息，
// 于是用户收到一串重复的「出错了」。
func (b *Bot) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := b.Config()
		if !cfg.Enabled {
			http.NotFound(w, r)
			return
		}
		if !b.verifySignature(cfg, r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			b.handleVerify(w, r, cfg)
		case http.MethodPost:
			b.handleMessage(w, r, cfg)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

// verifySignature 比对 URL 上的签名。
func (b *Bot) verifySignature(cfg BotConfig, r *http.Request) bool {
	q := r.URL.Query()
	// 配回调地址的那次 GET 带的是 echostr，收到消息的 POST 带的是 encrypt，
	// 两个字段都要参与验签 —— 少了任何一个，配地址那一步就会一直失败，
	// 而企微只会回一句「签名错误」，看不出是取错了字段。
	signed := q.Get("encrypt")
	if signed == "" {
		signed = q.Get("echostr")
	}
	// 签名是拿 **URL 里的** 密文算的，不是解密后的内容。
	// 用解密结果去算会得到完全不同的值，且永远不会匹配。
	if !VerifySignature(cfg.Token, q.Get("timestamp"), q.Get("nonce"), signed, q.Get("msg_signature")) {
		b.debugf("回调签名不匹配 path=%s", r.URL.Path)
		return false
	}
	return true
}

// handleVerify 处理企微配置回调 URL 时的 GET 探测。
//
// 官方要求：校验通过后**1 秒内把 echostr 解密后的明文原样返回**，
// 不加引号、不加 BOM、不加换行。加任何一样，企微都会判「校验失败」，
// 而用户看到的是「填了地址但企微不让保存」，根本猜不到是多了个换行。
func (b *Bot) handleVerify(w http.ResponseWriter, r *http.Request, cfg BotConfig) {
	echostr := r.URL.Query().Get("echostr")
	if echostr == "" {
		// 有些模式下 GET 带的是 encrypt 而不是 echostr。
		echostr = r.URL.Query().Get("encrypt")
	}
	plain, err := DecryptMessage(cfg.AESKey, echostr)
	if err != nil {
		b.warnf("解密 echostr 失败: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(plain)
}

// handleMessage 处理一条回调消息。
func (b *Bot) handleMessage(w http.ResponseWriter, r *http.Request, cfg BotConfig) {
	q := r.URL.Query()
	encrypted := q.Get("encrypt")
	if encrypted == "" {
		// 也有企业把密文放在 body 的 encrypt 字段里。
		var body struct {
			Encrypt string `json:"encrypt"`
		}
		if err := decodeBody(r, &body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		encrypted = body.Encrypt
	}
	plain, err := DecryptMessage(cfg.AESKey, encrypted)
	if err != nil {
		b.warnf("解密回调消息失败: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var msg CallbackMessage
	if err := unmarshal(plain, &msg); err != nil {
		b.warnf("解析回调消息失败: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	target := ReplyTarget{
		ResponseURL:    msg.ResponseURL,
		ResponseCode:   msg.ResponseCode,
		RequestToken:   cfg.Token,
		RequestNonce:   q.Get("nonce"),
		RequestTimeStr: q.Get("timestamp"),
	}
	if msg.IsGroup() {
		if chatID, ok := msg.ChatIDInt64(); ok {
			target.ChatID, target.Group = chatID, true
		}
	} else if uid, ok := msg.UserIDInt64(); ok {
		target.UserID = uid
	}

	// 进入会话事件先回欢迎语：企微文档说欢迎语是 5 秒窗口，
	// 过了这条事件不会重投。回一句「已连接」比什么都不回要好判断。
	if msg.IsEnterChat() {
		// 欢迎语必须是**命令列表**，不是 /help 的用法页：用户第一次和机器人
		// 说话时想知道的是「能干什么」，不是「/help 怎么用」。
		// 企微对这条事件只给 5 秒窗口且不重投，所以只能在这里给一次完整清单。
		welcome, err := inboundbot.HandleHelp(b.deps, r.Context(), inboundbot.Actor{}, nil)
		if err != nil {
			welcome = "已连接。发送 /help 查看可用命令。"
		}
		b.respond(r.Context(), target, welcome)
		w.WriteHeader(http.StatusOK)
		return
	}

	// ⚠️ 准入判定必须在**任何副作用之前**，包括解密后的命令解析。
	// 群里白名单外的人发一条 /organize，绝不能起任务。
	if decision := CheckAccess(cfg, msg); !decision.Allowed {
		if decision.Anonymous || decision.Reason == "" {
			b.debugf("忽略未授权消息 chatid=%s", msg.ChatID)
		} else {
			b.respond(r.Context(), target, decision.Reason)
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	b.dispatch(r.Context(), target, msg)
	w.WriteHeader(http.StatusOK)
}

// dispatch 路由一条已放行的消息。
func (b *Bot) dispatch(ctx context.Context, target ReplyTarget, msg CallbackMessage) {
	text := msg.Text()
	if text == "" {
		return
	}
	uid, _ := msg.UserIDInt64()
	actor := inboundbot.Actor{
		UserID:       uid,
		ChatID:       target.ChatID,
		ChatType:     chatTypeOf(msg),
		IsSuperAdmin: b.isSuperAdmin(ctx, uid),
	}

	// 用 isCommandText 而不是 text[0]=='/'：群里触发命令的标准写法是
	// 「@小助手 /organize」，只认首字符 '/' 的话，群里所有命令都会
	// 被当成裸链接去走转存分支。
	if !isCommandText(text) {
		b.handleBareLink(ctx, target, actor, text)
		return
	}
	name, args := splitCommand(text)
	spec, ok := b.registry.Lookup(name)
	if !ok {
		b.respond(ctx, target, inboundbot.UnknownCommandReply(name))
		return
	}
	if !inboundbot.CheckTier(spec.Tier, actor) {
		b.respond(ctx, target, inboundbot.ForbiddenReply(spec.Name))
		return
	}
	if !spec.Supported || spec.Handler == nil {
		b.respond(ctx, target, inboundbot.NotSupportedReply(spec.Name, spec.Summary))
		return
	}
	b.debugf("执行命令 /%s tier=%s chat=%d", spec.Name, spec.Tier, target.ChatID)
	reply, err := spec.Handler(b.deps, ctx, actor, args)
	if err != nil {
		b.respond(ctx, target, inboundbot.ErrorReply(err))
		return
	}
	if reply != "" {
		b.respond(ctx, target, reply)
	}
}

func chatTypeOf(msg CallbackMessage) string {
	if msg.IsGroup() {
		return "group"
	}
	return "private"
}

// handleBareLink 处理群里的裸链接与磁力。
func (b *Bot) handleBareLink(ctx context.Context, target ReplyTarget, actor inboundbot.Actor, text string) {
	link, ok := inboundbot.ExtractTransferLink(text)
	if !ok {
		return
	}
	if b.deps.Link == nil {
		b.respond(ctx, target, inboundbot.ErrorReply(errLinkNotWired))
		return
	}
	taskID, err := b.deps.Link.StartLinkTransfer(ctx, actor, link)
	if err != nil {
		b.respond(ctx, target, inboundbot.ErrorReply(err))
		return
	}
	b.remember(taskID, "link", actor)
	b.respond(ctx, target, fmt.Sprintf("已开始转存，任务号 %s\n完成后我会把结果发给你；中途要停用 /cancel %s", taskID, taskID))
}

func (b *Bot) remember(taskID, kind string, actor inboundbot.Actor) {
	if strings.TrimSpace(taskID) == "" || b.deps.Tasks == nil {
		return
	}
	b.deps.Tasks.Add(&inboundbot.Job{ID: taskID, Kind: kind, Actor: actor, CreatedAt: b.now()})
}

// splitCommand 切分一条命令文本。
//
// 群里 @机器人 是**独立的一段**（企微渲染成「@名字」），所以
// 「@小助手 /organize 国产剧」和「/organize @小助手 国产剧」都要认。
//
// 不能只取第一段当命令名：第一段是 @提及时会落进「不认识的命令」，
// 而这正是群里发命令的标准写法 —— 用户会以为机器人坏了。
func splitCommand(text string) (string, []string) {
	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "/")))
	// 先剥掉纯 @提及段（@name 与裸 @ 都算）。
	cleaned := make([]string, 0, len(fields))
	for _, f := range fields {
		if f == "@" || (strings.HasPrefix(f, "@") && !strings.HasPrefix(f, "/")) {
			continue
		}
		cleaned = append(cleaned, strings.TrimPrefix(f, "/"))
	}
	if len(cleaned) == 0 {
		return "", nil
	}
	name := cleaned[0]
	// 命令名里还可能带 @bot 后缀（有些客户端拼成「/organize@bot」）。
	if i := strings.IndexByte(name, '@'); i >= 0 {
		name = name[:i]
	}
	return name, cleaned[1:]
}

// ---- 回复 ----

// respond 回一条消息。
func (b *Bot) respond(ctx context.Context, target ReplyTarget, text string) {
	if text == "" {
		return
	}
	if b.respondOverride != nil {
		if err := b.respondOverride(ctx, target, text); err != nil {
			b.warnf("回复失败: %v", err)
		}
		return
	}
	if target.ResponseURL == "" {
		b.warnf("没有 response_url，只能放弃这条回复（企微被动回包只在本轮有效）")
		return
	}
	if err := PostResponse(ctx, target, text); err != nil {
		b.warnf("主动回复失败: %v", err)
	}
}

// Push 主动推一条消息，供装配层在异步任务完成后回推结果。
func (b *Bot) Push(ctx context.Context, target ReplyTarget, text string) error {
	if b.pushOverride != nil {
		return b.pushOverride(ctx, target, text)
	}
	return b.respond0(ctx, target, text)
}

func (b *Bot) respond0(ctx context.Context, target ReplyTarget, text string) error {
	if text == "" {
		return nil
	}
	if b.respondOverride != nil {
		return b.respondOverride(ctx, target, text)
	}
	if target.ResponseURL == "" {
		return fmt.Errorf("wecombot: 缺少 response_url，无法回复")
	}
	return PostResponse(ctx, target, text)
}

// PostResponse 通过回调带来的 response_url 发一条消息。
//
// 这个地址**不需要 access_token**：它是回调里一次性下发的凭证。
// 每个只能用一次、有效期 1 小时，所以重试时要重新等下一次回调。
func PostResponse(ctx context.Context, target ReplyTarget, text string) error {
	body, err := encodeJSON(newMarkdownReply(text))
	if err != nil {
		return err
	}
	endpoint := target.ResponseURL
	if target.ResponseCode != "" {
		if strings.Contains(endpoint, "?") {
			endpoint += "&response_code=" + target.ResponseCode
		} else {
			endpoint += "?response_code=" + target.ResponseCode
		}
	}
	req, err := newPostRequest(ctx, endpoint, body)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := readBody(resp)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wecombot: 主动回复返回 %d: %s", resp.StatusCode, truncateText(string(raw), 300))
	}
	return parseAPIError(raw, "aibot/response")
}
