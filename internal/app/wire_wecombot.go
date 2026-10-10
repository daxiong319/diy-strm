package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"litepan/internal/inboundbot"
	"litepan/internal/logx"
	"litepan/internal/mediaorganize"
	"litepan/internal/wecom"
)

// 装配入站企业微信智能机器人。
//
// 与 Telegram Bot 的关系：**命令表、十个业务 handler、任务归属表全部共用**
// （internal/inboundbot），这里只写企微特有的三件事 ——
//
//  1. 消息怎么进来（msg_signature 验签 + AES 解密）；
//  2. Actor 怎么构造（群按 chatid、单聊按 userid）；
//  3. 回复怎么发（一次性 response_url，不经 access_token）。
//
// 抽共用层而不是复制 handler：复制出来的第二份 /strm 分类校验
// 必然和第一份分叉，而分叉的那一处只有用户报障时才会被发现。
//
// 为什么在 discoverInit 之后装配：与 wire_telegramBot 同理 ——
// discovery 是包级函数 + 包级注入变量，提前装配只能拿到 nil。
// 但这里用的是命令被调用时读，与 wire_telegramBot 一样不需要严格顺序，
// 放在同一处是为了让「两个入站 Bot 的装配边界写在同一个地方」。
func wireWeComBot(st *storeBundle, organize *mediaorganize.Service, logs *logx.Manager) *wecom.Bot {
	log := logs.For(logx.ModuleSystem)
	bot := wecom.NewBot(wecom.BotOptions{
		Config: wecom.BotConfigFromSettings(st.settings),
		Title:  "企业微信机器人",
		Logger: botLogger{log},
		Now:    time.Now,
	})
	deps := bot.Deps()
	deps.Search = botSearcher{}
	deps.Link = botLinkTransfer{}
	deps.Status = botStatusProvider{settings: st.settings, organize: organize}
	deps.Recognizer = botRecognizer{}
	// 这里的三个能力（/strm /sub /duplicate）仓内没有对应的 Bot 适配，
	// 与 Telegram 侧同样留 nil —— 共用层会回「尚未接入」而不是崩。
	// 刻意不用假的空实现糊过去：/strm 回「成功」而实际什么都没做，
	// 比回「尚未接入」糟得多。
	deps.Runner = &weComOrganizeRunner{organize: organize, tasks: deps.Tasks, push: bot.Push}
	bot.SetSuperLookup(weComSuperLookup(st))
	return bot
}

// weComOrganizeRunner 是企微侧的整理任务执行器。
//
// 与 Telegram 侧共用同一套语义（跑一个**已存在**的整理任务，找不到就报错），
// 但推送目标不同：企微的主动回复地址**一次性且 1 小时有效**，
// 所以不能像 Telegram 那样把 chatID 留到 6 小时后 —— 那时地址早失效了。
// 这里改为：任务结束时的推送走「尽可能用当轮的 response_url，
// 用不了就明确告诉用户去管理台看」。
type weComOrganizeRunner struct {
	organize *mediaorganize.Service
	tasks    *inboundbot.TaskStore
	push     func(ctx context.Context, target wecom.ReplyTarget, text string) error
}

func (r *weComOrganizeRunner) Start(ctx context.Context, actor inboundbot.Actor, kind, payload string) (string, error) {
	if r.organize == nil {
		return "", fmt.Errorf("整理服务尚未就绪")
	}
	tasks, err := r.organize.ListTasks(ctx)
	if err != nil {
		return "", err
	}
	payload = strings.TrimSpace(payload)
	for _, t := range tasks {
		if payload == "" || strings.EqualFold(strings.TrimSpace(t.TaskName), payload) {
			if _, err := r.organize.RunTask(ctx, t.ID); err != nil {
				return "", err
			}
			return t.ID, nil
		}
	}
	return "", fmt.Errorf("没有名为「%s」的整理任务。\n%s", payload, botTaskNameList(tasks))
}

func (r *weComOrganizeRunner) Cancel(ctx context.Context, actor inboundbot.Actor, taskID string) (bool, error) {
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
	// 先停再删：直接删掉还在跑的整理，runner 还持有它的配置，
	// 用户看到的现象是「任务消失了，但网盘还在动」。
	r.organize.RequestStop(taskID)
	stopping, err := r.organize.DeleteTask(ctx, taskID)
	if err != nil {
		return false, err
	}
	return stopping || true, nil
}

// weComSuperLookup 判定某个企微成员在站内是不是超管。
//
// 拿不到映射时返回 false：「查不到」不等于「是超管」——
// 企微身份与站内账号之间没有系统性的对应关系，
// 这里只能靠管理员在设置页显式配置映射（目前尚未提供该入口），
// 所以在配置出现之前，所有人一律按普通用户对待：
// 加订阅、离线下载这些只有超管能做的操作因此保持关闭。
func weComSuperLookup(st *storeBundle) func(ctx context.Context, userID int64) bool {
	return func(ctx context.Context, userID int64) bool { return false }
}
