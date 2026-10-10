package inboundbot

import (
	"context"
	"fmt"
	"strings"
)

// ---- handler 实现 ----
//
// 这十个 handler 就是抽这一层的全部收益：换平台时它们一行都不用改。
// 它们只依赖 Deps + Actor + ctx，与 Telegram/企微都没有关系。

func HandleStatus(d *Deps, ctx context.Context, actor Actor, args []string) (string, error) {
	if d == nil || d.Status == nil {
		return "状态查询尚未接入。", nil
	}
	lines, err := d.Status.BotStatus(ctx)
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "没有可显示的状态。", nil
	}
	var b strings.Builder
	b.WriteString("系统状态\n")
	for _, l := range lines {
		b.WriteString("\n")
		b.WriteString(l.Label)
		b.WriteString("：")
		b.WriteString(l.Value)
	}
	return b.String(), nil
}

func HandleDuplicate(d *Deps, ctx context.Context, actor Actor, args []string) (string, error) {
	if d == nil || d.Duplicate == nil {
		return "重复排查尚未接入。", nil
	}
	lines, err := d.Duplicate.BotDuplicateReport(ctx, 10)
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "没有发现重复文件。", nil
	}
	return "发现重复（最多显示 10 组）：\n\n" + strings.Join(lines, "\n"), nil
}

func HandleRecognize(d *Deps, ctx context.Context, actor Actor, args []string) (string, error) {
	if len(args) == 0 {
		return "用法：/recognize <文件名>", nil
	}
	if d == nil || d.Recognizer == nil {
		return "识别能力尚未接入。", nil
	}
	lines, err := d.Recognizer.BotRecognize(ctx, args[0])
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "没能识别出「" + args[0] + "」。", nil
	}
	return args[0] + "：\n" + strings.Join(lines, "\n"), nil
}

func HandleStrm(d *Deps, ctx context.Context, actor Actor, args []string) (string, error) {
	category := strings.Join(args, " ")
	// 分类合法性校验放在这里而不是让执行方报错：
	// 「国产剧」和「国产剧 typo」会得到同样的报错，用户不知道自己写错了什么。
	if category != "" && d != nil && d.Categories != nil {
		known, err := d.Categories.BotCategories(ctx)
		if err != nil {
			return "", err
		}
		if !ContainsFold(known, category) {
			return "没有名为「" + category + "」的分类。\n" + RenderCategoryList(known), nil
		}
	}
	if d == nil || d.Strm == nil {
		return "STRM 生成尚未接入。", nil
	}
	taskID, err := d.Strm.BotRunStrm(ctx, actor, category)
	if err != nil {
		return "", err
	}
	remember(d, taskID, "strm", actor)
	return fmt.Sprintf("已开始生成 STRM，任务号 %s\n完成后我会把结果发给你；中途要停用 /cancel %s", taskID, taskID), nil
}

func HandleOrganize(d *Deps, ctx context.Context, actor Actor, args []string) (string, error) {
	if d == nil || d.Runner == nil {
		return "整理任务尚未接入。", nil
	}
	taskName := strings.Join(args, " ")
	taskID, err := d.Runner.Start(ctx, actor, "organize", taskName)
	if err != nil {
		return "", err
	}
	remember(d, taskID, "organize", actor)
	var b strings.Builder
	b.WriteString("整理已开始")
	if taskName != "" {
		b.WriteString("（任务：" + taskName + "）")
	}
	b.WriteString("\n任务号 " + taskID + "\n完成后我会把结果发给你；中途要停用 /cancel " + taskID)
	return b.String(), nil
}

func HandleSearch(d *Deps, ctx context.Context, actor Actor, args []string) (string, error) {
	keyword := strings.Join(args, " ")
	if keyword == "" {
		return "用法：/search <关键词>", nil
	}
	if d == nil || d.Search == nil {
		return "资源搜索尚未接入。", nil
	}
	hits, err := d.Search.Search(ctx, keyword, 10)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return "没有搜到「" + keyword + "」。", nil
	}
	var b strings.Builder
	b.WriteString("「")
	b.WriteString(keyword)
	b.WriteString("」搜到 ")
	b.WriteString(Itoa(int64(len(hits))))
	b.WriteString(" 条：\n\n")
	for i, h := range hits {
		if i >= 10 {
			break
		}
		b.WriteString(Itoa(int64(i + 1)))
		b.WriteString(". ")
		b.WriteString(h.Title)
		if h.Source != "" {
			b.WriteString("  来自 " + h.Source)
		}
		if h.Size != "" {
			b.WriteString("  " + h.Size)
		}
		if h.ShareURL != "" {
			b.WriteString("\n   ")
			b.WriteString(h.ShareURL)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func HandleSub(d *Deps, ctx context.Context, actor Actor, args []string) (string, error) {
	title := strings.Join(args, " ")
	if title == "" {
		return "用法：/sub <剧名>", nil
	}
	if d == nil || d.Subscriber == nil {
		return "订阅能力尚未接入。", nil
	}
	id, err := d.Subscriber.BotAddSubscription(ctx, actor, title)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("已添加订阅「%s」，编号 %s", title, id), nil
}

func HandleShare(d *Deps, ctx context.Context, actor Actor, args []string) (string, error) {
	link := strings.Join(args, " ")
	if link == "" {
		return "用法：/share <分享链接或提取码>", nil
	}
	if d == nil || d.Link == nil {
		return "", fmt.Errorf("转存能力尚未接入，请先在管理台配置网盘账号")
	}
	taskID, err := d.Link.StartLinkTransfer(ctx, actor, link)
	if err != nil {
		return "", err
	}
	remember(d, taskID, "link", actor)
	return fmt.Sprintf("已开始转存，任务号 %s\n完成后我会把结果发给你；中途要停用 /cancel %s", taskID, taskID), nil
}

func HandleCancel(d *Deps, ctx context.Context, actor Actor, args []string) (string, error) {
	if len(args) == 0 {
		return "用法：/cancel <task_id>", nil
	}
	if d == nil || d.Tasks == nil {
		return "暂无可取消的任务。", nil
	}
	taskID := args[0]
	job, ok := d.Tasks.Get(taskID)
	if !ok {
		// 查不到就说查不到。「这个任务不存在」和「这个任务已完成」对用户是同一件事：
		// 他手里那个 id 现在不管用了。谎称「已取消」会让他以为整理停住了，而它还在跑。
		return "没有这个任务号（或它已经结束）：" + taskID + "\nBot 只认自己发起、还没结束的任务。", nil
	}
	if !OwnsBy(job, actor) {
		return "这个任务不是你发起的，我不能替你取消。", nil
	}
	if job.Cancelled {
		return "这个任务已经取消过了。", nil
	}
	if job.Done {
		return "这个任务已经结束了，取消不了。", nil
	}

	var cancelled bool
	var err error
	if job.Kind == "link" && d.Link != nil {
		cancelled, err = d.Link.CancelLinkTransfer(ctx, actor, taskID)
	} else if d.Runner != nil {
		cancelled, err = d.Runner.Cancel(ctx, actor, taskID)
	} else {
		return "这个任务没有可用的取消入口。", nil
	}
	if err != nil {
		return "", err
	}
	if !cancelled {
		return "这个任务没能取消（可能已经结束，或已经不在我的管理范围内）。", nil
	}
	d.Tasks.MarkCancelled(taskID)
	return "已取消任务 " + taskID + "。", nil
}

// remember 记一条由 Bot 发起的任务。
func remember(d *Deps, taskID, kind string, actor Actor) {
	if d == nil || d.Tasks == nil || strings.TrimSpace(taskID) == "" {
		return
	}
	d.Tasks.Add(&Job{ID: taskID, Kind: kind, Actor: actor, CreatedAt: d.now()})
}

// HandleHelp 渲染命令列表。
//
// 分三档显示（只读 / 会改动 / 仅管理员）而不是一口气列全部：
// 用户要找的是「我现在能用的」，把不能用的混在中间只会让他一个个试。
func HandleHelp(d *Deps, ctx context.Context, actor Actor, args []string) (string, error) {
	reg := registryOf(d)
	if len(args) > 0 {
		spec, ok := reg.Lookup(args[0])
		if !ok {
			return UnknownCommandReply(args[0]), nil
		}
		return RenderCommandHelp(spec), nil
	}
	title := "Bot"
	if d != nil && d.Title != "" {
		title = d.Title
	}
	var b strings.Builder
	b.WriteString(title)
	b.WriteString(" 命令\n\n")
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
		for _, spec := range reg.List() {
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
	b.WriteString("群里直接发分享链接需要单独打开「群链接」开关。")
	return b.String(), nil
}

// RenderCommandHelp 渲染单条命令的用法。
func RenderCommandHelp(spec *CommandSpec) string {
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

// registryOf 取命令表。
//
// 它挂在 Deps 上而不是参数上：十个 handler 里只有 /help 需要命令表，
// 而命令表是「这个平台装配了哪些命令」的答案，天然属于装配结果。
func registryOf(d *Deps) *Registry {
	if d == nil || d.Registry == nil {
		return NewRegistry()
	}
	return d.Registry
}

// RenderCommandHelpSpec 按命令名渲染用法。
//
// 找不到时返回一句明确的「不认识的命令」而不是空串：空串会让入口
// 不知道该不该回话，结果用户发 /help 之后什么都没有。
func RenderCommandHelpSpec(reg *Registry, name string) string {
	if reg == nil {
		return UnknownCommandReply(name)
	}
	spec, ok := reg.Lookup(name)
	if !ok {
		return UnknownCommandReply(name)
	}
	return RenderCommandHelp(spec)
}
