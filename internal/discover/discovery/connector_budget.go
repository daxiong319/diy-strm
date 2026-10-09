package discovery

import (
	"time"

	"litepan/internal/settings"
)

// T05 · 连接器的两个时间预算。
//
// 为什么要分开：单源超时防的是「某一家源挂住把整轮订阅拖死」，
// 总预算防的是「八家源串行挨个跑完各自的超时，一轮订阅跑一小时」。
// 只留单源超时的话，总时长是 Σ(per-source timeout) —— 源越多越不可控；
// 只留总预算的话，第一家源不返回就一直占着，后面几家永远轮不到。
// 两个都要，且**总预算必须包含单源超时**：超预算时不是等它跑完，
// 而是直接跳过剩余源（见 connector.SearchAll）。
//
// 两个值都在 settings registry 注册（0~300 / 0~1800，Set 时归一），
// 这里再做一次兜底防御：值被绕过 registry 直接写进库时不能返回 0/负数，
// 那会让「超时」变成「立即放弃」。

// ConnectorTimeout 单个搜索源的超时上限。
func ConnectorTimeout() time.Duration {
	secs := guardrailInt(settings.KeyMOConnectorTimeoutSeconds, defaultConnectorTimeoutSeconds)
	if secs <= 0 {
		secs = defaultConnectorTimeoutSeconds
	}
	return time.Duration(secs) * time.Second
}

// ConnectorSearchBudget 一次订阅检索所有源的总时间预算。
func ConnectorSearchBudget() time.Duration {
	secs := guardrailInt(settings.KeyMOConnectorSearchBudgetSeconds, defaultConnectorBudgetSeconds)
	if secs <= 0 {
		secs = defaultConnectorBudgetSeconds
	}
	return time.Duration(secs) * time.Second
}

const (
	// 与 settings/registry.go 里注册项的默认值保持一致（30s / 45s）。
	defaultConnectorTimeoutSeconds = 30
	defaultConnectorBudgetSeconds  = 45
)
