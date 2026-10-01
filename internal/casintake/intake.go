// Package casintake 负责从通知渠道（当前为 Telegram Bot）接收用户发来的 .cas 文件，
// 并交给 cas 包按网盘类型自动转存到用户配置的保存目录。
//
// 通知渠道原本只有"sender"（向外发送），此包补齐"receiver"侧：
// 手写 getUpdates 长轮询（不引入第三方 Bot 依赖），收到 document 且扩展名为 .cas 时
// getFile → 下载 → cas.AutoSaveCASFiles。
package casintake

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"litepan/internal/cas"
	"litepan/internal/domain"
	"litepan/pkg/safego"
)

const (
	// pollTimeoutSeconds getUpdates 长轮询超时（秒），与客户端超时保持一档差。
	pollTimeoutSeconds = 50
	// maxCasFileBytes 单个 .cas 下载上限，防止被超大文件撑爆内存。
	maxCasFileBytes = 10 << 20
	// channelRefreshInterval 重新读取启用渠道的间隔（配置变更后自动生效）。
	channelRefreshInterval = 60 * time.Second
	// errorRetryInterval 拉取失败后的重试间隔。
	errorRetryInterval = 5 * time.Second
	// conflictMaxRetryInterval 同一 bot 被其它实例占用时的最大退避间隔。
	conflictMaxRetryInterval = 60 * time.Second
	// telegramDefaultHost Telegram 官方 API 基址。
	telegramDefaultHost = "https://api.telegram.org"
)

// Start 启动 CAS 接收服务：周期性读取启用渠道，为每个 Telegram 渠道维护一个长轮询协程。
// 传 nil repo 或 ctx 取消时安全返回。
func Start(ctx context.Context, repo domain.NotifyChannelRepository, log *slog.Logger) {
	if repo == nil {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	go safego.Guard(log, "casintake.supervisor", func() {
		supervise(ctx, repo, log)
	})
}

// supervise 维护「渠道 ID → 轮询协程 cancel」映射，渠道启停后自动增减。
func supervise(ctx context.Context, repo domain.NotifyChannelRepository, log *slog.Logger) {
	running := map[int64]context.CancelFunc{}
	defer func() {
		for _, cancel := range running {
			cancel()
		}
	}()

	ticker := time.NewTicker(channelRefreshInterval)
	defer ticker.Stop()
	for {
		refresh(ctx, repo, log, running)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// refresh 按最新启用渠道列表增删轮询协程。
func refresh(ctx context.Context, repo domain.NotifyChannelRepository, log *slog.Logger, running map[int64]context.CancelFunc) {
	channels, err := repo.ListEnabled(ctx)
	if err != nil {
		log.Warn("CAS 接收：读取通知渠道失败", "error", err.Error())
		return
	}

	alive := map[int64]struct{}{}
	for _, ch := range channels {
		if ch == nil || !strings.EqualFold(strings.TrimSpace(ch.Type), "telegram") {
			continue
		}
		cfg := decodeConfig(ch.Config)
		token := strings.TrimSpace(cfg["bot_token"])
		if token == "" {
			continue
		}
		alive[ch.ID] = struct{}{}
		if _, ok := running[ch.ID]; ok {
			continue
		}
		pollCtx, cancel := context.WithCancel(ctx)
		running[ch.ID] = cancel
		channelID := ch.ID
		host := normalizeHost(cfg["api_host"])
		log.Info("CAS 接收：启动 Telegram 长轮询", "channel_id", channelID, "name", ch.Name)
		go safego.Guard(log, "casintake.telegram", func() {
			pollTelegram(pollCtx, channelID, token, host, log)
		})
	}

	for id, cancel := range running {
		if _, ok := alive[id]; ok {
			continue
		}
		cancel()
		delete(running, id)
		log.Info("CAS 接收：渠道已停用，停止长轮询", "channel_id", id)
	}
}

// normalizeHost 归一 Telegram API 基址（空则用官方地址）。
func normalizeHost(raw string) string {
	host := strings.TrimSpace(raw)
	if host == "" {
		return telegramDefaultHost
	}
	return strings.TrimSuffix(host, "/")
}

// ---------------------------------------------------------------------------
// Telegram 长轮询
// ---------------------------------------------------------------------------

type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

type tgMessage struct {
	Chat     *tgChat     `json:"chat"`
	Document *tgDocument `json:"document"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

type tgDocument struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
}

// pollTelegram 单个渠道的长轮询循环：跳过启动前积压的更新，只处理运行期收到的 .cas。
//
// 同一 bot_token 若有其它实例（例如旧版 diy-strm 容器）也在长轮询，Telegram 会返回
// "Conflict: terminated by other getUpdates request"。这属于部署侧冲突而非本服务故障，
// 因此按指数退避降低请求频率（避免与对方互抢），且只在状态切换时告警，防止刷屏。
func pollTelegram(ctx context.Context, channelID int64, token, host string, log *slog.Logger) {
	client := &http.Client{Timeout: (pollTimeoutSeconds + 15) * time.Second}

	// 有 webhook 时 getUpdates 会 409，先删除（不影响已投递的更新）。
	deleteWebhook(ctx, client, host, token, log)

	offset := latestUpdateID(ctx, client, host, token)

	// 冲突状态下逐步拉长重试间隔；恢复后立即归零。
	conflicting := false
	backoff := errorRetryInterval

	for {
		if ctx.Err() != nil {
			return
		}
		updates, err := getUpdates(ctx, client, host, token, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if isPollingConflict(err) {
				if !conflicting {
					conflicting = true
					log.Warn("CAS 接收：检测到其它实例正在使用同一 bot，已进入退避轮询（请停止重复的 bot 实例）",
						"channel_id", channelID, "backoff", backoff.String(), "error", err.Error())
				}
				if !sleepCtx(ctx, backoff) {
					return
				}
				if backoff < conflictMaxRetryInterval {
					backoff *= 2
					if backoff > conflictMaxRetryInterval {
						backoff = conflictMaxRetryInterval
					}
				}
				continue
			}
			if conflicting {
				conflicting = false
				backoff = errorRetryInterval
			}
			log.Warn("CAS 接收：拉取更新失败", "channel_id", channelID, "error", err.Error())
			if !sleepCtx(ctx, errorRetryInterval) {
				return
			}
			continue
		}
		if conflicting {
			conflicting = false
			backoff = errorRetryInterval
			log.Info("CAS 接收：轮询冲突已解除，恢复正常长轮询", "channel_id", channelID)
		}
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			if u.Message == nil || u.Message.Document == nil {
				continue
			}
			doc := u.Message.Document
			name := strings.TrimSpace(doc.FileName)
			if !isCasFileName(name) {
				continue
			}
			handleDocument(ctx, client, host, token, channelID, doc, log)
		}
	}
}

// handleDocument 下载 .cas 内容并触发自动转存。
func handleDocument(ctx context.Context, client *http.Client, host, token string, channelID int64, doc *tgDocument, log *slog.Logger) {
	content, err := downloadFile(ctx, client, host, token, doc.FileID)
	if err != nil {
		log.Warn("CAS 接收：下载 .cas 失败", "channel_id", channelID, "file_name", doc.FileName, "error", err.Error())
		notifyResult(ctx, "error", doc.FileName, "下载失败："+err.Error())
		return
	}
	log.Info("CAS 接收：收到 .cas 文件", "channel_id", channelID, "file_name", doc.FileName, "size", len(content))

	results := cas.AutoSaveCASFiles(ctx, []cas.AutoSaveSourceFile{{FileName: doc.FileName, Content: content}})
	for _, r := range results {
		switch {
		case r.Saved:
			log.Info("CAS 接收：自动转存成功", "file_name", r.SavedFileName, "drive_type", r.DriveType, "account_id", r.AccountID)
			// 成功通知由 cas.AutoSaveCASFiles 内部经通知中心发出。
		case r.Skipped:
			log.Warn("CAS 接收：自动转存跳过", "file_name", doc.FileName, "reason", r.Reason)
			notifyResult(ctx, "warn", doc.FileName, "未转存："+r.Reason)
		}
	}
}

// notifyResult 在未成功转存时提示用户原因（下载/判定/落盘失败）。
func notifyResult(ctx context.Context, level, fileName, message string) {
	cas.NotifyAutoSaveFailure(ctx, level, fileName, message)
}

// isCasFileName 判断是否为 .cas 清单文件。
func isCasFileName(name string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(name)), ".cas")
}

// ---------------------------------------------------------------------------
// Telegram HTTP 调用
// ---------------------------------------------------------------------------

func deleteWebhook(ctx context.Context, client *http.Client, host, token string, log *slog.Logger) {
	endpoint := fmt.Sprintf("%s/bot%s/deleteWebhook", host, token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader("{}"))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = log
}

// latestUpdateID 取当前最新 update_id，使后续 offset 从该 ID 之后开始，避免重放历史积压。
func latestUpdateID(ctx context.Context, client *http.Client, host, token string) int64 {
	endpoint := fmt.Sprintf("%s/bot%s/getUpdates?offset=-1&limit=1&timeout=0", host, token)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	var payload struct {
		OK     bool       `json:"ok"`
		Result []tgUpdate `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil || !payload.OK {
		return 0
	}
	if len(payload.Result) == 0 {
		return 0
	}
	return payload.Result[len(payload.Result)-1].UpdateID + 1
}

// getUpdates 长轮询拉取更新。
func getUpdates(ctx context.Context, client *http.Client, host, token string, offset int64) ([]tgUpdate, error) {
	endpoint := fmt.Sprintf("%s/bot%s/getUpdates?offset=%d&timeout=%d&allowed_updates=%%5B%%22message%%22%%5D",
		host, token, offset, pollTimeoutSeconds)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var payload struct {
		OK          bool       `json:"ok"`
		Description string     `json:"description"`
		Result      []tgUpdate `json:"result"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("解析 getUpdates 响应失败: %w", err)
	}
	if !payload.OK {
		return nil, fmt.Errorf("Telegram 返回错误: %s", payload.Description)
	}
	return payload.Result, nil
}

// downloadFile 通过 getFile + 文件下载地址取回 .cas 内容（上限 maxCasFileBytes）。
func downloadFile(ctx context.Context, client *http.Client, host, token, fileID string) (string, error) {
	metaURL := fmt.Sprintf("%s/bot%s/getFile?file_id=%s", host, token, url.QueryEscape(fileID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metaURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	var meta struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	derr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&meta)
	_ = resp.Body.Close()
	if derr != nil {
		return "", derr
	}
	if !meta.OK {
		return "", fmt.Errorf("getFile 失败: %s", meta.Description)
	}
	if meta.Result.FilePath == "" {
		return "", fmt.Errorf("getFile 未返回文件路径")
	}

	dlURL := fmt.Sprintf("%s/file/bot%s/%s", host, token, meta.Result.FilePath)
	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return "", err
	}
	dlResp, err := client.Do(dlReq)
	if err != nil {
		return "", err
	}
	defer func() { _ = dlResp.Body.Close() }()
	if dlResp.StatusCode >= 400 {
		return "", fmt.Errorf("下载文件 HTTP %d", dlResp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(dlResp.Body, maxCasFileBytes))
	if err != nil {
		return "", err
	}
	if len(body) == 0 {
		return "", fmt.Errorf("文件内容为空")
	}
	return string(body), nil
}

// decodeConfig 解析渠道配置 JSON（notifychannel 的解析器未导出，此处独立实现）。
func decodeConfig(raw string) map[string]string {
	out := map[string]string{}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return out
	}
	_ = json.Unmarshal([]byte(trimmed), &out)
	return out
}

// isPollingConflict 判断是否为「同一 bot 有其它实例在 getUpdates」导致的冲突。
// Telegram 对此返回 409 与 "Conflict: terminated by other getUpdates request"。
func isPollingConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "conflict") || strings.Contains(msg, "terminated by other getupdates request")
}

// sleepCtx 可被取消中断的休眠；返回 false 表示 ctx 已结束。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
