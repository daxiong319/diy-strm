package mcp

import (
	"context"
	"sync"
)

// 本文件实现「依赖倒置」钩子层。
//
// 背景：部分工具需要的能力位于 internal/api 包，而 internal/api 为了注册
// MCP 路由必须 import 本包——如果本包直接调用它，就会形成循环依赖。
//
// 解法：本包只声明自己「需要什么能力」，由 internal/api 在初始化时把既有函数
// 注册进来。钩子未注册时返回明确错误，而不是静默返回空结果——后者会让 LLM
// 拿到「查到了，但是空的」这种看起来正常、实际完全错误的答案。

// ChannelSubscriptionRunner 执行一次频道订阅（可能产生转存等副作用）。
//
// summary 是给用户看的结果摘要，ok 表示本次执行是否真的做了事（例如没有新内容时为 false）。
type ChannelSubscriptionRunner func(ctx context.Context, subscriptionID uint) (summary string, ok bool, err error)

var (
	hookMu                    sync.RWMutex
	channelSubscriptionRunner ChannelSubscriptionRunner
)

// RegisterChannelSubscriptionRunner 注册频道订阅执行器。
//
// 重复注册会覆盖旧值（测试依赖这个语义），传 nil 表示注销。
func RegisterChannelSubscriptionRunner(runner ChannelSubscriptionRunner) {
	hookMu.Lock()
	defer hookMu.Unlock()
	channelSubscriptionRunner = runner
}

// runChannelSubscription 调用已注册的频道订阅执行器。
func runChannelSubscription(ctx context.Context, subscriptionID uint) (string, bool, error) {
	hookMu.RLock()
	runner := channelSubscriptionRunner
	hookMu.RUnlock()
	if runner == nil {
		return "", false, ErrRunnerNotRegistered
	}
	return runner(ctx, subscriptionID)
}

// ErrRunnerNotRegistered 表示业务执行器尚未注册。
//
// 刻意不用一个静态字符串：调用方可能需要 errors.Is 判断并给出「服务尚在启动」这类提示。
var ErrRunnerNotRegistered error = errRunnerNotRegistered{}

type errRunnerNotRegistered struct{}

func (errRunnerNotRegistered) Error() string {
	return "订阅执行器未注册（服务尚未完成初始化），请稍后重试"
}
