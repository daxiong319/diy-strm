package tgbot

import (
	"context"
	"fmt"
	"strings"
)

// LinkTransferer 是「给一个分享链接/磁力，触发转存」的能力。
//
// Bot 不自己实现转存：仓内 `internal/share` 与 `internal/offlinedownload` 已有完整链路，
// 这里只约定形状，保证换实现时 Bot 不用改。
type LinkTransferer interface {
	// StartLinkTransfer 发起转存，返回对外可展示的 task id。
	StartLinkTransfer(ctx context.Context, actor Actor, link string) (string, error)
	// CancelLinkTransfer 取消一个由 Bot 发起、尚未完成的转存。
	CancelLinkTransfer(ctx context.Context, actor Actor, taskID string) (bool, error)
}

// startLinkTransfer 走注入的转存能力。
//
// 没注入时**明确报错而不是静默成功**：Bot 收到链接回一句「已转存」、
// 实际什么都没发生，用户过十分钟才会发现，而那时候他大概已经忘了。
func (s *Service) startLinkTransfer(ctx context.Context, actor Actor, link string) (string, error) {
	if s.link == nil {
		return "", fmt.Errorf("转存能力尚未接入，请先在管理台配置网盘账号")
	}
	return s.link.StartLinkTransfer(ctx, actor, link)
}

// registerAll 登记 24 条命令全集。
//
// 命令名照搬 muvyo 是有意的：用户已经在用 muvyo，换名字等于让他重新记一遍。
// 没接业务的那几条 Supported=false，回「该命令暂未支持」而不是静默 ——
// 一个不认识的命令连提示都不给，用户只能去翻文档。
func registerAll(r *Registry) {
	type entry struct {
		spec *CommandSpec
		fn   func(s *Service, ctx context.Context, u Update, args []string) (string, error)
	}
	entries := []entry{
		{spec: cmdHelp, fn: handleHelp},
		{spec: cmdShare, fn: handleShare},
		{spec: cmdNewShare, fn: nil},
		{spec: cmdEd2k, fn: nil},
		{spec: cmdDownload, fn: nil},
		{spec: cmdSync, fn: nil},
		{spec: cmdStrm, fn: handleStrm},
		{spec: cmdStatus, fn: handleStatus},
		{spec: cmdUpgrade, fn: nil},
		{spec: cmdDuplicate, fn: handleDuplicate},
		{spec: cmdRecognize, fn: handleRecognize},
		{spec: cmdOrganize, fn: handleOrganize},
		{spec: cmdSearch, fn: handleSearch},
		{spec: cmdSub, fn: handleSub},
		{spec: cmdDouban, fn: nil},
		{spec: cmdHomepage, fn: nil},
		{spec: cmdEmby, fn: nil},
		{spec: cmdMonitor, fn: nil},
		{spec: cmdLifeEvent, fn: nil},
		{spec: cmdUpdate, fn: nil},
		{spec: cmdSetPrivacy, fn: handleSetPrivacy},
		{spec: cmdCancel, fn: handleCancel},
	}
	for _, e := range entries {
		if e.fn != nil {
			e.spec.Handler = e.fn
		}
		r.Register(e.spec)
	}
}

var cmdHelp = &CommandSpec{
	Name: "help", Aliases: []string{"h"}, Tier: TierRead, Supported: true,
	Summary: "查看命令列表",
	Usage:   "/help —— 列出全部命令\n/help <命令> —— 看单条命令的用法",
}

var cmdShare = &CommandSpec{
	Name: "share", Tier: TierWrite, Supported: true,
	Summary: "转存网盘分享链接",
	Usage:   "/share <链接或提取码>\n\n也支持直接把分享链接发给我（私聊或已放行的群）。",
}

var cmdNewShare = &CommandSpec{
	Name: "newshare", Tier: TierWrite,
	Summary: "创建自己的分享链接",
}

var cmdEd2k = &CommandSpec{
	Name: "ed2k", Tier: TierRead,
	Summary: "生成 ed2k 链接",
}

var cmdDownload = &CommandSpec{
	Name: "download", Aliases: []string{"dl"}, Tier: TierSuper,
	Summary: "离线下载（磁力/直链）",
	Usage:   "/download <链接>\n/download rss <RSS 地址> —— 一次性订阅 RSS 的全部条目",
}

var cmdSync = &CommandSpec{
	Name: "sync", Tier: TierSuper,
	Summary: "同步（网盘 → 媒体库）",
}

var cmdStrm = &CommandSpec{
	Name: "strm", Tier: TierWrite, Supported: true,
	Summary: "生成 STRM，可带分类参数",
	Usage:   "/strm [分类]\n分类名走整理规则的分类目录，/strm 不带参数则整理当前目录。",
}

var cmdStatus = &CommandSpec{
	Name: "status", Aliases: []string{"st"}, Tier: TierRead, Supported: true,
	Summary: "查看系统与账号状态",
}

var cmdUpgrade = &CommandSpec{
	Name: "upgrade", Tier: TierSuper,
	Summary: "洗版",
}

var cmdDuplicate = &CommandSpec{
	Name: "duplicate", Aliases: []string{"dup"}, Tier: TierRead, Supported: true,
	Summary: "重复文件排查",
}

var cmdRecognize = &CommandSpec{
	Name: "recognize", Aliases: []string{"rec"}, Tier: TierRead, Supported: true,
	Summary: "识别文件名对应的影视信息",
}

var cmdOrganize = &CommandSpec{
	Name: "organize", Aliases: []string{"org"}, Tier: TierWrite, Supported: true,
	Summary: "触发目录整理",
	Usage:   "/organize [任务名]\n立即返回 task_id，完成后我会把结果推给你；\n用 /cancel <task_id> 取消。",
}

var cmdSearch = &CommandSpec{
	Name: "search", Aliases: []string{"s"}, Tier: TierRead, Supported: true,
	Summary: "搜索影视资源",
	Usage:   "/search <关键词>",
}

var cmdSub = &CommandSpec{
	Name: "sub", Aliases: []string{"subscribe"}, Tier: TierSuper, Supported: true,
	Summary: "添加订阅（仅管理员）",
	Usage:   "/sub <剧名>",
}

var cmdDouban = &CommandSpec{
	Name: "douban", Tier: TierRead,
	Summary: "豆瓣榜单",
}

var cmdHomepage = &CommandSpec{
	Name: "homepage", Aliases: []string{"hp"}, Tier: TierWrite,
	Summary: "Homepage 集成",
}

var cmdEmby = &CommandSpec{
	Name: "emby", Tier: TierRead,
	Summary: "Emby 相关操作",
}

var cmdMonitor = &CommandSpec{
	Name: "monitor", Tier: TierRead,
	Summary: "播放监控",
}

var cmdLifeEvent = &CommandSpec{
	Name: "lifeevent", Tier: TierRead,
	Summary: "生活事件订阅",
}

var cmdUpdate = &CommandSpec{
	Name: "update", Tier: TierSuper,
	Summary: "自动更新（容器）",
	Usage:   "/update [容器名]",
}

var cmdSetPrivacy = &CommandSpec{
	Name: "setprivacy", Tier: TierSuper, Supported: true,
	Summary: "隐私模式说明（Bot 无法自行关闭）",
	Usage:   "/setprivacy —— 打印开启/关闭隐私模式的操作步骤",
}

var cmdCancel = &CommandSpec{
	Name: "cancel", Tier: TierWrite, Supported: true,
	Summary: "取消由 Bot 发起的任务",
	Usage:   "/cancel <task_id>",
}

// handleHelp 渲染命令列表。
//
// 分三档显示（只读 / 会改动 / 仅管理员）而不是一口气列 24 行：
// 用户要找的是「我现在能用的」，把不能用的混在中间只会让他一个个试。
func handleHelp(s *Service, ctx context.Context, u Update, args []string) (string, error) {
	if len(args) > 0 {
		spec, ok := s.registry.Lookup(args[0])
		if !ok {
			return unknownCommandReply(args[0]), nil
		}
		return renderCommandHelp(spec), nil
	}
	var b strings.Builder
	b.WriteString("Telegram Bot 命令\n\n")
	groups := []struct {
		title string
		tiers []Tier
	}{
		{"只读", []Tier{TierRead}},
		{"会改动数据", []Tier{TierWrite}},
		{"仅管理员", []Tier{TierSuper}},
	}
	for _, g := range groups {
		b.WriteString("【")
		b.WriteString(g.title)
		b.WriteString("】\n")
		for _, spec := range s.registry.List() {
			if !inTiers(spec.Tier, g.tiers) {
				continue
			}
			b.WriteString("/")
			b.WriteString(spec.Name)
			if spec.Usage != "" {
				usage := strings.SplitN(spec.Usage, "\n", 2)[0]
				b.WriteString("  ")
				b.WriteString(usage)
			} else if spec.Supported {
				b.WriteString("  ")
				b.WriteString(spec.Summary)
			} else {
				b.WriteString("  （暂未支持）")
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("群里直接发分享链接需要先按 /setprivacy 关闭隐私模式。")
	return b.String(), nil
}

func renderCommandHelp(spec *CommandSpec) string {
	var b strings.Builder
	b.WriteString("/")
	b.WriteString(spec.Name)
	b.WriteString("  ")
	b.WriteString(spec.Summary)
	if spec.Usage != "" {
		b.WriteString("\n\n")
		b.WriteString(spec.Usage)
	}
	if !spec.Supported {
		b.WriteString("\n\n（这条命令 litepan 还没实现，暂时只回这一句话。）")
	}
	return b.String()
}

func inTiers(t Tier, list []Tier) bool {
	for _, v := range list {
		if v == t {
			return true
		}
	}
	return false
}
