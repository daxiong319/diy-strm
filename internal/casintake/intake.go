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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"litepan/internal/cas"
	"litepan/internal/domain"
	"litepan/pkg/safego"
)

const (
	// pollTimeoutSeconds getUpdates 长轮询超时（秒），与客户端超时保持一档差。
	pollTimeoutSeconds = 50
	// pollClientTimeoutSeconds HTTP 客户端超时，必须显著大于服务端长轮询
	// pollTimeoutSeconds，否则响应头还没回来客户端就先超时了（表现为
	// "Client.Timeout exceeded while awaiting headers"）。Telegram 在
	// timeout=50 时通常在 50s 左右返回，留 40s 余量吸收网络抖动与排队。
	pollClientTimeoutSeconds = pollTimeoutSeconds + 40
	// maxCasFileBytes 单个 .cas 下载上限，防止被超大文件撑爆内存。
	maxCasFileBytes = 10 << 20
	// channelRefreshInterval 重新读取启用渠道的间隔（配置变更后自动生效）。
	channelRefreshInterval = 60 * time.Second
	// errorRetryInterval 拉取失败后的重试间隔。
	errorRetryInterval = 5 * time.Second
	// conflictMaxRetryInterval 同一 bot 被其它实例占用时的最大退避间隔。
	conflictMaxRetryInterval = 60 * time.Second
	// transientMaxRetryInterval 瞬时网络故障（超时/连接重置）的最大退避间隔，
	// 避免弱网下每 5s 打一条 WARN 刷屏。
	transientMaxRetryInterval = 30 * time.Second
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
	client := &http.Client{Timeout: pollClientTimeoutSeconds * time.Second}

	// 有 webhook 时 getUpdates 会 409，先删除（不影响已投递的更新）。
	deleteWebhook(ctx, client, host, token, log)

	offset := latestUpdateID(ctx, client, host, token)

	// 失败分类计数：状态变化时打一条明细，其余只累计，避免同类告警刷屏。
	track := newFailureTracker()

	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		updates, err := getUpdates(ctx, client, host, token, offset)
		elapsed := time.Since(start)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			kind := classifyPollError(err)
			wait := track.observe(kind, err, elapsed, channelID, host, log)
			if !sleepCtx(ctx, wait) {
				return
			}
			continue
		}
		track.recovered(channelID, log)
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			if u.Message == nil || u.Message.Document == nil {
				continue
			}
			doc := u.Message.Document
			name := strings.TrimSpace(doc.FileName)
			// 既要处理 .cas，也要处理压缩包（发送方常把清单打包发出）。
			// 不匹配的文件仍然静默跳过：TG 群里日常还有别的文件，
			// 逐个提示会变成刷屏。
			if !isCasIntakeName(name) {
				continue
			}
			handleDocument(ctx, client, host, token, channelID, doc, log)
		}
	}
}

// pollFailureKind 长轮询失败分类：不同原因的处置与日志级别不同。
type pollFailureKind int

const (
	// pollFailureConflict 同一 bot 被其它实例占用（Telegram 409），属部署侧问题。
	pollFailureConflict pollFailureKind = iota
	// pollFailureTransient 瞬时网络故障（超时、连接被拒/重置、DNS 抖动），
	// 之前它被打成与真实上游失败相同的 WARN 并无限刷屏，正是用户看到的
	// "拉取更新失败" 大量重复。现在按类聚合、逐级退避。
	pollFailureTransient
	// pollFailureUpstream Telegram 明确返回的业务错误（ok=false），需要人工关注。
	pollFailureUpstream
)

// String 便于日志与测试断言。
func (k pollFailureKind) String() string {
	switch k {
	case pollFailureConflict:
		return "conflict"
	case pollFailureTransient:
		return "transient"
	case pollFailureUpstream:
		return "upstream"
	}
	return "unknown"
}

// classifyPollError 判定失败类别。
//
// 注意：context deadline exceeded / Client.Timeout exceeded 属于客户端超时，
// 不包含 "conflict" 字样，因此不会被误判为冲突。
func classifyPollError(err error) pollFailureKind {
	if err == nil {
		return pollFailureUpstream
	}
	if isPollingConflict(err) {
		return pollFailureConflict
	}
	if isTransientNetworkError(err) {
		return pollFailureTransient
	}
	return pollFailureUpstream
}

// isTransientNetworkError 判断是否为瞬时网络故障（可自愈，不需要人工介入）。
func isTransientNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ETIMEDOUT) ||
		errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"client.timeout exceeded",
		"context deadline exceeded",
		"i/o timeout",
		"connection refused",
		"connection reset by peer",
		"broken pipe",
		"no such host",
		"tls handshake timeout",
		"eof",
		"temporary failure in name resolution",
		"network is unreachable",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// failureTracker 抑制重复告警：同类别连续失败只在首次（以及每 50 次）打明细，
// 恢复时打一条 Info 收尾，从而把「显示了好多个」压成可读的少量日志。
type failureTracker struct {
	kind      pollFailureKind
	active    bool
	count     int
	backoff   time.Duration
	lastError string
}

// newFailureTracker 初始化为「未失败」状态。
func newFailureTracker() *failureTracker {
	return &failureTracker{}
}

// observe 记录一次失败，必要时打日志，并返回本次应休眠的时长。
func (t *failureTracker) observe(kind pollFailureKind, err error, elapsed time.Duration, channelID int64, host string, log *slog.Logger) time.Duration {
	t.count++
	// 失败原因变化时重置计数与退避，保证新问题第一时间可见。
	if !t.active || t.kind != kind {
		t.active = true
		t.kind = kind
		t.count = 1
		t.backoff = 0
	}
	t.lastError = err.Error()

	switch kind {
	case pollFailureConflict:
		if t.backoff == 0 {
			t.backoff = errorRetryInterval
		}
		if t.count == 1 {
			log.Warn("CAS 接收：检测到其它实例正在使用同一 bot，已进入退避轮询（请停止重复的 bot 实例）",
				"channel_id", channelID, "backoff", t.backoff.String(), "error", t.lastError)
		}
		t.grow(conflictMaxRetryInterval)
		return t.consumeBackoff()
	case pollFailureTransient:
		if t.backoff == 0 {
			t.backoff = errorRetryInterval
		}
		if t.count == 1 {
			log.Warn("CAS 接收：拉取更新超时/网络抖动，将自动重试",
				"channel_id", channelID, "host", host, "elapsed", elapsed.Round(time.Millisecond).String(),
				"error", t.lastError)
		} else if t.count%50 == 0 {
			log.Warn("CAS 接收：拉取更新仍不稳定，持续重试中",
				"channel_id", channelID, "host", host, "failures", t.count,
				"backoff", t.backoff.String(), "error", t.lastError)
		}
		t.grow(transientMaxRetryInterval)
		return t.consumeBackoff()
	default:
		if t.count == 1 {
			log.Warn("CAS 接收：拉取更新失败",
				"channel_id", channelID, "host", host, "elapsed", elapsed.Round(time.Millisecond).String(),
				"error", t.lastError)
		} else if t.count%50 == 0 {
			log.Warn("CAS 接收：拉取更新持续失败", "channel_id", channelID, "host", host,
				"failures", t.count, "error", t.lastError)
		}
		return errorRetryInterval
	}
}

// grow 指数增长退避，封顶 max。
func (t *failureTracker) grow(max time.Duration) {
	if t.backoff <= 0 {
		t.backoff = errorRetryInterval
		return
	}
	t.backoff *= 2
	if t.backoff > max {
		t.backoff = max
	}
}

// consumeBackoff 取本次休眠时长并把退避推进到下一档。
func (t *failureTracker) consumeBackoff() time.Duration {
	current := t.backoff
	t.grow(t.maxForKind())
	return current
}

// maxForKind 该类别允许的最大退避。
func (t *failureTracker) maxForKind() time.Duration {
	if t.kind == pollFailureConflict {
		return conflictMaxRetryInterval
	}
	return transientMaxRetryInterval
}

// recovered 成功后收尾：只有在之前确实处于失败态时才打一条恢复日志。
func (t *failureTracker) recovered(channelID int64, log *slog.Logger) {
	if !t.active {
		return
	}
	kind := t.kind
	failures := t.count
	t.active = false
	t.count = 0
	t.backoff = 0
	t.lastError = ""
	if kind == pollFailureConflict {
		log.Info("CAS 接收：轮询冲突已解除，恢复正常长轮询", "channel_id", channelID)
		return
	}
	log.Info("CAS 接收：长轮询已恢复正常", "channel_id", channelID,
		"failed_attempts", failures, "reason", kind.String())
}

// handleDocument 下载文件内容并触发自动转存。
//
// 支持两种投递形态：
//   - 单个 .cas 清单（原有行为）
//   - 压缩包（.zip/.tar/.tar.gz/.tgz/.gz），解压后取出里面的 .cas 逐个转存
//
// 压缩包形态在实践中很常见：发送方习惯把清单打包发出来。
// 修复前 isCasFileName 只认 .cas 后缀，压缩包被静默 continue 丢弃，
// 用户看到的现象就是「转发了但系统毫无反应，日志里也没有记录」。
func handleDocument(ctx context.Context, client *http.Client, host, token string, channelID int64, doc *tgDocument, log *slog.Logger) {
	name := strings.TrimSpace(doc.FileName)

	if isUnsupportedArchiveName(name) {
		// 明确告知而不是静默丢弃：用户以为自己发了，程序却当没看见。
		log.Warn("CAS 接收：压缩包格式不受支持", "channel_id", channelID, "file_name", name)
		notifyResult(ctx, "warn", name, "", "", "压缩包格式不受支持（当前仅支持 zip / tar / tar.gz / tgz / gz）")
		return
	}

	limit := int64(maxCasFileBytes)
	if isArchiveFileName(name) {
		limit = maxArchiveFileBytes
	}
	content, err := downloadFile(ctx, client, host, token, doc.FileID, limit)
	if err != nil {
		kind := "下载 .cas 失败"
		if isArchiveFileName(name) {
			kind = "下载压缩包失败"
		}
		log.Warn("CAS 接收："+kind, "channel_id", channelID, "file_name", name, "error", err.Error())
		notifyResult(ctx, "error", name, "", "", "下载失败："+err.Error())
		return
	}

	if !isArchiveFileName(name) {
		log.Info("CAS 接收：收到 .cas 文件", "channel_id", channelID, "file_name", name, "size", len(content))
		autoSaveItems(ctx, name, []casItem{{Name: name, Content: string(content)}}, log)
		return
	}

	log.Info("CAS 接收：收到压缩包", "channel_id", channelID, "file_name", name, "size", len(content))
	items, err := extractCasFromArchive(name, content)
	if err != nil {
		log.Warn("CAS 接收：压缩包解压失败", "channel_id", channelID, "file_name", name, "error", err.Error())
		notifyResult(ctx, "warn", name, "", "", "解压失败："+err.Error())
		return
	}
	log.Info("CAS 接收：压缩包解压完成", "channel_id", channelID, "file_name", name, "cas_count", len(items))
	autoSaveItems(ctx, name, items, log)
}

// autoSaveItems 把取出的一批 .cas 交给自动转存，并记录每个结果。
// sourceName 是用户实际发来的文件名（可能是压缩包名），用于日志溯源。
func autoSaveItems(ctx context.Context, sourceName string, items []casItem, log *slog.Logger) {
	files := make([]cas.AutoSaveSourceFile, 0, len(items))
	for _, item := range items {
		files = append(files, cas.AutoSaveSourceFile{FileName: item.Name, Content: item.Content, Source: sourceName})
	}
	results := cas.AutoSaveCASFiles(ctx, files)
	for _, r := range results {
		// 日志要能独立回答「转存了什么文件 → 到哪个网盘 → 哪个目录 → 成功还是失败」，
		// 只打 file_name + drive_type 时用户看到「自动转存成功 / 账号 2」完全不知道去向。
		name := firstNonEmpty(r.SavedFileName, sourceName)
		drive := cas.DriveDisplayName(r.DriveType)
		dir := cas.DisplaySaveDir(r.SaveDir)
		switch {
		case r.Saved:
			log.Info("CAS 接收：自动转存成功", "file_name", r.SavedFileName, "drive_type", r.DriveType,
				"drive", drive, "save_dir", dir, "account_id", r.AccountID, "source", sourceName)
			// 成功通知由 cas.AutoSaveCASFiles 内部经通知中心发出。
		case r.Skipped:
			log.Warn("CAS 接收：自动转存失败", "file_name", r.SavedFileName, "drive_type", r.DriveType,
				"drive", drive, "save_dir", dir, "source", sourceName, "reason", r.Reason)
			// 失败原因用清单名而不是压缩包名，用户才能对上是哪个文件出问题。
			notifyResult(ctx, "warn", name, r.DriveType, r.SaveDir, "未转存："+r.Reason)
		}
	}
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// notifyResult 在未成功转存时提示用户原因（下载/判定/落盘失败）。
// driveType/saveDir 可能为空（尚未判定出网盘，或下载阶段就失败），此时文案自动略去。
func notifyResult(ctx context.Context, level, fileName, driveType, saveDir, message string) {
	cas.NotifyAutoSaveFailure(ctx, level, fileName, driveType, saveDir, message)
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
//
// 必须显式检查 HTTP 状态码：Telegram 在 409（同一 bot 有其它实例占用）时
// 响应体仍是 JSON 且 ok=false，但更糟的情况是网关返回非 JSON 的错误页。
// 只依赖 JSON 解析会把「连接问题」和「上游业务错误」混为一谈。
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
		ErrorCode   int        `json:"error_code"`
		Description string     `json:"description"`
		Result      []tgUpdate `json:"result"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("Telegram 返回 HTTP %d（响应非 JSON）", resp.StatusCode)
		}
		return nil, fmt.Errorf("解析 getUpdates 响应失败: %w", err)
	}
	if !payload.OK {
		if payload.Description != "" {
			// 带上 error_code：409 冲突文案即使措辞变化也能被 isPollingConflict 兜住。
			return nil, fmt.Errorf("Telegram 返回错误（HTTP %d，error_code %d）: %s",
				resp.StatusCode, payload.ErrorCode, payload.Description)
		}
		return nil, fmt.Errorf("Telegram 返回错误（HTTP %d）", resp.StatusCode)
	}
	return payload.Result, nil
}

// downloadFile 通过 getFile + 文件下载地址取回文件内容。
//
// 返回 []byte 而非 string：压缩包是二进制，用 string 承载虽然当前实现能
// 保全字节，但语义上会诱导调用方做文本处理，这里直接按二进制返回。
// limit 由调用方按对象类型给出（.cas 与压缩包的上限不同）。
func downloadFile(ctx context.Context, client *http.Client, host, token, fileID string, limit int64) ([]byte, error) {
	metaURL := fmt.Sprintf("%s/bot%s/getFile?file_id=%s", host, token, url.QueryEscape(fileID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metaURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
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
		return nil, derr
	}
	if !meta.OK {
		return nil, fmt.Errorf("getFile 失败: %s", meta.Description)
	}
	if meta.Result.FilePath == "" {
		return nil, fmt.Errorf("getFile 未返回文件路径")
	}

	dlURL := fmt.Sprintf("%s/file/bot%s/%s", host, token, meta.Result.FilePath)
	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return nil, err
	}
	dlResp, err := client.Do(dlReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dlResp.Body.Close() }()
	if dlResp.StatusCode >= 400 {
		return nil, fmt.Errorf("下载文件 HTTP %d", dlResp.StatusCode)
	}
	// 多读 1 字节用于判断是否超限：只读到 limit 无法区分「刚好等于上限」
	// 和「被截断」，会把超大文件当成完整内容传给解析器。
	body, err := io.ReadAll(io.LimitReader(dlResp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("文件超过上限（%d 字节）", limit)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("文件内容为空")
	}
	return body, nil
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
