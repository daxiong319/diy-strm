package models

import (
	"context"
	"errors"
	"sync"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/hdhive"
)

// ---------------------------------------------------------------------------
// 影巢双通道：tgtodrive 中转（install_id 签名）与官方 OpenAPI（X-API-Key + Bearer）
// ---------------------------------------------------------------------------

// 官方通道配置存于 cloud_settings（source=hdhive）
const (
	CloudSettingKeyHiveOfficialClientID   = "official_client_id"
	CloudSettingKeyHiveOfficialAppSecret  = "official_app_secret"
	CloudSettingKeyHiveOfficialBaseURL    = "official_base_url"
	CloudSettingKeyHiveOfficialRedirectURI = "official_redirect_uri"
)

// HiveChannelTgtodrive / HiveChannelOfficial 通道常量
const (
	HiveChannelTgtodrive = "tgtodrive"
	HiveChannelOfficial  = "official"
)

// GetHiveOfficialConfig 读取官方通道配置
func GetHiveOfficialConfig() hdhive.OfficialConfig {
	get := func(key string) string {
		v, err := GetCloudSetting("hdhive", key)
		if err != nil {
			return ""
		}
		return v
	}
	return hdhive.OfficialConfig{
		BaseURL:     get(CloudSettingKeyHiveOfficialBaseURL),
		ClientID:    get(CloudSettingKeyHiveOfficialClientID),
		AppSecret:   get(CloudSettingKeyHiveOfficialAppSecret),
		RedirectURI: get(CloudSettingKeyHiveOfficialRedirectURI),
	}
}

// SetHiveOfficialConfig 保存官方通道配置（空字段跳过）
func SetHiveOfficialConfig(clientID, appSecret, baseURL, redirectURI string) error {
	pairs := map[string]string{
		CloudSettingKeyHiveOfficialClientID:    clientID,
		CloudSettingKeyHiveOfficialAppSecret:   appSecret,
		CloudSettingKeyHiveOfficialBaseURL:     baseURL,
		CloudSettingKeyHiveOfficialRedirectURI: redirectURI,
	}
	for k, v := range pairs {
		if v == "" {
			continue
		}
		if err := SetCloudSetting("hdhive", k, v); err != nil {
			return err
		}
	}
	return nil
}

// HiveClientForAccount 按账号通道构建客户端
// official 通道注入账号 token，并在刷新成功后回写数据库
func HiveClientForAccount(acc *HiveOAuthAccount) hdhive.ChannelClient {
	if acc != nil && acc.Channel == HiveChannelOfficial {
		cfg := GetHiveOfficialConfig()
		client := hdhive.NewOfficialClient(cfg)
		client.AccessToken = acc.AccessToken
		client.RefreshToken = acc.RefreshToken
		client.OnTokenRefreshed = func(access, refresh string, expiresIn int) {
			acc.AccessToken = access
			acc.RefreshToken = refresh
			if expiresIn > 0 {
				t := time.Now().Add(time.Duration(expiresIn) * time.Second)
				acc.TokenExpiresAt = &t
			}
			_ = db.Db.Save(acc).Error
		}
		return client
	}
	return hdhive.NewOAuthClient(acc.InstallID)
}

// ---------------------------------------------------------------------------
// 通道健康度与故障切换
// ---------------------------------------------------------------------------

var hiveChannelHealthMu sync.Mutex
var hiveChannelFails = map[string]int{} // channel -> 连续失败次数

// hiveChannelFailbadChannel 记录通道失败
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
	for _, ch := range []string{HiveChannelTgtodrive, HiveChannelOfficial} {
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

// ListHiveAccountsForQuery 返回可用于资源查询的账号（启用+已授权），
// 按通道健康度排序（连续失败少的通道优先；同通道内主账号在前），保证双通道互为备份
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
		if a.Channel == "" {
			a.Channel = HiveChannelTgtodrive
		}
		out = append(out, &a)
	}
	// 稳定排序：通道失败数升序 → is_main 优先 → id 升序
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			fi, fj := fails[out[i].Channel], fails[out[j].Channel]
			if fj < fi || (fj == fi && out[j].IsMain && !out[i].IsMain) {
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

// HiveQueryResourcesWithFailover 双通道资源查询：按健康度逐账号尝试，
// 网络错误 / HTTP≥500 / 401（通道级故障）时自动切换下一个通道；
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
