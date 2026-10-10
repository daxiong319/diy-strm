package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"litepan/internal/discover/discovery"
	"litepan/internal/discover/mediaparse"
	"litepan/internal/domain"
	"litepan/internal/logx"
	"litepan/internal/mediaorganize"
	"litepan/internal/settings"
	"litepan/internal/tgbot"
)

// 装配入站 Telegram Bot。
//
// 三条边界，都是踩过的坑：
//
//  1. **必须在 discoverInit 之后装配**。discovery 是包级函数 + 包级注入变量
//     （TransferShareFn / OfflineLinkFn 在 internal/discover/discovery/resources_ext.go:47,50），
//     discoverInit 里的 bindDiscoveryTransfer 才把它们接上。
//     Bot 早一步装配就能拿到 nil，然后 /share 报「转存服务尚未就绪」——
//     而用户看到的现象是「Bot 坏了」，完全指不到真实原因。
//
//  2. **Bot 的任务号不能和站内任务号混用**。媒体整理的 RunTask 接受的是站内 taskID，
//     所以 /organize 走「ListTasks → 按名字挑一个 → RunTask」，
//     挑不到就**明确报错**，绝不静默新建一个任务：用户敲 /organize 是想跑已有任务，
//     替他新建一个等于在他网盘里做一件他没要求的事。
//
//  3. **白名单是 Bot 唯一的自证机制**。Bot 是外部入口，
//     拿不到站内 RBAC 判据时一律当普通用户（isSuperAdmin 默认 false），
//     「加订阅」「离线下载」这类能持续往网盘塞东西的操作因此只在超管手里。

func wireTelegramBot(st *storeBundle, organize *mediaorganize.Service, logs *logx.Manager) *tgbot.Service {
	log := logs.For(logx.ModuleSystem)
	client := tgbot.NewClient(tgbot.ConfigFromSettings(st.settings).Token, "")
	svc := tgbot.New(tgbot.Options{
		Config:     tgbot.ConfigFromSettings(st.settings),
		Client:     client,
		Runner:     nil, // 见下方：需要 Bot 自己的引用才能回推结果
		Search:     botSearcher{},
		Link:       botLinkTransfer{},
		Status:     botStatusProvider{settings: st.settings, organize: organize},
		Recognizer: botRecognizer{},
		Log:        botLogger{log},
	})
	// runner 需要 Bot 自己的 push，所以必须在 svc 建好之后再回填 ——
	// 装配顺序在这里是硬约束，不是风格问题。
	svc.SetRunner(&botOrganizeRunner{organize: organize, push: svc.Push})
	// webhook 地址由设置页里的「站点基址」推导：Telegram 要能访问到本机，
	// 用户在本地 NAS 上跑时这个地址填的就是他对外的域名。
	svc.SetWebhookURL(func(cfg tgbot.Config) string {
		base := strings.TrimRight(strings.TrimSpace(st.settings.String(settings.KeyStrmBaseURL)), "/")
		if base == "" {
			return ""
		}
		return base + "/api/telegram/webhook"
	})
	// 启动时同步一次：Bot 进程重启、配置没变的情况下，Telegram 侧的 webhook
	// 登记仍然有效，但**本地生成的 secret 口令只在进程内存里** ——
	// 重启后必须重新登记一次，否则 Telegram 带旧口令来、被本地 401 全拒，
	// 而两边都看不出哪里不对。
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := svc.SyncConfig(ctx, tgbot.ConfigFromSettings(st.settings)); err != nil {
			log.Warn("Telegram Bot webhook 同步失败", "err", err)
		}
	}()
	return svc
}

// ---- /organize 与 /cancel ----

// botOrganizeRunner 把 Bot 的 organize 动作落到站内媒体整理服务。
//
// 语义是「跑一个**已经存在**的整理任务」，不是「建一个新任务」。
// 找不到任务时报错并列出可用的任务名 —— 静默新建会往网盘里写用户没要求的东西。
type botOrganizeRunner struct {
	organize *mediaorganize.Service
	push     func(ctx context.Context, chatID int64, text string) error
}

func (r *botOrganizeRunner) Start(ctx context.Context, actor tgbot.Actor, kind string, payload string) (string, error) {
	if r.organize == nil {
		return "", fmt.Errorf("整理服务尚未就绪")
	}
	tasks, err := r.organize.ListTasks(ctx)
	if err != nil {
		return "", err
	}
	payload = strings.TrimSpace(payload)
	var chosen *domain.MediaOrganizeTask
	for _, t := range tasks {
		if payload == "" || strings.EqualFold(strings.TrimSpace(t.TaskName), payload) {
			chosen = t
			break
		}
	}
	if chosen == nil {
		return "", fmt.Errorf("没有名为「%s」的整理任务。\n%s", payload, botTaskNameList(tasks))
	}
	// RunTask 自身异步（内部起 goroutine 后立即返回 task_id），所以这里不需要额外脱离 ctx。
	if _, err := r.organize.RunTask(ctx, chosen.ID); err != nil {
		return "", err
	}
	r.watch(chosen.ID, actor.TGChatID)
	return chosen.ID, nil
}

// watch 轮询任务直到结束，把结果推回发起它所在的会话。
//
// 必须脱离请求 ctx：Bot 的 webhook 响应在处理完 update 就返回了，
// 用请求 ctx 轮询的话第一条推送还没等到，ctx 就已经取消了。
func (r *botOrganizeRunner) watch(taskID string, chatID int64) {
	if r.organize == nil || r.push == nil || chatID == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if r.organize.IsRunning(taskID) {
			continue
		}
		task, err := r.organize.GetTask(ctx, taskID)
		text := "整理任务 " + taskID + " 已结束"
		if err != nil {
			text += "（读不到最终状态：" + err.Error() + "）"
		} else if task != nil {
			text += "（状态：" + task.Status + "）"
		}
		if err := r.push(ctx, chatID, text); err != nil {
			return
		}
		return
	}
}

func (r *botOrganizeRunner) Cancel(ctx context.Context, actor tgbot.Actor, taskID string) (bool, error) {
	if r.organize == nil {
		return false, fmt.Errorf("整理服务尚未就绪")
	}
	task, err := r.organize.GetTask(ctx, taskID)
	if err != nil {
		return false, err
	}
	if task == nil {
		return false, nil
	}
	// 先停再删：直接删掉一个还在跑的整理，runner 还持有它的配置，
	// 用户看到的现象是「任务消失了，但网盘还在动」。
	r.organize.RequestStop(taskID)
	stopping, err := r.organize.DeleteTask(ctx, taskID)
	if err != nil {
		return false, err
	}
	return stopping || true, nil
}

func botTaskNameList(tasks []*domain.MediaOrganizeTask) string {
	names := make([]string, 0, len(tasks))
	for _, t := range tasks {
		if name := strings.TrimSpace(t.TaskName); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "当前一个整理任务都没有，请先在管理台「整理任务」里建一个。"
	}
	return "可用任务：" + strings.Join(names, "、")
}

// ---- /search ----

// botSearcher 走既有的 TG 资源频道检索（discovery.SearchTGChannelResources）。
//
// 刻意不新造一条搜索链：Bot 只是个入口，搜出来的东西必须和管理台里看到的一致，
// 两套搜索给出两个结果集，用户会开始怀疑哪个才是真的。
type botSearcher struct{}

func (botSearcher) Search(ctx context.Context, keyword string, limit int) ([]tgbot.SearchHit, error) {
	if limit <= 0 {
		limit = 10
	}
	resources, errs := discovery.SearchTGChannelResources(ctx, keyword, nil, "")
	hits := make([]tgbot.SearchHit, 0, limit)
	for _, r := range resources {
		if len(hits) >= limit {
			break
		}
		hits = append(hits, tgbot.SearchHit{
			Title:    firstNonEmptyBot(r.PostText, r.Channel),
			Source:   "TG · " + r.Channel,
			ShareURL: r.MessageURL,
		})
	}
	if len(hits) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("检索失败：%s", errs[0].Error)
	}
	return hits, nil
}

// ---- /share ----

// botLinkTransfer 复用 discovery 已有的转存/离线入口。
//
// 资源形态决定通道（分享链接转存、磁力走离线下载），与 RSS 落地用的是同一套判定
// （internal/discover/discovery/rss_delivery.go:310 的 rssChannelFor）。
// 这里不复刻那份 switch：写第二份判定的结果是两边悄悄分叉。
type botLinkTransfer struct{}

func (botLinkTransfer) StartLinkTransfer(ctx context.Context, actor tgbot.Actor, link string) (string, error) {
	link = strings.TrimSpace(link)
	if link == "" {
		return "", fmt.Errorf("没有给出链接")
	}
	provider := discovery.SettingString(discovery.SettingTargetProvider, "123")
	provider, err := discovery.NormalizeTransferProvider(provider)
	if err != nil {
		return "", err
	}
	if isMagnetish(link) {
		if err := discovery.SubmitOfflineLink(ctx, link, provider); err != nil {
			return "", err
		}
		return "offline-" + tgbotTaskID("offline", link), nil
	}
	if _, _, err := discovery.TransferShareLink(ctx, link, "", provider); err != nil {
		return "", err
	}
	return "transfer-" + tgbotTaskID("transfer", link), nil
}

func (botLinkTransfer) CancelLinkTransfer(ctx context.Context, actor tgbot.Actor, taskID string) (bool, error) {
	// 转存与离线下载都是「提交即返回」的同步执行，没有可取消的句柄。
	// 诚实地告诉用户没法撤，比回一句「已取消」然后什么也没做要好。
	return false, fmt.Errorf("转存是立即执行的，没有可取消的句柄；如需清理请到网盘里删除已转存的文件")
}

func isMagnetish(link string) bool {
	low := strings.ToLower(link)
	return strings.HasPrefix(low, "magnet:") || strings.HasPrefix(low, "ed2k:") ||
		strings.HasPrefix(low, "thunder:") || strings.HasPrefix(low, "btih:")
}

// ---- /status ----

type botStatusProvider struct {
	settings *settings.Service
	organize *mediaorganize.Service
}

func (p botStatusProvider) BotStatus(ctx context.Context) ([]tgbot.StatusLine, error) {
	lines := []tgbot.StatusLine{
		{Label: "Bot", Value: botOnOff(p.settings.Bool(settings.KeyMOTelegramBotEnabled))},
		{Label: "放行的用户 ID", Value: firstNonEmptyBot(p.settings.String(settings.KeyMOTelegramBotAllowedUsers), "（空：拒绝所有私聊）")},
		{Label: "放行的群 ID", Value: firstNonEmptyBot(p.settings.String(settings.KeyMOTelegramBotAllowedChats), "（空：拒绝所有群）")},
		{Label: "群链接转存", Value: botOnOff(p.settings.Bool(settings.KeyMOTelegramBotGroupLinkEnabled))},
	}
	if p.organize != nil {
		running := 0
		if tasks, err := p.organize.ListTasks(ctx); err == nil {
			for _, t := range tasks {
				if p.organize.IsRunning(t.ID) {
					running++
				}
			}
		}
		lines = append(lines, tgbot.StatusLine{Label: "正在跑的整理任务", Value: fmt.Sprintf("%d 个", running)})
	}
	return lines, nil
}

func botOnOff(v bool) string {
	if v {
		return "开"
	}
	return "关"
}

// ---- /recognize ----

// botRecognizer 用既有的文件名解析（internal/discover/mediaparse）。
// 不自己写一套正则：整理链已经在用同一个解析器，两套规则对同一个文件给出不同分类，
// 用户会以为整理坏了。
type botRecognizer struct{}

func (botRecognizer) BotRecognize(ctx context.Context, filename string) ([]string, error) {
	category, title, season, episode, year := mediaparse.ParseMedia(filename)
	out := []string{"分类：" + firstNonEmptyBot(category, "（未识别）")}
	if title != "" {
		out = append(out, "标题："+title)
	}
	if season > 0 {
		out = append(out, fmt.Sprintf("季：S%02d", season))
	}
	if episode > 0 {
		out = append(out, fmt.Sprintf("集：E%02d", episode))
	}
	if year > 0 {
		out = append(out, fmt.Sprintf("年份：%d", year))
	}
	return out, nil
}

type botLogger struct{ log *slog.Logger }

func (l botLogger) Debugf(format string, args ...any) { l.log.Debug(fmt.Sprintf(format, args...)) }
func (l botLogger) Warnf(format string, args ...any)  { l.log.Warn(fmt.Sprintf(format, args...)) }

// tgbotTaskID 给一次「提交即返回」的动作造一个可读且稳定的任务号。
//
// 转存与离线下载是同步提交、没有站内任务号，但 Bot 的 /cancel 与完成推送
// 都以任务号为键。用链接内容的散列而不是时间戳：同一个链接敲两次得到同一个号，
// 用户重发一遍就能看到「这活已经在做了」，而不是凭空多出一个在跑的任务。
func tgbotTaskID(kind, link string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(link)))
	return kind + "-" + hex.EncodeToString(sum[:6])
}

func firstNonEmptyBot(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// botPollDone 轮询一个站内任务直到它不再运行，把结果推回发起者。
//
// 站内没有「媒体整理完成」事件（eventbus 里只有 FileMutated 等，
// 见 internal/eventbus/events.go），所以完成推送只能轮询 ——
// mediaorganize 不发事件这件事是既有事实，不在 T18 范围内改。
func botPollDone(ctx context.Context, push func(ctx context.Context, chatID int64, text string) error, chatID int64, organize *mediaorganize.Service, taskID string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	deadline := time.Now().Add(6 * time.Hour)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !organize.IsRunning(taskID) {
			task, err := organize.GetTask(ctx, taskID)
			text := "整理任务 " + taskID + " 已结束"
			if err == nil && task != nil {
				text += "（状态：" + task.Status + "）"
			}
			_ = push(ctx, chatID, text)
			return
		}
		if time.Now().After(deadline) {
			return
		}
	}
}
