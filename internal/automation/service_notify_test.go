package automation

import (
	"context"
	"strings"
	"testing"

	"litepan/internal/domain"
)

type capturedNotice struct {
	level, category, title, message string
	accountID                       int64
}

func captureNotices(captured *[]capturedNotice) Notifier {
	return func(_ context.Context, level, category, title, message string, accountID, _ int64) {
		*captured = append(*captured, capturedNotice{
			level: level, category: category, title: title, message: message, accountID: accountID,
		})
	}
}

func TestRunNotifyPerFileUsesTriggeredFileNames(t *testing.T) {
	t.Parallel()

	var captured []capturedNotice
	service := New(Options{Notify: captureNotices(&captured)})
	batch := &casTriggerBatch{
		AccountID: 2,
		DriveType: "cloud189",
		Dir:       "影视/CAS待整理",
		Files:     []string{"流浪地球2.cas", "沙丘2.cas"},
		Filenames: "流浪地球2.cas、沙丘2.cas",
	}

	result := service.runNotify(context.Background(), map[string]any{
		"title":   "「{file}」已入库",
		"message": "来自 {drive} {dir}，共 {count} 个文件",
	}, batch)

	if result["success"] != true {
		t.Fatalf("通知动作应成功: %#v", result)
	}
	if len(captured) != 2 {
		t.Fatalf("每部影片一条，期望 2 条，实际 %d", len(captured))
	}
	if captured[0].title != "「流浪地球2.cas」已入库" {
		t.Fatalf("标题占位符未替换: %q", captured[0].title)
	}
	if captured[1].title != "「沙丘2.cas」已入库" {
		t.Fatalf("第二条标题应使用自己的文件名: %q", captured[1].title)
	}
	if captured[0].message != "来自 天翼云盘 /影视/CAS待整理，共 2 个文件" {
		t.Fatalf("内容占位符未替换: %q", captured[0].message)
	}
	if captured[0].accountID != 2 || captured[0].category != "automation" {
		t.Fatalf("通知归属不正确: %#v", captured[0])
	}
}

func TestRunNotifyOnceMergesBatchIntoOneNotice(t *testing.T) {
	t.Parallel()

	var captured []capturedNotice
	service := New(Options{Notify: captureNotices(&captured)})
	batch := &casTriggerBatch{
		AccountID: 2,
		DriveType: "cloud189",
		Dir:       "影视/CAS待整理",
		Files:     []string{"流浪地球2.cas", "沙丘2.cas"},
		Filenames: "流浪地球2.cas、沙丘2.cas",
	}

	result := service.runNotify(context.Background(), map[string]any{
		"batch_mode": notifyBatchModeOnce,
		"message":    "已入库 {files}",
	}, batch)

	if result["success"] != true {
		t.Fatalf("通知动作应成功: %#v", result)
	}
	if len(captured) != 1 {
		t.Fatalf("整批一条，期望 1 条，实际 %d", len(captured))
	}
	if captured[0].message != "已入库 流浪地球2.cas、沙丘2.cas" {
		t.Fatalf("{files} 占位符未替换: %q", captured[0].message)
	}
}

func TestRunNotifyWithoutTriggerContextReplacesPlaceholdersWithEmpty(t *testing.T) {
	t.Parallel()

	var captured []capturedNotice
	service := New(Options{Notify: captureNotices(&captured)})

	result := service.runNotify(context.Background(), map[string]any{
		"title":   "定时联动「{file}」",
		"message": "{drive}{dir}",
	}, nil)

	if result["success"] != true {
		t.Fatalf("无触发上下文时通知仍应发送: %#v", result)
	}
	if len(captured) != 1 {
		t.Fatalf("期望 1 条通知，实际 %d", len(captured))
	}
	if strings.Contains(captured[0].title, "{file}") || strings.Contains(captured[0].message, "{drive}") {
		t.Fatalf("占位符应替换为空而不是原样保留: title=%q message=%q", captured[0].title, captured[0].message)
	}
	// 占位符被清空后不应留下「」这类空壳标点。
	if strings.Contains(captured[0].title, "「」") {
		t.Fatalf("标题残留空引号: %q", captured[0].title)
	}
	if captured[0].title != "定时联动" {
		t.Fatalf("标题应清理为 %q，实际 %q", "定时联动", captured[0].title)
	}
	// 内容只剩占位符、替换后为空，回退到默认文案。
	if captured[0].message != notifyDefaultBody {
		t.Fatalf("内容占位符清空后应回退到默认文案: %q", captured[0].message)
	}
}

func TestApplyPlaceholdersCleansEmptyShells(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"{file}":  "",
		"{drive}": "",
		"{dir}":   "",
		"{count}": "0",
	}

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"中文书名号空壳", "「{file}」已入库", "已入库"},
		{"方括号空壳", "【{file}】已入库", "已入库"},
		{"圆括号空壳", "({file}) 已入库", "已入库"},
		{"内容只剩占位符", "{drive}{dir}", ""},
		{"中间空壳压缩空白", "来自 {drive} {dir} 的影片", "来自 的影片"},
		{"无占位符保持原样", "本次联动已执行完成", "本次联动已执行完成"},
		{"相邻顿号收尾", "{file}、{drive}", ""},
		{"零计数尾巴被删除", "来自 {drive} {dir}，共 {count} 个文件", "来自"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := applyPlaceholders(tc.in, values); got != tc.want {
				t.Fatalf("applyPlaceholders(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRunNotifyEmptyShellTitleFallsBackToDefault(t *testing.T) {
	t.Parallel()

	var captured []capturedNotice
	service := New(Options{Notify: captureNotices(&captured)})

	// 只写了「{file}」的标题，没有触发上下文时整体为空，应回退默认标题而不是发一条空标题。
	result := service.runNotify(context.Background(), map[string]any{
		"title":   "「{file}」",
		"message": "已入库",
	}, nil)

	if result["success"] != true {
		t.Fatalf("通知动作应成功: %#v", result)
	}
	if len(captured) != 1 {
		t.Fatalf("期望 1 条通知，实际 %d", len(captured))
	}
	if captured[0].title != notifyDefaultTitle {
		t.Fatalf("空壳标题应回退默认标题 %q，实际 %q", notifyDefaultTitle, captured[0].title)
	}
	if captured[0].message != "已入库" {
		t.Fatalf("无占位符的内容应原样保留，实际 %q", captured[0].message)
	}
}

func TestRunNotifyFallsBackToDefaultText(t *testing.T) {
	t.Parallel()

	var captured []capturedNotice
	service := New(Options{Notify: captureNotices(&captured)})

	result := service.runNotify(context.Background(), map[string]any{}, nil)

	if result["success"] != true {
		t.Fatalf("通知动作应成功: %#v", result)
	}
	if len(captured) != 1 || captured[0].title != notifyDefaultTitle || captured[0].message != notifyDefaultBody {
		t.Fatalf("空参数应使用默认文案: %#v", captured)
	}
	if captured[0].level != "success" {
		t.Fatalf("默认级别应为 success: %q", captured[0].level)
	}
}

func TestRunNotifyWithoutNotifierFails(t *testing.T) {
	t.Parallel()

	service := New(Options{})
	result := service.runNotify(context.Background(), map[string]any{}, nil)

	if result["success"] != false || result["status"] != "failed" {
		t.Fatalf("未注入通知服务时应明确失败: %#v", result)
	}
}

func TestNotifyActionIsDispatchable(t *testing.T) {
	t.Parallel()

	var captured []capturedNotice
	service := New(Options{Notify: captureNotices(&captured)})

	result := service.executeAction(context.Background(), RuleAction{
		Type:   domain.AutomationActionNotify,
		Params: map[string]any{"message": "完成"},
	}, nil, nil)

	if result["success"] != true {
		t.Fatalf("notify 动作应可由动作分发器执行: %#v", result)
	}
	if len(captured) != 1 {
		t.Fatalf("期望派发出一条通知，实际 %d", len(captured))
	}
}

func TestActionDisplayNameForNotify(t *testing.T) {
	t.Parallel()

	got := actionDisplayName(RuleAction{Type: domain.AutomationActionNotify})
	if got != "发送入库通知" {
		t.Fatalf("actionDisplayName() = %q, want %q", got, "发送入库通知")
	}
}
