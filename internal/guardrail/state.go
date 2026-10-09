package guardrail

import (
	"strconv"
	"time"
)

// State 是落库的熔断状态。
//
// 形态是「每个作用域一行的小 JSON」，与已有的 account_auth_states 一致。
// 刻意不建新表：熔断状态只有暂停到期时间、原因、触发时刻三个字段，
// 为此多一个迁移 + 一个仓库文件 + 一处备份白名单判断，收益只有
// 「不用 encode/decode」这一点。
type State struct {
	// PausedUntil 是暂停到期时间。零值表示没在暂停。
	PausedUntil time.Time `json:"paused_until,omitempty"`
	// Reason 是暂停原因，跨重启后仍要能显示给用户。
	Reason string `json:"reason,omitempty"`
	// TriggeredAt 是本次暂停的触发时刻。
	TriggeredAt time.Time `json:"triggered_at,omitempty"`
	// PersistFailed 标记这次熔断没能落库。
	//
	// 字段本身会在下次成功写入时自然消失；它存在的原因是一个真实的坑：
	// 落库失败时熔断**不会**被撤销（见 trip 的注释），所以如果服务此刻重启，
	// 这次熔断就丢了，熔断形同虚设。把这个标记落进状态本身，
	// 管理台才能提示「这次暂停没存下来，重启后可能不生效」。
	PersistFailed bool `json:"persist_failed,omitempty"`
}

// itoa 是本地的小整数转字符串，避免为一个转换拉 strconv 进主文件。
// 负数在这里没有语义，但仍然正确处理，免得将来有人传进来一个负的计数。
func itoa(v int64) string { return strconv.FormatInt(v, 10) }
