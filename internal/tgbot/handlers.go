package tgbot

import (
	"context"
	"fmt"
	"strings"
)

// 下面这一组窄接口是 Bot 与现有 service 之间的全部接触面。
//
// 刻意不直接依赖 mediaorganize / discover / share 的具体 service：
// Bot 的职责只有「权限 + 参数解析 + 调现有能力 + 格式化」，
// 绑死具体实现会让那些包每次重构都要动 Bot，而这里只有几十行接口。

// StatusProvider 提供 /status 需要的系统状态。
type StatusProvider interface {
	BotStatus(ctx context.Context) ([]StatusLine, error)
}

// StatusLine 是一行状态。
type StatusLine struct {
	Label string
	Value string
}

// DuplicateChecker 提供 /duplicate。
type DuplicateChecker interface {
	BotDuplicateReport(ctx context.Context, limit int) ([]string, error)
}

// Recognizer 提供 /recognize。
type Recognizer interface {
	BotRecognize(ctx context.Context, filename string) ([]string, error)
}

// CategoryLister 提供 /strm 的分类参数补全与合法性校验。
//
// 直接消费 T27 已落地的 ListActiveCategories
// （internal/classifyorganize/catalog.go:141），**不重新发明**：
// 它返回的是「规则里配了哪些分类」，正是命令需要的语义。
type CategoryLister interface {
	BotCategories(ctx context.Context) ([]string, error)
}

// StrmRunner 是 /strm 的执行能力。
type StrmRunner interface {
	BotRunStrm(ctx context.Context, actor Actor, category string) (string, error)
}

// Subscriber 是 /sub 的执行能力。
type Subscriber interface {
	BotAddSubscription(ctx context.Context, actor Actor, title string) (string, error)
}

// ---- handler 实现 ----

func handleStatus(s *Service, ctx context.Context, u Update, args []string) (string, error) {
	if s.status == nil {
		return "状态查询尚未接入。", nil
	}
	lines, err := s.status.BotStatus(ctx)
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

func handleDuplicate(s *Service, ctx context.Context, u Update, args []string) (string, error) {
	if s.duplicate == nil {
		return "重复排查尚未接入。", nil
	}
	lines, err := s.duplicate.BotDuplicateReport(ctx, 10)
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "没有发现重复文件。", nil
	}
	return "发现重复（最多显示 10 组）：\n\n" + strings.Join(lines, "\n"), nil
}

func handleRecognize(s *Service, ctx context.Context, u Update, args []string) (string, error) {
	if len(args) == 0 {
		return "用法：/recognize <文件名>", nil
	}
	if s.recognizer == nil {
		return "识别能力尚未接入。", nil
	}
	lines, err := s.recognizer.BotRecognize(ctx, args[0])
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "没能识别出「" + args[0] + "」。", nil
	}
	return args[0] + "：\n" + strings.Join(lines, "\n"), nil
}

func handleStrm(s *Service, ctx context.Context, u Update, args []string) (string, error) {
	category := ""
	if len(args) > 0 {
		category = strings.Join(args, " ")
	}
	// 分类合法性校验放在这里而不是让执行方报错：
	// 「国产剧」和「国产剧 typo」会得到同样的报错，用户不知道自己写错了什么。
	if category != "" && s.categories != nil {
		known, err := s.categories.BotCategories(ctx)
		if err != nil {
			return "", err
		}
		if !containsFold(known, category) {
			return "没有名为「" + category + "」的分类。\n" + renderCategoryList(known), nil
		}
	}
	if s.strm == nil {
		return "STRM 生成尚未接入。", nil
	}
	taskID, err := s.strm.BotRunStrm(ctx, actorOf(u), category)
	if err != nil {
		return "", err
	}
	s.remember(taskID, "strm", actorOf(u))
	return fmt.Sprintf("已开始生成 STRM，任务号 %s\n完成后我会把结果发给你；中途要停用 /cancel %s", taskID, taskID), nil
}

func handleOrganize(s *Service, ctx context.Context, u Update, args []string) (string, error) {
	if s.runner == nil {
		return "整理任务尚未接入。", nil
	}
	actor := actorOf(u)
	taskName := strings.Join(args, " ")
	taskID, err := s.runner.Start(ctx, actor, "organize", taskName)
	if err != nil {
		return "", err
	}
	s.remember(taskID, "organize", actor)
	var b strings.Builder
	b.WriteString("整理已开始")
	if taskName != "" {
		b.WriteString("（任务：" + taskName + "）")
	}
	b.WriteString("\n任务号 " + taskID + "\n完成后我会把结果发给你；中途要停用 /cancel " + taskID)
	return b.String(), nil
}

func handleSearch(s *Service, ctx context.Context, u Update, args []string) (string, error) {
	keyword := strings.Join(args, " ")
	if keyword == "" {
		return "用法：/search <关键词>", nil
	}
	if s.search == nil {
		return "资源搜索尚未接入。", nil
	}
	hits, err := s.search.Search(ctx, keyword, 10)
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
	b.WriteString(itoa(int64(len(hits))))
	b.WriteString(" 条：\n\n")
	for i, h := range hits {
		if i >= 10 {
			break
		}
		b.WriteString(itoa(int64(i + 1)))
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

func handleSub(s *Service, ctx context.Context, u Update, args []string) (string, error) {
	title := strings.Join(args, " ")
	if title == "" {
		return "用法：/sub <剧名>", nil
	}
	if s.subscriber == nil {
		return "订阅能力尚未接入。", nil
	}
	id, err := s.subscriber.BotAddSubscription(ctx, actorOf(u), title)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("已添加订阅「%s」，编号 %s", title, id), nil
}

func handleShare(s *Service, ctx context.Context, u Update, args []string) (string, error) {
	link := strings.Join(args, " ")
	if link == "" {
		return "用法：/share <分享链接或提取码>", nil
	}
	actor := actorOf(u)
	taskID, err := s.startLinkTransfer(ctx, actor, link)
	if err != nil {
		return "", err
	}
	s.remember(taskID, "link", actor)
	return fmt.Sprintf("已开始转存，任务号 %s\n完成后我会把结果发给你；中途要停用 /cancel %s", taskID, taskID), nil
}

func handleCancel(s *Service, ctx context.Context, u Update, args []string) (string, error) {
	if len(args) == 0 {
		return "用法：/cancel <task_id>", nil
	}
	taskID := args[0]
	actor := actorOf(u)
	job, ok := s.tasks.get(taskID)
	if !ok {
		// 查不到就说查不到。「这个任务不存在」和「这个任务已完成」对用户是同一件事：
		// 他手里那个 id 现在不管用了。谎称「已取消」会让他以为整理停住了，而它还在跑。
		return "没有这个任务号（或它已经结束）：" + taskID + "\nBot 只认自己发起、还没结束的任务。", nil
	}
	if !ownsBy(job, actor) {
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
	if job.Kind == "link" && s.link != nil {
		cancelled, err = s.link.CancelLinkTransfer(ctx, actor, taskID)
	} else {
		cancelled, err = s.runner.Cancel(ctx, actor, taskID)
	}
	if err != nil {
		return "", err
	}
	if !cancelled {
		return "这个任务没能取消（可能已经结束，或已经不在我的管理范围内）。", nil
	}
	s.tasks.markCancelled(taskID)
	return "已取消任务 " + taskID + "。", nil
}

func handleSetPrivacy(s *Service, ctx context.Context, u Update, args []string) (string, error) {
	// 这个命令存在的意义是「解释为什么群里发链接没反应」，
	// 而不是假装 Bot 能关掉它 —— Bot API 里的隐私模式是在 BotFather 里改的，
	// 没有对应的 Bot API 方法。回一个假的「已关闭」会让用户一直找不到原因。
	return "Bot 无法自行关闭隐私模式，需要在 BotFather 里操作：\n\n" +
			"1. 打开 @BotFather\n" +
			"2. /mybots → 选中你的 Bot\n" +
			"3. Bot Settings → Group Privacy → 选 Disable\n" +
			"4. 群里把 Bot 设为管理员（至少需要能读消息）\n\n" +
			"关闭后，群里直接发分享链接或磁力就会自动转存。\n" +
			"注意：默认配置下群里只有 / 命令、@提及和回复会送达 Bot —— 隐私模式开着时收不到普通消息，这是 Telegram 的机制，不是故障。",
		nil
}

// remember 记一条由 Bot 发起的任务。
func (s *Service) remember(taskID, kind string, actor Actor) {
	if strings.TrimSpace(taskID) == "" {
		return
	}
	s.tasks.add(&Job{ID: taskID, Kind: kind, Actor: actor, CreatedAt: s.now()})
}

// renderCategoryList 渲染可用分类。
func renderCategoryList(list []string) string {
	if len(list) == 0 {
		return "（还没有配置任何分类）"
	}
	trimmed := list
	if len(trimmed) > 30 {
		trimmed = trimmed[:30]
	}
	out := "可用分类：" + strings.Join(trimmed, "、")
	if len(list) > len(trimmed) {
		out += fmt.Sprintf(" 等 %d 个", len(list))
	}
	return out
}

func containsFold(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}
