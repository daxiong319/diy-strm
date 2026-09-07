package pan139

import (
	"testing"
	"time"
)

func TestSetDefaultQPS(t *testing.T) {
	c := NewClient(1, "test")
	// 默认 2 QPS
	if c.defaultLimiter.Burst() != 1 {
		t.Fatal("默认限流器应存在")
	}
	c.SetDefaultQPS(5)
	// 通过等待行为验证生效：连续取 6 个许可，5 QPS 下第 6 个应需要等待
	start := time.Now()
	ctx := t.Context()
	for i := 0; i < 6; i++ {
		if err := c.waitForPermission(ctx, "/file/list"); err != nil {
			t.Fatalf("等待限流许可失败：%v", err)
		}
	}
	elapsed := time.Since(start)
	if elapsed < 100*time.Millisecond {
		t.Fatalf("5 QPS 下取第 6 个许可应有明显等待，实际 %s", elapsed)
	}
	// 非法值不改限流器
	c.SetDefaultQPS(0)
	c.SetDefaultQPS(-1)
}

func TestBackoffFlow(t *testing.T) {
	c := NewClient(1, "test")
	ctx := t.Context()

	// 无退避时立即通过
	if err := c.waitBackoff(ctx); err != nil {
		t.Fatalf("无退避不应阻塞：%v", err)
	}
	// 标记限流后进入退避
	c.markRateLimited()
	c.backoffMu.Lock()
	until := c.backoffUntil
	level := c.backoffLevel
	c.backoffMu.Unlock()
	if level != 1 {
		t.Fatalf("首次退避级别应为 1，实际 %d", level)
	}
	if time.Until(until) < MinBackoffDelay-time.Second {
		t.Fatalf("首次退避应约 %s，实际截止 %s", MinBackoffDelay, until)
	}
	// 连续限流指数递增
	c.markRateLimited()
	c.markRateLimited()
	c.backoffMu.Lock()
	level = c.backoffLevel
	c.backoffMu.Unlock()
	if level != 3 {
		t.Fatalf("三次限流后级别应为 3，实际 %d", level)
	}
	// 成功即复位
	c.resetBackoff()
	c.backoffMu.Lock()
	zero := c.backoffUntil.IsZero() && c.backoffLevel == 0
	c.backoffMu.Unlock()
	if !zero {
		t.Fatal("复位后退避状态应清零")
	}
	if err := c.waitBackoff(ctx); err != nil {
		t.Fatalf("复位后不应阻塞：%v", err)
	}
}

func TestIsRateLimitText(t *testing.T) {
	cases := map[string]bool{
		"操作过于频繁，请稍后再试": true,
		"请求过多":          false, // 无关键词
		"Too Many Requests": true,
		"Rate limit exceeded": true,
		"文件不存在":         false,
		"":                  false,
	}
	for text, want := range cases {
		if got := isRateLimitText(text); got != want {
			t.Fatalf("isRateLimitText(%q) = %v, 期望 %v", text, got, want)
		}
	}
}
