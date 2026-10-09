// Package guardrail 实现「目录监控风控三件套」，目的是别把网盘账号用废。
//
// 三条熔断里，**哪些数字是对方文档确认的、哪些是我们自己定的**，必须写清楚，
// 否则以后没人分得清「这是有依据的约束」还是「这是随手填的默认值」：
//
//	① 连续调用接口次数超限 → 暂停。**上限 86400 秒（24 小时）是文档确认的。**
//	   「窗口多长」「触顶后暂停多久」文档没给，我们默认 3600 秒窗口 / 3600 秒暂停，
//	   理由是窗口太短会把正常的批量扫描误判成连续调用。
//	② 连续整理时长超限 → 暂停。**上限 1440 分钟（24 小时）是文档确认的。**
//	   「阈值多少」文档没给，默认 0（不限）；「暂停多久」默认 60 分钟，是我们定的。
//	③ 媒体文件最小体积 —— move 模式下低于阈值的文件被**移走**。
//	   阈值默认 0（不启用）是我们定的，因为默认开启会静默搬走用户的文件。
//
// 关于第 ③ 条为什么是「移走」而不是「跳过」：孤儿目录清理依赖目录里没有杂物，
// 一个几百 KB 的 .nfo 或 .txt 会让目录永远不被判定为空。
//
// 关于「移走」而不是「删掉」：文档写的是删除，但账号被封的代价远高于多占一点空间，
// 所以这里移到隔离目录，用户可以在管理台看到并搬回去。真删是用户的明确选择，
// 不是默认行为。
//
// 熔断状态必须持久化，否则「重启服务」就成了绕过风控的后门 —— 而这恰恰是
// 账号被封最常见的原因之一（用户觉得卡，重启，再卡，再重启）。
package guardrail

import (
	"time"
)

// 上限常量。文档确认的两个数字放在这里并单独注释，便于日后核对。
const (
	// MaxPauseSecondsCap 是调用次数熔断的暂停时长上限，文档确认为 86400 秒。
	MaxPauseSecondsCap = 86400
	// MaxPauseMinutesCap 是整理时长熔断的暂停时长上限，文档确认为 1440 分钟。
	MaxPauseMinutesCap = 1440
)

// Guardrail 是三条熔断的阈值集合。零值表示不启用对应的那条。
type Guardrail struct {
	// MaxCallsPerWindow 是统计窗口内允许的接口调用次数上限，0 表示不限。
	MaxCallsPerWindow int
	// CallWindowSeconds 是这个次数在多长的窗口内累计。<=0 时按 DefaultCallWindowSeconds。
	CallWindowSeconds int
	// MaxPauseSeconds 是调用次数触顶后的暂停秒数，会被截到 MaxPauseSecondsCap。
	MaxPauseSeconds int
	// MaxWorkMinutes 是一轮连续整理的时长上限（分钟），0 表示不限。
	MaxWorkMinutes int
	// MaxPauseMinutes 是整理时长触顶后的暂停分钟数，会被截到 MaxPauseMinutesCap。
	MaxPauseMinutes int
	// MinMediaSizeBytes 是 move 模式下小文件被移走的体积阈值，0 表示不启用。
	MinMediaSizeBytes int64
}

// 以下默认值是**我们自己定的**，不是文档确认的。
const (
	// DefaultCallWindowSeconds 是调用次数的默认统计窗口（1 小时）。
	DefaultCallWindowSeconds = 3600
	// DefaultCallPauseSeconds 是调用次数触顶后的默认暂停秒数（1 小时）。
	DefaultCallPauseSeconds = 3600
	// DefaultWorkPauseMinutes 是整理时长触顶后的默认暂停分钟数。
	DefaultWorkPauseMinutes = 60
)

// normalized 把配置收进合法范围。调用方**必须**用它的返回值而不是原值：
// 上限截断与窗口兜底都在这里做，漏了这一步就等于没有上限。
func (g Guardrail) normalized() Guardrail {
	if g.CallWindowSeconds <= 0 {
		g.CallWindowSeconds = DefaultCallWindowSeconds
	}
	if g.MaxPauseSeconds <= 0 {
		g.MaxPauseSeconds = DefaultCallPauseSeconds
	}
	if g.MaxPauseSeconds > MaxPauseSecondsCap {
		g.MaxPauseSeconds = MaxPauseSecondsCap
	}
	if g.MaxPauseMinutes <= 0 {
		g.MaxPauseMinutes = DefaultWorkPauseMinutes
	}
	if g.MaxPauseMinutes > MaxPauseMinutesCap {
		g.MaxPauseMinutes = MaxPauseMinutesCap
	}
	if g.MaxCallsPerWindow < 0 {
		g.MaxCallsPerWindow = 0
	}
	if g.MaxWorkMinutes < 0 {
		g.MaxWorkMinutes = 0
	}
	if g.MinMediaSizeBytes < 0 {
		g.MinMediaSizeBytes = 0
	}
	return g
}

// CallLimitReached 判断当前窗口内的调用次数是否已经触顶。
func (g Guardrail) CallLimitReached(callsInWindow int) bool {
	g = g.normalized()
	if g.MaxCallsPerWindow <= 0 {
		return false
	}
	return callsInWindow >= g.MaxCallsPerWindow
}

// CallPauseDuration 是调用次数熔断后的暂停时长。
func (g Guardrail) CallPauseDuration() time.Duration {
	return time.Duration(g.normalized().MaxPauseSeconds) * time.Second
}

// CallWindow 是调用次数的统计窗口。
func (g Guardrail) CallWindow() time.Duration {
	return time.Duration(g.normalized().CallWindowSeconds) * time.Second
}

// WorkPauseDuration 是整理时长熔断后的暂停时长。
func (g Guardrail) WorkPauseDuration() time.Duration {
	return time.Duration(g.normalized().MaxPauseMinutes) * time.Minute
}

// WorkLimitReached 判断一轮整理是否超过时长上限。
func (g Guardrail) WorkLimitReached(elapsed time.Duration) bool {
	if g.MaxWorkMinutes <= 0 {
		return false
	}
	return elapsed > time.Duration(g.MaxWorkMinutes)*time.Minute
}

// ShouldQuarantineSmallFile 判断一个文件是否该被移到隔离目录。
//
// 三个条件同时成立才搬：启用了阈值、处于 move 模式、体积真的低于阈值。
// 「copy 模式不搬」是有意的 —— copy 的语义就是源文件不动，任何情况下都不该动它。
func (g Guardrail) ShouldQuarantineSmallFile(size int64, moveMode bool) bool {
	if g.MinMediaSizeBytes <= 0 || !moveMode {
		return false
	}
	return size < g.MinMediaSizeBytes
}

// Enabled 报告是否有任何一条熔断或小文件处理被启用，用于界面决定要不要显示警示。
func (g Guardrail) Enabled() bool {
	return g.MaxCallsPerWindow > 0 || g.MaxWorkMinutes > 0 || g.MinMediaSizeBytes > 0
}
