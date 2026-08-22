package models

import (
	"context"
	"errors"
	"sync"

	"diy-strm/internal/db"
	"diy-strm/internal/hdhive"
)

// ---------------------------------------------------------------------------
// 影巢双通道：symedia 中转（主渠道，握手+会话签名）与 tgtodrive 中转（备用渠道，install_id 签名）
// ---------------------------------------------------------------------------

// HiveChannelSymedia / HiveChannelTgtodrive 通道常量
const (
	HiveChannelSymedia   = "symedia"   // 主渠道：hdhive.symedia.top（会话握手 + HMAC 签名）
	HiveChannelTgtodrive = "tgtodrive" // 备用渠道：hdhive-open.tgtodrive.top（install_id 签名，tgto123 同款）
)

// hiveChannelPriority 通道优先级（0 优先，用于主备调度：symedia 主渠道在前）
func hiveChannelPriority(ch string) int {
	if ch == HiveChannelSymedia {
		return 0
	}
	return 1
}

// HiveClientForAccount 按账号通道构建客户端
func HiveClientForAccount(acc *HiveOAuthAccount) hdhive.ChannelClient {
	if acc != nil && acc.Channel == HiveChannelSymedia {
		return hdhive.NewSymediaClient(acc.SymediaUserID, acc.ProxyUserKey)
	}
	return hdhive.NewOAuthClient(acc.InstallID)
}

// ---------------------------------------------------------------------------
// 通道健康度与故障切换
// ---------------------------------------------------------------------------

var hiveChannelHealthMu sync.Mutex
var hiveChannelFails = map[string]int{} // channel -> 连续失败次数

// hiveChannelFailed 记录通道失败
func hiveChannelFailed(channel string) {
	hiveChannelHealthMu.Lock()
	defer hiveChannelHealthMu.Unlock()
	hiveChannelFails[channel]++
}

// hiveChannelSucceeded 记录通道成功（清零）
func hiveChannelSucceeded(channel string) {
	hiveChannelHealthMu.Lock()
	defer hiveChannelHealthMu.Unlock()
	hiveChannelFails[channel] = 0
}

// HiveChannelHealth 当前各通道连续失败数（前端展示用）
func HiveChannelHealth() map[string]int {
	hiveChannelHealthMu.Lock()
	defer hiveChannelHealthMu.Unlock()
	out := map[string]int{}
	for _, ch := range []string{HiveChannelSymedia, HiveChannelTgtodrive} {
		out[ch] = hiveChannelFails[ch]
	}
	return out
}

// HasAuthorizedHiveChannelAccount 是否存在任一启用+已授权账号（不限通道）
func HasAuthorizedHiveChannelAccount() bool {
	var cnt int64
	db.Db.Model(&HiveOAuthAccount{}).Where("enabled = ? AND authorized = ?", true, true).Count(&cnt)
	return cnt > 0
}

// hiveChannelUsable 通道是否参与查询调度（symedia/tgtodrive；official 等历史通道不再使用）
func hiveChannelUsable(ch string) bool {
	switch ch {
	case HiveChannelSymedia, HiveChannelTgtodrive:
		return true
	case "":
		return true // 默认 tgtodrive
	}
	return false
}

// ListHiveAccountsForQuery 返回可用于资源查询的账号（启用+已授权），
// 按主备调度排序：通道优先级（symedia 主渠道在前）→ 通道失败数升序 → 主账号在前 → id 升序，
// 保证主渠道故障时自动降级到备用渠道
func ListHiveAccountsForQuery() []*HiveOAuthAccount {
	var accs []HiveOAuthAccount
	if err := db.Db.Where("enabled = ? AND authorized = ?", true, true).
		Order("id asc").Find(&accs).Error; err != nil {
		return nil
	}
	hiveChannelHealthMu.Lock()
	fails := map[string]int{}
	for k, v := range hiveChannelFails {
		fails[k] = v
	}
	hiveChannelHealthMu.Unlock()
	out := make([]*HiveOAuthAccount, 0, len(accs))
	for i := range accs {
		a := accs[i]
		if !hiveChannelUsable(a.Channel) {
			continue
		}
		if a.Channel == "" {
			a.Channel = HiveChannelTgtodrive
		}
		out = append(out, &a)
	}
	// 稳定排序：通道优先级升序 → 通道失败数升序 → is_main 优先 → id 升序
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			pi, pj := hiveChannelPriority(out[i].Channel), hiveChannelPriority(out[j].Channel)
			fi, fj := fails[out[i].Channel], fails[out[j].Channel]
			if pj < pi || (pj == pi && fj < fi) ||
				(pj == pi && fj == fi && out[j].IsMain && !out[i].IsMain) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// HiveQueryResult 资源查询结果（带使用的账号，供后续详情/解锁沿用同一通道）
type HiveQueryResult struct {
	Account *HiveOAuthAccount
	Client  hdhive.ChannelClient
	Resp    *hdhive.OAuthAPIResponse
}

// HiveQueryResourcesWithFailover 双通道资源查询：按主备调度逐账号尝试，
// 网络错误（含超时）/ HTTP≥500 / 401（通道级故障）时自动切换下一个通道；
// 业务失败（success=false 但 HTTP 200）视为有效结果直接返回
func HiveQueryResourcesWithFailover(ctx context.Context, mediaType, tmdbID string) (*HiveQueryResult, error) {
	accs := ListHiveAccountsForQuery()
	if len(accs) == 0 {
		return nil, errors.New("没有启用中的影巢授权账号")
	}
	var lastErr error
	tried := map[string]bool{}
	for _, acc := range accs {
		channel := acc.Channel
		if channel == "" {
			channel = HiveChannelTgtodrive
		}
		if tried[channel] {
			continue // 每个通道只尝试一次（同通道多账号不解决通道故障）
		}
		tried[channel] = true
		client := HiveClientForAccount(acc)
		resp, err := client.GetResources(ctx, mediaType, tmdbID)
		if err != nil {
			hiveChannelFailed(channel)
			lastErr = err
			continue
		}
		if resp.StatusCode >= 500 || resp.StatusCode == 401 {
			hiveChannelFailed(channel)
			lastErr = errors.New("通道响应异常 HTTP " + itoa(resp.StatusCode))
			continue
		}
		hiveChannelSucceeded(channel)
		return &HiveQueryResult{Account: acc, Client: client, Resp: resp}, nil
	}
	if lastErr == nil {
		lastErr = errors.New("全部通道均不可用")
	}
	return nil, lastErr
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
