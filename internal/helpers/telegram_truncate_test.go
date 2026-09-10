package helpers

import (
	"strings"
	"testing"
)

func TestTelegramUTF16Len(t *testing.T) {
	// 中文 BMP=1
	if got := telegramUTF16Len("中文"); got != 2 {
		t.Fatalf("中文长度 = %d，期望 2", got)
	}
	// emoji 补充平面=2
	if got := telegramUTF16Len("😀"); got != 2 {
		t.Fatalf("emoji 长度 = %d，期望 2", got)
	}
}

func TestTelegramTruncateShortTextUnchanged(t *testing.T) {
	text := "整理完成：成功 3 个"
	if got := telegramTruncate(text, 4000); got != text {
		t.Fatalf("短文本不应被截断，got = %q", got)
	}
}

func TestTelegramTruncateLongText(t *testing.T) {
	text := strings.Repeat("重案六组消失的警号 Episode 12 mkv 2160p WEB-DL H265\n", 500) // ~25k rune
	got := telegramTruncate(text, 4000)
	if !strings.Contains(got, "内容过长已截断") {
		t.Fatalf("超长文本应包含截断提示，尾部 = %q", got[max(0, len(got)-60):])
	}
	if n := telegramUTF16Len(got); n > 4096 {
		t.Fatalf("截断后 UTF-16 长度 %d 超过 Telegram 上限 4096", n)
	}
	// 截断应保留开头内容
	if !strings.HasPrefix(got, "重案六组") {
		t.Fatalf("截断后应保留开头内容")
	}
}

func TestTelegramTruncateCleansUnclosedTag(t *testing.T) {
	// 截断点落在 HTML 标签中间：<b>标题文字…（长文本）…<b>残段
	text := strings.Repeat("x", 3998) + "<b>加粗内容结尾"
	got := telegramTruncate(text, 4000)
	if strings.Contains(got, "<b>") && !strings.Contains(got, "</b>") {
		// 残段被清理或 '<' 已转全角，不能存在未闭合的 '<b>'
		if strings.Contains(got, "<b>") {
			t.Fatalf("未闭合标签未被清理：尾部 = %q", got[max(0, len(got)-60):])
		}
	}
	if n := telegramUTF16Len(got); n > 4096 {
		t.Fatalf("截断后 UTF-16 长度 %d 超限", n)
	}
}

func TestTelegramTruncateCleansEntity(t *testing.T) {
	text := strings.Repeat("y", 4000) + "名称 &amp更多"
	got := telegramTruncate(text, 4000)
	// &amp 残段应被清理（不存在截半的 "&amp" 结尾）
	if strings.HasSuffix(strings.TrimSuffix(got, "\n\n…（内容过长已截断）"), "&amp") {
		t.Fatalf("未闭合实体残段未清理")
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
