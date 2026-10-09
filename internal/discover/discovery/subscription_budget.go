package discovery

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"litepan/internal/settings"
	"litepan/pkg/timeutil"
)

// T06 · 订阅执行强度四档 + 时段窗口 + 完结宽限。
//
// 三件事合在一个文件里，因为它们是同一个决策的三个维度：
// 「这一轮要不要跑、跑几次、跑多密」。

// ---------------------------------------------------------------------------
// 一、执行强度四档
// ---------------------------------------------------------------------------

// T06 设置项 key。直接引用 internal/settings 的常量，避免两处字面量漂移 ——
// 漂移的表现是「配置改了不生效」，排查成本极高。
//
// ⚠️ 读法：**不能**用本包的 SettingString / SettingInt。
// 那两个函数读的是 discovery 自己的 discovery_settings 表 + DefaultSettings() 白名单，
// 而 mo_* 系列的键由 internal/settings 的注册表服务持有（管理界面写的就是那边）。
// 走错读取通道 = 用户在界面上改了配置、运行时永远读默认值 —— 这正是 T06
// 写第一版时踩的坑。所以这里统一走 currentSettings()（settings.Service）。
const (
	KeySubscriptionExecutionMode     = settings.KeyMOSubscriptionExecutionMode
	KeySubscriptionCustomAttempts    = settings.KeyMOSubscriptionCustomAttempts
	KeySubscriptionCustomIntervalSec = settings.KeyMOSubscriptionCustomIntervalSec
	KeySubscriptionCustomJitterSec   = settings.KeyMOSubscriptionCustomJitterSec
	KeySubscriptionTimeWindows       = settings.KeyMOSubscriptionTimeWindows
	KeySubscriptionFinishedGraceDays = settings.KeyMOSubscriptionFinishedGraceDays
)

// guardrailString 读一个 T06 字符串设置项。
//
// 服务未装配时返回 def（而不是 panic）：单测与早期装配阶段都可能没有设置服务。
func guardrailString(key, def string) string {
	svc := currentSettings()
	if svc == nil {
		return def
	}
	if v := strings.TrimSpace(svc.StringAllowEmpty(key)); v != "" {
		return v
	}
	return def
}

// guardrailInt 读一个 T06 整数设置项。
func guardrailInt(key string, def int) int {
	svc := currentSettings()
	if svc == nil {
		return def
	}
	// settings.Service.Int 自己会回落到 registry 里的 Default，
	// 注册表里写了默认值所以这里不必再兜一层。
	return svc.Int(key)
}

// 执行强度档位。取值就是设置项 mo_subscription_execution_mode 的可选值。
const (
	ModeConservative = "conservative" // 保守
	ModeBalanced     = "balanced"     // 均衡（默认）
	ModeAggressive   = "aggressive"   // 激进
	ModeCustom       = "custom"       // 自定义
)

// executionProfile 一次执行强度的完整参数。
type executionProfile struct {
	Mode     string
	Attempts int
	// IntervalMin/IntervalMax 是一次转存尝试之间的间隔区间（含抖动）。
	// 实际间隔在区间内随机取，避免固定节奏被风控识别。
	IntervalMin time.Duration
	IntervalMax time.Duration
}

// 逆向确认：参考实现 官网 docs 逐字给出四档的尝试数与间隔区间。
// 这一组数字不是推测值，改动前请先改 docs 口径。
var (
	profileConservative = executionProfile{Mode: ModeConservative, Attempts: 1,
		IntervalMin: 45 * time.Second, IntervalMax: 65 * time.Second}
	profileBalanced = executionProfile{Mode: ModeBalanced, Attempts: 2,
		IntervalMin: 25 * time.Second, IntervalMax: 40 * time.Second}
	profileAggressive = executionProfile{Mode: ModeAggressive, Attempts: 3,
		IntervalMin: 10 * time.Second, IntervalMax: 18 * time.Second}
)

const (
	defaultExecutionMode         = ModeBalanced
	defaultCustomAttempts        = 2
	defaultCustomIntervalSeconds = 30
	defaultCustomJitterSeconds   = 10
	defaultFinishedGraceDays     = 7
	maxExecutionAttempts         = 10
	minExecutionIntervalSeconds  = 1
	maxExecutionIntervalSeconds  = 600
)

// ErrBudgetExhausted 表示本轮执行次数已用尽。
var ErrBudgetExhausted = errors.New("本轮订阅执行次数已用尽")

// resolveExecutionProfile 读设置得出执行强度。
// 宽限期强制降到保守档（参考实现 口径：完结宽限期内继续搜但强度最低）。
func resolveExecutionProfile(inGrace bool) executionProfile {
	p := presetProfile(guardrailString(KeySubscriptionExecutionMode, defaultExecutionMode))
	if p.Mode == "" {
		p = profileBalanced
	}
	if inGrace && p.Attempts > profileConservative.Attempts {
		// 宽限期内降档：即便用户选了「激进」，宽限期也只按保守档走。
		p = profileConservative
	}
	return p
}

// presetProfile 按档位名取预设。自定义档不设默认区间（由配置给出）。
func presetProfile(mode string) executionProfile {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case ModeConservative:
		return profileConservative
	case ModeAggressive:
		return profileAggressive
	case ModeCustom:
		return customProfile()
	case ModeBalanced, "":
		return profileBalanced
	}
	return executionProfile{}
}

// customProfile 自定义档：尝试数来自 mo_subscription_custom_attempts，
// 间隔基准来自 mo_subscription_custom_interval_sec，
// 抖动来自 mo_subscription_custom_jitter_sec（基准 + [0, 抖动) 的随机）。
func customProfile() executionProfile {
	attempts := clampInt(guardrailInt(KeySubscriptionCustomAttempts, defaultCustomAttempts), 1, maxExecutionAttempts)
	base := clampInt(guardrailInt(KeySubscriptionCustomIntervalSec, defaultCustomIntervalSeconds),
		minExecutionIntervalSeconds, maxExecutionIntervalSeconds)
	jitter := clampInt(guardrailInt(KeySubscriptionCustomJitterSec, defaultCustomJitterSeconds), 0, 600)
	return executionProfile{
		Mode:        ModeCustom,
		Attempts:    attempts,
		IntervalMin: time.Duration(base) * time.Second,
		IntervalMax: time.Duration(base+jitter) * time.Second,
	}
}

// runBudget 单订阅单轮的执行预算。
//
// 关键语义（任务书逐条要求的）：
//   - **边扫边转**：判定通过即刻发起转存，不攒够一批再一起转；
//   - **被筛掉的不占尝试次数**：Acquire 只在真正要转存前调用，
//     词表过滤 / 去重 / 身份校验挡下的候选一次都不消耗预算；
//   - **已发起但失败的仍计数**：失败在 Acquire 之后发生，计数已经加上。
//
// now/wait/rand 全部可注入，测试里用假时钟把 45~65s 的等待跑成零耗时，
// 同时仍能断言「实际请求的间隔落在区间内」。
type runBudget struct {
	profile executionProfile
	used    int
	// lastAttemptAt 是上一次**尝试**的时刻（不是上一轮开始时刻）。
	lastAttemptAt time.Time
	now           func() time.Time
	wait          func(context.Context, time.Duration) error
	rand          func(int) int
	// granted 记录每次实际放行的间隔，测试用；nil 时不记录。
	granted []time.Duration
}

func newRunBudget(p executionProfile) *runBudget {
	return &runBudget{
		profile: p,
		now:     time.Now,
		wait:    waitContext,
		rand:    randInt,
	}
}

func waitContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// randInt 返回 [0, n) 的随机整数。用 crypto/rand 而不是 math/rand：
// 转存间隔的抖动不需要密码学强度，但用全局 math/rand 会和仓库其它地方
// 共享种子源，测试里为其它用途 seed 过就会让这里失去随机性。
func randInt(n int) int {
	if n <= 1 {
		return 0
	}
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0
	}
	return int(binary.BigEndian.Uint64(buf[:]) % uint64(n))
}

// remaining 返回本轮还剩几次尝试。
func (b *runBudget) remaining() int {
	if b.profile.Attempts <= b.used {
		return 0
	}
	return b.profile.Attempts - b.used
}

// interval 抽一次随机间隔。
func (b *runBudget) interval() time.Duration {
	span := int(b.profile.IntervalMax - b.profile.IntervalMin)
	if span <= 0 {
		return b.profile.IntervalMin
	}
	return b.profile.IntervalMin + time.Duration(b.rand(span))
}

// Acquire 申请一次转存尝试。超出预算返回 ErrBudgetExhausted。
//
// 第一次不等待 —— 一轮刚开始时没有任何节奏问题。
// 之后的间隔从**上一次尝试时刻**算起：如果上一次转存本身耗时超过了间隔
// （大文件、网络慢），就不再额外等待，否则等于把转存耗时又叠加了一次间隔。
func (b *runBudget) Acquire(ctx context.Context) error {
	if b.used >= b.profile.Attempts {
		return ErrBudgetExhausted
	}
	now := b.now()
	if b.used > 0 && !b.lastAttemptAt.IsZero() {
		gap := b.interval()
		if d := b.lastAttemptAt.Add(gap).Sub(now); d > 0 {
			if err := b.wait(ctx, d); err != nil {
				return err
			}
		}
		b.granted = append(b.granted, gap)
	}
	b.used++
	b.lastAttemptAt = b.now()
	return nil
}

func (b *runBudget) describe() string {
	return fmt.Sprintf("模式 %s，本轮 %d 次尝试，间隔 %ds~%ds",
		b.profile.Mode, b.profile.Attempts,
		int(b.profile.IntervalMin.Seconds()), int(b.profile.IntervalMax.Seconds()))
}

// enforceGrace 把预算降到保守档（完结宽限期内调用）。
//
// 只降**未来**的尝试：已经用掉的次数不退还，也不把 used 往下压 —
// 已经转存出去的东西没法「收回」，把它算进本轮如实反映这一轮的实际强度。
//
// 如果降档前已经用得比保守档还多（激进档用了 2 次），保守档的 1 次配额已经被
// 吃掉，remaining() 归零 ⇒ 本轮不再新增尝试。宽限期的语义是「少量继续搜」，
// 不是「继续搜」，所以这个结果是刻意的。
func (b *runBudget) enforceGrace() {
	b.profile = profileConservative
}

// ---------------------------------------------------------------------------
// 二、时段窗口
// ---------------------------------------------------------------------------

// timeWindows 解析后的时段窗口列表。
type timeWindows []struct{ Start, End string }

// parseTimeWindows 解析 mo_subscription_time_windows。
// 格式：`HH:MM-HH:MM`，多个用逗号、分号或换行分隔，例如：
//
//	00:00-08:00,23:00-06:00
//
// 跨午夜（结束早于开始）由 timeutil.InClockWindow 自己处理，这里不判断。
// 空配置 = 不限时段。
func parseTimeWindows(raw string) timeWindows {
	var out timeWindows
	for _, item := range splitWindowRaw(raw) {
		lo, hi, ok := strings.Cut(item, "-")
		if !ok {
			continue
		}
		start := strings.TrimSpace(lo)
		end := strings.TrimSpace(hi)
		if _, _, ok := timeutil.ParseClock(start); !ok {
			continue
		}
		if _, _, ok := timeutil.ParseClock(end); !ok {
			continue
		}
		out = append(out, struct{ Start, End string }{Start: start, End: end})
	}
	return out
}

func splitWindowRaw(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', '，', ';', '；', '\n', '\r', '|', '｜':
			return true
		}
		return false
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// outsideTimeWindows 判断 now 是否落在所有时段之外。
//
// 语义与 参考实现 一致：**时段外直接跳过，不补跑**。
// 补跑会在恢复时段的第一分钟把所有积压的候选一起转存，
// 这种突发流量正是网盘风控最容易盯上的形态，所以宁可少转也不补。
//
// 空配置 / 全是无效配置 = 不限制（fail-open），否则一条写错的配置会让订阅彻底不动。
func outsideTimeWindows(windows timeWindows, now time.Time) bool {
	if len(windows) == 0 {
		return false
	}
	for _, w := range windows {
		if timeutil.InClockWindow(w.Start, w.End, now) {
			return false
		}
	}
	return true
}

// triggerExemptFromWindow 判断这次触发是否豁免时段窗口。
//
// 逆向确认：参考实现 docs 明确「时段外到点那轮直接跳过不补跑，也不计入自动补找次数；
// **手动『立即搜索』和新建订阅首次搜索不受限**」。
// 所以只有定时触发受限，手动与 MCP 触发一律放行。
func triggerExemptFromWindow(trigger string) bool {
	switch strings.ToLower(strings.TrimSpace(trigger)) {
	case "scheduled", "":
		return false
	}
	return true
}

// describeTimeWindows 把时段配置渲染成人读的文本，用于日志与订阅运行详情。
func describeTimeWindows(windows timeWindows) string {
	if len(windows) == 0 {
		return "不限"
	}
	parts := make([]string, 0, len(windows))
	for _, w := range windows {
		parts = append(parts, w.Start+"-"+w.End)
	}
	return strings.Join(parts, ",")
}

// 时段外跳过时的固定日志/事件文案。测试按关键字断言，避免文案微调就打挂。
const timeWindowSkipMessage = "时段外跳过"

// ---------------------------------------------------------------------------
// 三、完结宽限
// ---------------------------------------------------------------------------

// finishedGraceDays 返回完结宽限天数。0 表示不启用宽限。
func finishedGraceDays() int {
	return clampInt(guardrailInt(KeySubscriptionFinishedGraceDays, defaultFinishedGraceDays), 0, 3650)
}
