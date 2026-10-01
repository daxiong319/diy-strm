package api

import (
	"errors"
	"strings"
)

// ---------------------------------------------------------------------------
// 资源来源失败归类（D7）
//
// 背景：此前「部分来源失败」把性质完全不同的三类情况混在一起，都当成搜索错误
// 塞进 errors 数组，前端一律弹 red toast：
//
//   1. 来源压根没配置（SeedHub 未配置）——这不是错误，只是这个源没启用；
//   2. 来源配置了，但它依赖的外部进程没跑（tgto123 反代 connection refused）——
//      这是环境/依赖故障，跟搜不到资源是两回事，且用户能自己修；
//   3. 来源正常调用但上游返回错误——这才是真正的「搜索失败」。
//
// 归类放在这里而不是散在 handler 里，是为了让它成为一个可单测的纯函数。
// ---------------------------------------------------------------------------

// 资源来源失败的机器可读码（前端按码决定语气：info / warning / error）。
const (
	// CodeResourceSourceUnconfigured 来源未配置：不是失败，只是没启用。
	// 前端应静默或按「未启用」展示，绝不能弹红色错误。
	CodeResourceSourceUnconfigured = "RESOURCE_SOURCE_UNCONFIGURED"
	// CodeResourceSourceUnavailable 来源依赖的运行环境不可用（进程没起/端口不通）。
	// 用户可自行修复，提示里必须给出「哪个服务 + 怎么修」。
	CodeResourceSourceUnavailable = "RESOURCE_SOURCE_UNAVAILABLE"
	// CodeResourceSourceTimeout 来源调用超时。
	CodeResourceSourceTimeout = "RESOURCE_SOURCE_TIMEOUT"
	// CodeResourceSourceError 上游返回错误等其它真实失败。
	CodeResourceSourceError = "RESOURCE_SOURCE_ERROR"
)

// resourceSourceOutcome 单个来源的归类结果。
type resourceSourceOutcome struct {
	// Skip=true 表示这条来源不该出现在 errors 里（未配置，视为未启用）。
	Skip bool
	// Code 机器可读码，前端据此决定语气与图标。
	Code string
	// Message 面向用户的可执行提示（已把裸 dial tcp 错误翻译成「该做什么」）。
	Message string
}

// classifyResourceSourceError 把来源错误归类。
//
// source 是来源标识（re0/guanying/seedhub/tg），err 是 searchResourceBySource 的返回值。
// 纯函数：不读全局状态、不发网络请求，便于单测覆盖各类错误文本。
func classifyResourceSourceError(source string, err error) resourceSourceOutcome {
	if err == nil {
		return resourceSourceOutcome{}
	}
	msg := err.Error()

	// 1) 未配置 —— 不是失败。用类型化哨兵错误优先判定，字符串匹配只作兜底
	//    （第三方/上游返回的错误文本无法全部改造，兜底是必要的）。
	if errors.Is(err, errResourceSourceNotConfigured) || looksUnconfigured(msg) {
		return resourceSourceOutcome{Skip: true, Code: CodeResourceSourceUnconfigured, Message: msg}
	}

	// 2) 依赖不可用（进程没起、端口不通、连接被拒）
	if looksUnreachable(msg) {
		return resourceSourceOutcome{
			Code:    CodeResourceSourceUnavailable,
			Message: unreachableHint(source, msg),
		}
	}

	// 3) 超时
	if strings.Contains(msg, "超时") || strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "deadline exceeded") || strings.Contains(msg, "Client.Timeout") {
		return resourceSourceOutcome{
			Code:    CodeResourceSourceTimeout,
			Message: sourceName(source) + "查询超时：上游响应过慢或网络受限，可稍后重试",
		}
	}

	// 4) 其它 → 真实失败，保留原文（这类错误通常已经是可读的业务错误）
	return resourceSourceOutcome{Code: CodeResourceSourceError, Message: msg}
}

// errResourceSourceNotConfigured 未配置哨兵错误。来源实现可用 %w 包装它，
// 让归类不必依赖错误文案。
var errResourceSourceNotConfigured = errors.New("资源来源未配置")

// looksUnconfigured 文案兜底：这些短语表示「没配」，而不是「配了但坏了」。
func looksUnconfigured(msg string) bool {
	for _, kw := range []string{
		"未配置",
		"请先配置",
		"请先在",
		"未启用",
		"请先开启",
		"未授权",
		"auth required",
		"未登录",
		"请先登录",
		"缺少 API",
		"未接入",
	} {
		if strings.Contains(msg, kw) {
			// 「未配置」出现在「反代未配置」这类句子时，同样属于没配，可直接跳过。
			return true
		}
	}
	return false
}

// looksUnreachable 文案判定：连接层面的故障（区别于上游业务错误）。
func looksUnreachable(msg string) bool {
	for _, kw := range []string{
		"connection refused",
		"connect: connection refused",
		"dial tcp",
		"no such host",
		"i/o timeout",
		"connection reset",
		"network is unreachable",
		"拒绝连接",
		"无法连接",
		"连接失败",
		"反代不可用",
	} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// unreachableHint 把裸的连接错误翻译成「哪个服务 + 怎么修」。
func unreachableHint(source, raw string) string {
	switch source {
	case "re0":
		return "RE0 依赖本机的 tgto123 反代服务，但连接失败（" + summarizeDialError(raw) + "）。" +
			"请确认 tgto123 进程已启动并监听 127.0.0.1:12366；" +
			"若它部署在别处，请到「影视发现 - 基础配置」把反代地址改为实际地址"
	case "tg":
		return "TG 频道检索依赖 t.me 公开页面，当前网络无法访问（" + summarizeDialError(raw) + "）。" +
			"请检查本机是否能访问 t.me（常见于未配置代理或 DNS 受限）"
	case "guanying":
		return "观影服务当前不可达（" + summarizeDialError(raw) + "）。" +
			"请到「影视发现 - 基础配置」重新登录观影以刷新会话"
	case "seedhub":
		return "SeedHub 服务不可达（" + summarizeDialError(raw) + "）。" +
			"请检查「影视发现 - 基础配置」中填写的 API 地址是否正确、服务是否在运行"
	default:
		return "来源 " + source + " 依赖的服务当前不可达（" + summarizeDialError(raw) + "），请检查相关服务是否已启动"
	}
}

// summarizeDialError 从 Go 的底层网络错误里抽出最有信息量的一小段，
// 避免把整条 `Post "http://127.0.0.1:12366/api/login": dial tcp ...` 原样丢给用户。
func summarizeDialError(raw string) string {
	if i := strings.Index(raw, "connection refused"); i >= 0 {
		return "连接被拒绝"
	}
	if i := strings.Index(raw, "no such host"); i >= 0 {
		return "域名无法解析"
	}
	if i := strings.Index(raw, "i/o timeout"); i >= 0 {
		return "连接超时"
	}
	if i := strings.Index(raw, "network is unreachable"); i >= 0 {
		return "网络不可达"
	}
	// 兜底：截断原文，保持提示简短
	return truncateRunes(raw, 120)
}

// sourceName 来源中文名（用于拼提示语）
func sourceName(source string) string {
	switch source {
	case "re0":
		return "RE0"
	case "guanying":
		return "观影"
	case "seedhub":
		return "SeedHub"
	case "tg":
		return "TG 频道"
	}
	return source
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
