package inboundbot

import "context"

// 平台无关的命令表。
//
// 22 条命令的元数据只在**这一处**定义：Telegram 与企业微信挂的是同一份。
// 用户是同一批人，让两个平台的 /help 长得不一样只会让人怀疑「是不是两个东西」。
// 平台之间只允许有两处不同：命令表本身（Telegram 多一条 /setprivacy），
// 以及对外回话的平台抬头（Deps.Title）。

// SharedCommandSpecs 登记 Telegram 与企业微信**共有**的命令全集。
//
// 命令名照搬 muvyo 是有意的：用户已经在用 muvyo，换名字等于让他重新记一遍。
// 没接业务的那几条 Supported=false，回「该命令暂未支持」而不是静默 ——
// 一个不认识的命令连提示都不给，用户只能去翻文档。
func SharedCommandSpecs() []*CommandSpec {
	type entry struct {
		spec *CommandSpec
		fn   func(d *Deps, ctx context.Context, actor Actor, args []string) (string, error)
	}
	entries := []entry{
		{spec: cmdHelp, fn: HandleHelp},
		{spec: cmdShare, fn: HandleShare},
		{spec: cmdNewShare},
		{spec: cmdEd2k},
		{spec: cmdDownload},
		{spec: cmdSync},
		{spec: cmdStrm, fn: HandleStrm},
		{spec: cmdStatus, fn: HandleStatus},
		{spec: cmdUpgrade},
		{spec: cmdDuplicate, fn: HandleDuplicate},
		{spec: cmdRecognize, fn: HandleRecognize},
		{spec: cmdOrganize, fn: HandleOrganize},
		{spec: cmdSearch, fn: HandleSearch},
		{spec: cmdSub, fn: HandleSub},
		{spec: cmdDouban},
		{spec: cmdHomepage},
		{spec: cmdEmby},
		{spec: cmdMonitor},
		{spec: cmdLifeEvent},
		{spec: cmdUpdate},
		{spec: cmdCancel, fn: HandleCancel},
	}
	out := make([]*CommandSpec, 0, len(entries))
	for _, e := range entries {
		if e.fn != nil {
			e.spec.Handler = e.fn
		}
		out = append(out, e.spec)
	}
	return out
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

var cmdCancel = &CommandSpec{
	Name: "cancel", Tier: TierWrite, Supported: true,
	Summary: "取消由 Bot 发起的任务",
	Usage:   "/cancel <task_id>",
}
