package playmonitor

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"litepan/internal/domain"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// 三态判定：整个 T11 里最容易做错的一块，单独成文件以便针对性用例
// ─────────────────────────────────────────────────────────────────────────────

// StreamOrigin 流代理分支的两种取流形态。写进 StreamEvent.RequestType，
// 也是 playback_records.request_type 的取值。
// StreamEvent 一次播放请求的判定结果（由 internal/playback 的取流入口构造）。
type StreamEvent = domain.StreamEvent

const (
	// RequestTypeStream 字节流经自己服务器（外网时 → 计费中）。
	RequestTypeStream = "stream"
	// RequestTypeRedirect 302 跳网盘直链（外网时 → CDN 直连，计 0）。
	RequestTypeRedirect = "redirect"
)

// classifyState 三态判定的唯一入口。
//
//	lan == true                    → 局域网        （不计费）
//	lan == false && origin == true → 计费中        （计费）
//	lan == false && origin == false→ CDN 直连      （计 0）
//
// origin 即「字节流是否经过自己的服务器」，由 internal/playback.PickAction
// 的结果决定：ActionStream → true，ActionRedirect → false。
//
// **判定顺序是硬要求**：局域网必须先判，因为它一旦成立就不再产生上行
// 流量，origin 是 true 还是 false 都不该影响结果。写成
// `if origin { ... } else if lan { ... }` 会让内网流代理会话被记成计费中。
func classifyState(lan, origin bool) domain.PlayState {
	if lan {
		return domain.PlayStateLAN
	}
	if origin {
		return domain.PlayStateMetered
	}
	return domain.PlayStateCDN
}

// bitrateFromContentLength 由 Content-Length 估算码率（bps）。
// 播放器不发 Content-Range 时用它兜底，算不出来就返回 0（不猜）。
func bitrateFromContentLength(total, seconds int64) int64 {
	if total <= 0 || seconds <= 0 {
		return 0
	}
	return total / seconds * 8
}

// rangeBitrate 由 Range 头估算码率（bps）。
//
// 播放器每段取流通常固定长度（比如 4MB/4s ≈ 8Mbps），
// 用「本段字节数 × 8 / 本段耗时」比按整个文件长度算准得多 ——
// 一个 40GB 的电影按总长平均算会得到几 Kbps 的荒谬值。
func rangeBitrate(byteCount int64, elapsed time.Duration) int64 {
	if byteCount <= 0 || elapsed <= 0 {
		return 0
	}
	secs := elapsed.Seconds()
	if secs <= 0 {
		return 0
	}
	return int64(float64(byteCount) * 8 / secs)
}

// dedupKey 会话去重键：同设备 + 同片 + 同位置。
//
// 对应 参考实现「同一台设备在同一部片的同一位置停下，只会转交���次停止事件」。
// 播放器暂停后会在后台零星取一点数据，用这个键把这些抖动合并成同一次播放。
func dedupKey(deviceID, itemName string) string {
	device := normalizeKeyPart(deviceID)
	if device == "" {
		device = "-"
	}
	return fmt.Sprintf("%s|%s", device, itemName)
}

// normalizeKeyPart 把 Client/Device/User-Agent 归一到可比较的形式。
//
// Web 客户端的 UA 里带版本号（ExoPlayerLib/2.19.1），每次播放器升级
// 都会变，混进去会导致同一台设备被当成两台。去数字与易变段。
func normalizeKeyPart(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			// 保留数字，但连续数字段（版本号）压缩成一个 #。
			b.WriteByte('#')
		case r == '.' || r == '-' || r == '_' || r == '/' || r == ':':
			// 版本号分隔符直接吞掉，让 2.19.1 与 2.20.3 归一到同一个形状。
			b.WriteByte('#')
		case r == ' ':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	for strings.Contains(out, "##") {
		out = strings.ReplaceAll(out, "##", "#")
	}
	return strings.Trim(out, "# ")
}

// hashKey 生成稳定的短 ID（hex），用于会话 ID 与停播去噪 map 的键。
func hashKey(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:12])
}
