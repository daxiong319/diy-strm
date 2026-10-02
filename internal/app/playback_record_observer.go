package app

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"litepan/internal/domain"
	"litepan/internal/playback"
	"litepan/internal/playbackrecord"
)

// playbackRecordObserver 把取流入口的 302 回调翻译成一条播放记录。
// 对应老版 emby302/service/emby/strm_redirect.go 的 recordStrmPlayback：
// UserId/Client/DeviceId 取自请求查询串，StrmPath 取文件名，ItemName 取文件名基名。
// 落库走异步协程，任何错误都不影响重定向本身。
type playbackRecordObserver struct {
	svc *playbackrecord.Service
	// accounts 用于把云盘账号 ID 翻译成网盘类型（quark/aliyun…）。
	// 允许为 nil：此时 provider 回落为空串。
	accounts domain.AccountRepository
	log      func(error)
}

// newPlaybackRecordObserver 基于播放记录服务构造观察者。
func newPlaybackRecordObserver(svc *playbackrecord.Service) playback.RedirectObserver {
	obs := &playbackRecordObserver{svc: svc}
	return obs.observe
}

// newPlaybackRecordObserverWithAccounts 带账号仓储的版本，可填充 provider。
func newPlaybackRecordObserverWithAccounts(svc *playbackrecord.Service, accounts domain.AccountRepository) playback.RedirectObserver {
	obs := &playbackRecordObserver{svc: svc, accounts: accounts}
	return obs.observe
}

// observe 实现 playback.RedirectObserver。
func (o *playbackRecordObserver) observe(r *http.Request, accountID int64, res playback.Resolved, intent playback.Intent) {
	if o == nil || o.svc == nil {
		return
	}
	// 文件名优先取意图里的展示名（STRM 播放会传入文件名），
	// 其次取解析出来的远端文件名。
	name := strings.TrimSpace(intent.FileName)
	if name == "" {
		name = strings.TrimSpace(res.File.Name)
	}
	if name == "" {
		name = strings.TrimSpace(res.Link.FileName)
	}
	path := strings.TrimSpace(name)

	entry := &playbackrecord.Record{
		RuleID:     "1",
		UserID:     queryOf(r, "UserId"),
		Client:     queryOf(r, "Client"),
		DeviceID:   queryOf(r, "DeviceId"),
		ItemName:   baseName(path),
		StrmPath:   path,
		Provider:   o.providerOf(accountID),
		PlaybackAt: time.Now(),
	}
	if entry.UserID == "" && entry.ItemName == "" && entry.StrmPath == "" {
		// 播放记录服务自身也会跳过全空记录，这里提前返回避免无意义协程。
		return
	}
	// 落库由 playbackrecord.Service 内部另起协程，不会阻塞重定向。
	o.svc.Record(context.Background(), entry)
}

// providerOf 把云盘账号 ID 翻译成网盘类型（quark/aliyun…）。
// 账号仓储缺失或查询失败时回落为空串，不影响记录落库。
func (o *playbackRecordObserver) providerOf(accountID int64) string {
	if o.accounts == nil || accountID <= 0 {
		return ""
	}
	acc, err := o.accounts.Get(context.Background(), accountID)
	if err != nil {
		if o.log != nil {
			o.log(err)
		}
		return ""
	}
	if acc == nil {
		return ""
	}
	return strings.TrimSpace(acc.DriverType)
}

// queryOf 容错读取查询参数。
func queryOf(r *http.Request, key string) string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.URL.Query().Get(key))
}

// baseName 取路径基名，空值返回空串。
func baseName(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return base
}

// accountIDText 便于日志/调试时展示账号。
func accountIDText(id int64) string {
	if id <= 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}
