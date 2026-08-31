// Package hdhive —— 客户端节流（借鉴 mediavault 的双层限流设计）：
//   - 全局 API 滑动窗口：默认 2 QPS（所有通道的所有业务请求共享），避免连续请求
//     触发上游 "rate limit exceeded"（实测 16 连发详情查询就会撞限）
//   - 解锁单独节流：两次解锁之间最小间隔（默认 5 秒），解锁是计费操作更易触发风控
// 可用环境变量覆盖：HDHIVE_API_QPS（1..10）、HDHIVE_UNLOCK_MIN_INTERVAL_SECONDS（1..60）
package hdhive

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	hiveAPIQPSDefault            = 2
	hiveUnlockMinIntervalDefault = 5 * time.Second
)

func init() {
	if v := strings.TrimSpace(os.Getenv("HDHIVE_API_QPS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 10 {
			hiveAPIQPSDefault = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("HDHIVE_UNLOCK_MIN_INTERVAL_SECONDS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 60 {
			hiveUnlockMinIntervalDefault = time.Duration(n) * time.Second
		}
	}
}

// hiveRateLimiter 进程级滑动窗口限流器
type hiveRateLimiter struct {
	mu        sync.Mutex
	timestamps []time.Time // 窗口内的请求时间戳
	qps       int
	window    time.Duration
}

var apiLimiter = &hiveRateLimiter{qps: hiveAPIQPSDefault, window: time.Second}

var (
	unlockMu       sync.Mutex
	unlockLastTime time.Time
	unlockInterval = hiveUnlockMinIntervalDefault
)

// AcquireAPI 阻塞直到获得一个全局 API 请求许可（滑动窗口 QPS）
func AcquireAPI(ctx context.Context) error {
	return apiLimiter.acquire(ctx)
}

// AcquireUnlock 阻塞直到获得解锁许可（与上一次解锁至少间隔 unlockInterval）
func AcquireUnlock(ctx context.Context) error {
	for {
		unlockMu.Lock()
		wait := unlockInterval - time.Since(unlockLastTime)
		if wait <= 0 {
			unlockLastTime = time.Now()
			unlockMu.Unlock()
			return nil
		}
		unlockMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

func (l *hiveRateLimiter) acquire(ctx context.Context) error {
	for {
		l.mu.Lock()
		now := time.Now()
		// 清理窗口外的旧时间戳
		keep := l.timestamps[:0]
		for _, ts := range l.timestamps {
			if now.Sub(ts) < l.window {
				keep = append(keep, ts)
			}
		}
		l.timestamps = keep
		if len(l.timestamps) < l.qps {
			l.timestamps = append(l.timestamps, now)
			l.mu.Unlock()
			return nil
		}
		// 最早的时间戳出窗口后可重试
		retryAfter := l.window - now.Sub(l.timestamps[0])
		l.mu.Unlock()
		if retryAfter < 50*time.Millisecond {
			retryAfter = 50 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryAfter):
		}
	}
}
