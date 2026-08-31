package models

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/hdhive"
	"diy-strm/internal/helpers"
)

// ---------------------------------------------------------------------------
// 影巢四通道：symedia 中转 / tgtodrive 中转 / nanshare 中转 / 官方直连。
// 上层查询与调用统一走通道抽象，按优先级调度，通道故障时逐个降级尝试。
// ---------------------------------------------------------------------------

// 通道常量
const (
	HiveChannelSymedia   = "symedia"   // 主渠道：hdhive.symedia.top（会话握手 + HMAC 签名）
	HiveChannelTgtodrive = "tgtodrive" // 备用渠道：hdhive-open.tgtodrive.top（install_id 签名，tgto123 同款）
	HiveChannelNanShare  = "nanshare"  // 中转渠道：hdhive.nanl.top（NanShare 项目签名 + 中转托管 OAuth）
	HiveChannelOfficial  = "official"  // 官方直连：hdhive.com OpenAPI（OAuth 授权码，mediavault 同款）
)

// HiveChannelLabels 通道显示名（前端展示用）
var HiveChannelLabels = map[string]string{
	HiveChannelSymedia:   "Symedia 中转",
	HiveChannelTgtodrive: "Tgtodrive 中转",
	HiveChannelNanShare:  "NanShare 中转",
	HiveChannelOfficial:  "官方直连",
}

// hiveChannelPriority 通道优先级（0 优先，用于主备调度）
func hiveChannelPriority(ch string) int {
	switch ch {
	case HiveChannelSymedia:
		return 0
	case HiveChannelTgtodrive:
		return 1
	case HiveChannelNanShare:
		return 2
	case HiveChannelOfficial:
		return 3
	}
	return 9
}

// HiveAllChannels 全部通道（按优先级）
var HiveAllChannels = []string{HiveChannelSymedia, HiveChannelTgtodrive, HiveChannelNanShare, HiveChannelOfficial}

// HiveClientForAccount 按账号通道构建客户端
func HiveClientForAccount(acc *HiveOAuthAccount) hdhive.ChannelClient {
	if acc == nil {
		return nil
	}
	switch acc.Channel {
	case HiveChannelSymedia:
		return hdhive.NewSymediaClient(acc.SymediaUserID, acc.ProxyUserKey)
	case HiveChannelNanShare:
		return hdhive.NewNanShareClient(acc.NanShareAccountID)
	case HiveChannelOfficial:
		client := hdhive.NewOfficialClient(acc.AccessToken, acc.RefreshToken, tokenExpiresOrZero(acc))
		client.OnTokenRefresh = func(accessToken, refreshToken string, expiresAt time.Time) error {
			acc.AccessToken = accessToken
			acc.RefreshToken = refreshToken
			acc.TokenExpiresAt = &expiresAt
			if err := SaveHiveAccount(acc); err != nil {
				helpers.AppLogger.Errorf("保存官方通道刷新 Token 失败：%v", err)
				return err
			}
			helpers.AppLogger.Infof("官方通道 Access Token 已刷新并持久化（过期时间 %s）", expiresAt.Format("2006-01-02 15:04:05"))
			return nil
		}
		return client
	}
	// tgtodrive 备用通道：注入 AccessToken（优先）或 InstallID 作为 Feed 授权标识，
	// 否则 HDHive 对资源查询返回误导性的 "Premium membership required"。
	oc := hdhive.NewOAuthClient(acc.InstallID)
	oc.AccessToken = acc.AccessToken
	return oc
}

func tokenExpiresOrZero(acc *HiveOAuthAccount) time.Time {
	if acc.TokenExpiresAt != nil {
		return *acc.TokenExpiresAt
	}
	return time.Time{}
}

// ---------------------------------------------------------------------------
// 通道健康度与故障切换（连续失败计数 + 冷却窗口）
// ---------------------------------------------------------------------------

var hiveChannelHealthMu sync.Mutex
var hiveChannelFails = map[string]int{}          // channel -> 连续失败次数
var hiveChannelCooldown = map[string]time.Time{} // channel -> 冷却截止时间

// 冷却时长
const (
	hiveCooldownRateLimit = 90 * time.Second // 限流：默认 90s（Retry-After 可覆盖）
	hiveCooldownAuth      = 10 * time.Minute // 授权失效：10 分钟
	hiveCooldownServer    = 60 * time.Second // 5xx / 网络：60s
)

// hiveChannelFailed 记录通道失败并设置冷却
func hiveChannelFailed(channel string, cooldown time.Duration) {
	hiveChannelHealthMu.Lock()
	defer hiveChannelHealthMu.Unlock()
	hiveChannelFails[channel]++
	hiveChannelCooldown[channel] = time.Now().Add(cooldown)
}

// hiveChannelSucceeded 记录通道成功（清零 + 解除冷却）
func hiveChannelSucceeded(channel string) {
	hiveChannelHealthMu.Lock()
	defer hiveChannelHealthMu.Unlock()
	hiveChannelFails[channel] = 0
	delete(hiveChannelCooldown, channel)
}

// HiveChannelHealth 当前各通道健康度：连续失败数 + 冷却剩余秒数（前端展示用）
func HiveChannelHealth() map[string]map[string]int64 {
	hiveChannelHealthMu.Lock()
	defer hiveChannelHealthMu.Unlock()
	now := time.Now()
	out := map[string]map[string]int64{}
	for _, ch := range HiveAllChannels {
		entry := map[string]int64{"fails": int64(hiveChannelFails[ch])}
		if until, ok := hiveChannelCooldown[ch]; ok && until.After(now) {
			entry["cooldown_seconds"] = int64(until.Sub(now).Seconds())
		} else {
			entry["cooldown_seconds"] = 0
		}
		out[ch] = entry
	}
	return out
}

// HasAuthorizedHiveChannelAccount 是否存在任一启用+已授权账号（不限通道）
func HasAuthorizedHiveChannelAccount() bool {
	var cnt int64
	db.Db.Model(&HiveOAuthAccount{}).Where("enabled = ? AND authorized = ?", true, true).Count(&cnt)
	return cnt > 0
}

// hiveChannelUsable 通道是否参与查询调度
func hiveChannelUsable(ch string) bool {
	switch ch {
	case HiveChannelSymedia, HiveChannelTgtodrive, HiveChannelNanShare, HiveChannelOfficial:
		return true
	case "":
		return true // 默认 tgtodrive
	}
	return false
}

// ListHiveAccountsForQuery 返回可用于资源查询的账号（启用+已授权），
// 按负载均衡调度排序：通道优先级 → 通道失败数升序 → 冷却中的通道靠后 → 主账号在前 → id 升序。
// 通道故障时自动逐个降级到下一通道；全部冷却时仍返回（避免无通道可用）。
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
	cooling := map[string]bool{}
	now := time.Now()
	for ch, until := range hiveChannelCooldown {
		if until.After(now) {
			cooling[ch] = true
		}
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
	// 稳定排序：优先级升序 → 冷却中靠后 → 失败数升序 → 主账号优先 → id 升序
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			ci, cj := out[i].Channel, out[j].Channel
			pi, pj := hiveChannelPriority(ci), hiveChannelPriority(cj)
			fi, fj := fails[ci], fails[cj]
			oi, oj := 0, 0
			if cooling[ci] {
				oi = 1
			}
			if cooling[cj] {
				oj = 1
			}
			if pj < pi || (pj == pi && oj < oi) ||
				(pj == pi && oj == oi && fj < fi) ||
				(pj == pi && oj == oi && fj == fi && out[j].IsMain && !out[i].IsMain) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// HiveQueryResult 资源查询结果（带使用的账号与通道，供后续详情/解锁沿用同一通道）
type HiveQueryResult struct {
	Account *HiveOAuthAccount
	Client  hdhive.ChannelClient
	Channel string
	Resp    *hdhive.OAuthAPIResponse
}

// classifyHiveChannelError 判定响应是否为通道级故障（应切换下一通道）及冷却时长。
// 通道级故障：网络错误 / HTTP≥500 / 401 / 403 / 429 / 授权缺失。
// 业务失败（success=false 但 HTTP 200 正常语义）不算通道故障。
func classifyHiveChannelError(resp *hdhive.OAuthAPIResponse, err error) (time.Duration, bool) {
	if err != nil {
		return hiveCooldownServer, true
	}
	if resp == nil {
		return hiveCooldownServer, true
	}
	if resp.StatusCode == 429 {
		// Retry-After 优先（official 客户端将其放 RateLimitIdentity: retry_after:N）
		if strings.HasPrefix(resp.RateLimitIdentity, "retry_after:") {
			if n, e := strconv.Atoi(strings.TrimPrefix(resp.RateLimitIdentity, "retry_after:")); e == nil && n > 0 && n <= 600 {
				return time.Duration(n) * time.Second, true
			}
		}
		return hiveCooldownRateLimit, true
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return hiveCooldownAuth, true
	}
	if resp.StatusCode >= 500 {
		return hiveCooldownServer, true
	}
	if !resp.Success && hiveIsFeedAuthError(resp) {
		return hiveCooldownAuth, true
	}
	// 上游限流文本（部分通道以 200 + message 形式返回限流）
	msg := strings.ToLower(resp.Message + " " + resp.Description)
	if strings.Contains(msg, "rate limit") || strings.Contains(msg, "请求过于频繁") || strings.Contains(msg, "限流") {
		return hiveCooldownRateLimit, true
	}
	return 0, false
}

// HiveQueryResourcesWithFailover 多通道资源查询：按负载均衡调度逐通道尝试，
// 通道级故障（网络/5xx/401/403/429/授权缺失）时自动逐个降级下一通道；
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
		if client == nil {
			continue
		}
		if err := hdhive.AcquireAPI(ctx); err != nil {
			return nil, err
		}
		resp, err := client.GetResources(ctx, mediaType, tmdbID)
		if cd, chFail := classifyHiveChannelError(resp, err); chFail {
			hiveChannelFailed(channel, cd)
			if err != nil {
				lastErr = fmt.Errorf("通道 %s：%v", channelLabel(channel), err)
			} else {
				lastErr = fmt.Errorf("通道 %s 响应异常（HTTP %d）：%s", channelLabel(channel), resp.StatusCode, hiveFeedErrMsg(resp))
			}
			helpers.AppLogger.Warnf("影巢通道 %s 查询失败，切换下一通道（冷却 %s）：%v", channelLabel(channel), cd, lastErr)
			continue
		}
		hiveChannelSucceeded(channel)
		return &HiveQueryResult{Account: acc, Client: client, Channel: channel, Resp: resp}, nil
	}
	if lastErr == nil {
		lastErr = errors.New("全部通道均不可用")
	}
	return nil, lastErr
}

// HiveCallWithFailover 通用通道调用（详情/解锁等）：优先使用 preferred 通道，
// 通道级故障时逐个降级尝试其余通道。返回响应与实际使用的通道名。
func HiveCallWithFailover(ctx context.Context, preferred string, call func(hdhive.ChannelClient) (*hdhive.OAuthAPIResponse, error)) (*hdhive.OAuthAPIResponse, string, error) {
	order := hiveChannelOrder(preferred)
	var lastErr error
	for _, ch := range order {
		client, _ := hiveClientForChannel(ch)
		if client == nil {
			continue
		}
		if err := hdhive.AcquireAPI(ctx); err != nil {
			return nil, "", err
		}
		resp, err := call(client)
		if cd, chFail := classifyHiveChannelError(resp, err); chFail {
			hiveChannelFailed(ch, cd)
			if err != nil {
				lastErr = fmt.Errorf("通道 %s：%v", channelLabel(ch), err)
			} else {
				lastErr = fmt.Errorf("通道 %s 响应异常（HTTP %d）：%s", channelLabel(ch), resp.StatusCode, hiveFeedErrMsg(resp))
			}
			helpers.AppLogger.Warnf("影巢通道 %s 调用失败，降级下一通道（冷却 %s）：%v", channelLabel(ch), cd, lastErr)
			continue
		}
		hiveChannelSucceeded(ch)
		return resp, ch, nil
	}
	if lastErr == nil {
		lastErr = errors.New("全部通道均不可用")
	}
	return nil, "", lastErr
}

// hiveChannelOrder 调用顺序：preferred 首位，其余按优先级 + 冷却/失败数排序；
// 无可用授权账号的通道直接跳过
func hiveChannelOrder(preferred string) []string {
	usable := make([]string, 0, len(HiveAllChannels))
	for _, ch := range HiveAllChannels {
		if _, acc := hiveClientForChannel(ch); acc == nil {
			continue
		}
		usable = append(usable, ch)
	}
	out := make([]string, 0, len(usable))
	remaining := append([]string{}, usable...)
	// preferred 置顶
	for i, ch := range remaining {
		if ch == preferred {
			out = append(out, ch)
			remaining = append(remaining[:i], remaining[i+1:]...)
			break
		}
	}
	hiveChannelHealthMu.Lock()
	fails := map[string]int{}
	for k, v := range hiveChannelFails {
		fails[k] = v
	}
	cooling := map[string]bool{}
	now := time.Now()
	for ch, until := range hiveChannelCooldown {
		if until.After(now) {
			cooling[ch] = true
		}
	}
	hiveChannelHealthMu.Unlock()
	for len(remaining) > 0 {
		best := 0
		for i := 1; i < len(remaining); i++ {
			bi, ii := remaining[best], remaining[i]
			pi, pj := hiveChannelPriority(bi), hiveChannelPriority(ii)
			oi, oj := 0, 0
			if cooling[bi] {
				oi = 1
			}
			if cooling[ii] {
				oj = 1
			}
			fi, fj := fails[bi], fails[ii]
			if pj < pi || (pj == pi && oj < oi) || (pj == pi && oj == oi && fj < fi) {
				best = i
			}
		}
		out = append(out, remaining[best])
		remaining = append(remaining[:best], remaining[best+1:]...)
	}
	return out
}

// hiveClientForChannel 取该通道的一个可用账号客户端（启用+已授权，主账号优先）
func hiveClientForChannel(ch string) (hdhive.ChannelClient, *HiveOAuthAccount) {
	var accs []HiveOAuthAccount
	if err := db.Db.Where("channel = ? AND enabled = ? AND authorized = ?", ch, true, true).
		Order("is_main desc, id asc").Find(&accs).Error; err != nil || len(accs) == 0 {
		return nil, nil
	}
	acc := &accs[0]
	client := HiveClientForAccount(acc)
	return client, acc
}

func channelLabel(ch string) string {
	if l, ok := HiveChannelLabels[ch]; ok {
		return l
	}
	return ch
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

// hiveIsFeedAuthError 判断资源查询的业务失败是否由缺少 Feed 授权导致。
// HDHive 对未带 X-HDHive-Feed-Authorization 的请求返回误导性的
// "Premium membership required" / auth_required，需要切换通道重试。
func hiveIsFeedAuthError(resp *hdhive.OAuthAPIResponse) bool {
	if resp.AuthRequired != nil && *resp.AuthRequired {
		return true
	}
	msg := strings.ToLower(resp.Message)
	if msg == "" {
		msg = strings.ToLower(resp.Description)
	}
	for _, kw := range []string{"premium", "membership", "auth_required", "authoriz", "授权", "feed", "reauth"} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// hiveFeedErrMsg 提取资源查询失败的可读信息
func hiveFeedErrMsg(resp *hdhive.OAuthAPIResponse) string {
	msg := resp.Message
	if msg == "" {
		msg = resp.Description
	}
	if msg == "" {
		msg = "请求失败"
	}
	return msg
}
