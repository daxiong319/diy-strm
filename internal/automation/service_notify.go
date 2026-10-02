package automation

import (
	"context"
	"fmt"
	"strings"

	"litepan/internal/cas"
)

// 入库通知动作：链路跑完（整理 → STRM → 元数据 → 扫库）后给用户一个结论。
//
// 通知走站内通知中心（与 CAS 转存通知同一条路），
// 站内落库后由通知渠道 dispatcher 转发到 Telegram/Bark 等外部渠道，
// 因此这里只需要注入 Notifier，不必知道渠道细节。

// notifyBatchModePerFile 每部影片发一条，成功/失败能一一对应；
// notifyBatchModeOnce 整批合并成一条，适合一次转存几十个文件时避免刷屏。
const (
	notifyBatchModePerFile = "per_file"
	notifyBatchModeOnce    = "once"
)

const (
	notifyDefaultTitle = "自动联动完成"
	notifyDefaultBody  = "本次联动已执行完成"
)

// notifyPlaceholders 支持在标题与内容里插入本次触发上下文。
// 定时/Webhook 等非 CAS 触发没有上下文，占位符替换为空串而不是原样保留，
// 否则用户会看到一条写着「{file}」的通知。
func placeholders(batch *casTriggerBatch) map[string]string {
	if batch == nil {
		return map[string]string{
			"{file}":       "",
			"{files}":      "",
			"{drive}":      "",
			"{dir}":        "",
			"{count}":      "0",
			"{account_id}": "",
		}
	}
	return map[string]string{
		"{file}":       batch.Filenames,
		"{files}":      strings.Join(batch.Files, "、"),
		"{drive}":      cas.DriveDisplayName(batch.DriveType),
		"{dir}":        cas.DisplaySaveDir(batch.Dir),
		"{count}":      fmt.Sprintf("%d", len(batch.Files)),
		"{account_id}": fmt.Sprintf("%d", batch.AccountID),
	}
}

// 占位符被替换成空串后常会留下孤零零的括号或分隔符，
// 例如标题「「{file}」已入库」在手动执行时变成「「」已入库」。
// 这里把这些空壳一并收掉，避免用户看到没有意义的标点。
var notifyEmptyShells = []string{
	"「」", "『』", "（）", "()", "[]", "【】", "《》", "<>", "''", `""`, "，，", "。。",
}

// 没有触发上下文时 count 为 0，此时「共 {count} 个文件」这类尾巴没有意义，
// 整条通知退化成「来自 ，共 0 个文件」。把这些片段一并去掉。
var notifyZeroCountFragments = []string{
	"，共 0 个文件", ", 共 0 个文件", "，共 0 个", ", 共 0 个", "共 0 个文件", "共 0 个",
}

func applyPlaceholders(text string, values map[string]string) string {
	out := text
	for key, value := range values {
		out = strings.ReplaceAll(out, key, value)
	}
	if values["{count}"] == "0" {
		for _, frag := range notifyZeroCountFragments {
			out = strings.ReplaceAll(out, frag, "")
		}
	}
	// 反复清理，直到没有新的空壳产生（嵌套场景例如「【{file}】」）
	for i := 0; i < 3; i++ {
		before := out
		for _, shell := range notifyEmptyShells {
			out = strings.ReplaceAll(out, shell, "")
		}
		if out == before {
			break
		}
	}
	out = strings.Join(strings.Fields(out), " ")
	return strings.TrimSpace(strings.Trim(out, " ，,、/·-"))
}

func normalizeNotifyBatchMode(raw any) string {
	if strings.TrimSpace(anyString(raw)) == notifyBatchModeOnce {
		return notifyBatchModeOnce
	}
	return notifyBatchModePerFile
}

// runNotify 发送入库通知。
//
// 返回值里的 success 表示「通知是否已投递」，不表示上游整理/扫库是否成功：
// 上游失败由各自动作的步骤状态负责，这里只报告通知本身的结果。
func (s *Service) runNotify(ctx context.Context, params map[string]any, batch *casTriggerBatch) map[string]any {
	if s.notify == nil {
		return map[string]any{"status": "failed", "success": false, "message": "通知服务未就绪"}
	}
	level := strings.TrimSpace(anyString(params["level"]))
	if level == "" {
		level = "success"
	}
	values := placeholders(batch)
	// 标题与内容都先做占位符替换：用户填了「「{file}」已入库」但本次没有触发上下文时，
	// 替换后只剩空壳，此时回退到默认文案，而不是发一条只有括号的通知。
	rawTitle := strings.TrimSpace(anyString(params["title"]))
	rawBody := strings.TrimSpace(anyString(params["message"]))

	accountID := int64(0)
	if batch != nil {
		accountID = batch.AccountID
	}

	resolveTitle := func(values map[string]string) string {
		if rawTitle == "" {
			return notifyDefaultTitle
		}
		if text := applyPlaceholders(rawTitle, values); text != "" {
			return text
		}
		return notifyDefaultTitle
	}
	resolveBody := func(values map[string]string) string {
		if rawBody == "" {
			return notifyDefaultBody
		}
		if text := applyPlaceholders(rawBody, values); text != "" {
			return text
		}
		return notifyDefaultBody
	}

	// 整批一条：没有触发上下文时也照发，此时就是一条「联动完成」的提示。
	if normalizeNotifyBatchMode(params["batch_mode"]) == notifyBatchModeOnce || batch == nil || len(batch.Files) == 0 {
		s.notify(ctx, level, "automation", resolveTitle(values), resolveBody(values), accountID, 0)
		return map[string]any{
			"status":  "success",
			"success": true,
			"message": "已发送入库通知",
			"data":    map[string]any{"batch_mode": notifyBatchModeOnce, "count": 1},
		}
	}

	// 每个文件一条：{file} 换成具体文件名，{files} 同理，
	// 这样用户能一眼看出是哪一部影片没入库。
	sent := 0
	for _, name := range batch.Files {
		perFile := make(map[string]string, len(values))
		for k, v := range values {
			perFile[k] = v
		}
		perFile["{file}"] = name
		perFile["{files}"] = name
		s.notify(ctx, level, "automation", resolveTitle(perFile), resolveBody(perFile), accountID, 0)
		sent++
	}
	return map[string]any{
		"status":  "success",
		"success": true,
		"message": fmt.Sprintf("已发送 %d 条入库通知", sent),
		"data":    map[string]any{"batch_mode": notifyBatchModePerFile, "count": sent},
	}
}
