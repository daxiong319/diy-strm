package playmonitor

import (
	"context"
	"net/http"
	"sort"
	"time"

	"litepan/internal/domain"
)

// ─────────────────────────────────────────────────────────────────────────────
// 实时会话：起播 / 心跳 / 停播
// ─────────────────────────────────────────────────────────────────────────────

// Wrap 一次取流请求被判为「开始播放」时由 internal/playback 调用，
// 返回包了字节计数器的 http.ResponseWriter。
//
// 这是整个播放监控唯一给「写字节」的地方。判定所需的三样东西：
//
//	origin = internal/playback.PickAction 是否选了流代理分支
//	         （true → 字节流过自己服务器；false → 302 到网盘直链）
//	r      = 原始请求（判客户端 IP 是否内网）
//	ev     = 条目/设备/来源等事件字段
//
// 三态判定见 classifyState —— **顺序是硬要求，先判局域网再判 origin**：
// 写成 `if origin {} else if lan {}` 会把内网的流代理误判成计费中。
//
// 返回 nil 表示监控未启用或这次请求不该建会话，
// 调用方必须**原样使用传入的 w**（拿 nil 去写会 panic，
// 而"监控关闭"恰好是最常见的那条路径）。
func (s *Service) Wrap(r *http.Request, ev StreamEvent, origin bool) http.ResponseWriter {
	if s.opts.Settings == nil || !s.opts.Settings.Bool(KeyMonitorEnabled) {
		return nil
	}
	if ev.ItemName == "" && ev.StrmPath == "" {
		return nil
	}
	lan := requestIsLAN(r)
	state := classifyState(lan, origin)
	// request_type 记录的是「取流形态」，与三态是**两个维度**：
	// 内网流代理也是 stream，内网 302 也是 redirect。
	// 是否计费只由 State 决定，绝不由 request_type 决定。
	if origin {
		ev.RequestType = RequestTypeStream
	} else {
		ev.RequestType = RequestTypeRedirect
	}
	ev.State = state
	if ev.ClientIP == "" {
		ev.ClientIP = clientIP(r)
	}
	if r != nil {
		if ev.UserAgent == "" {
			ev.UserAgent = r.UserAgent()
		}
		if ev.RequestURL == "" {
			ev.RequestURL = r.URL.Path
		}
	}
	if ev.AppSource == "" {
		ev.AppSource = PlaySourceAuto
	}
	if ev.StorageSlug == "" {
		ev.StorageSlug = DefaultStorageSlug
	}
	if ev.StorageType == "" {
		ev.StorageType = DefaultStorageType
	}
	if ev.DeviceID == "" {
		ev.DeviceID = dedupKey(ev.Client, ev.UserAgent)
	}

	key := dedupKey(ev.Client, ev.UserAgent) + "|" + ev.ItemName
	now := time.Now()

	s.mu.Lock()
	if existing, found := s.sessions[key]; found {
		// 同一 (设备, 片名) 的后续请求 = 同一次播放的后续分段（续传/重试）。
		existing.LastSeenAt = now
		if ev.Bitrate > 0 {
			existing.Bitrate = ev.Bitrate
		}
		if existing.State != state {
			// 内网↔外网切换（同一设备从内网切到外网续看）：以最新判定为准。
			existing.State = state
		}
		if existing.AppUserID == 0 && ev.AppUserID != 0 {
			existing.AppUserID = ev.AppUserID
		}
		if existing.UserName == "" {
			existing.UserName = s.userNameLocked(ev.AppUserID)
		}
		if existing.EmbyUserID == "" {
			existing.EmbyUserID = ev.EmbyUserID
		}
		id := existing.ID
		resumed := s.resumeLocked(key, now)
		s.mu.Unlock()
		// 暂停消噪窗口内续播：撤销停止事件，不重新上报停止。
		if resumed {
			s.logf("播放监控：同设备同片在消噪窗口内续播，撤销停止事件 %s", id)
		}
		// 分段续传同样要计字节，所以照样包一层（但不开新会话）。
		return s.wrapExisting(id)
	}
	id := hashKey(key, now.Format(time.RFC3339Nano))
	sess := &sessionEntry{
		PlaySession: domain.PlaySession{
			ID:          id,
			State:       state,
			AppUserID:   ev.AppUserID,
			EmbyUserID:  ev.EmbyUserID,
			ItemName:    ev.ItemName,
			StrmPath:    ev.StrmPath,
			ItemScope:   ev.ItemScope,
			ClientIP:    ev.ClientIP,
			UserAgent:   ev.UserAgent,
			Client:      ev.Client,
			DeviceID:    ev.DeviceID,
			StorageSlug: ev.StorageSlug,
			StorageType: ev.StorageType,
			Bitrate:     ev.Bitrate,
			StartedAt:   now,
			LastSeenAt:  now,
			AccountID:   ev.AccountID,
		},
		startMono: now,
	}
	sess.UserName = s.userNameLocked(ev.AppUserID)
	s.sessions[key] = sess
	// 新的一次播放：清掉这条 (设备,片名) 的停止标记。
	delete(s.stopped, key)
	s.mu.Unlock()
	s.logf("播放监控：起播 %s %s（%s）", id, ev.ItemName, state.DisplayText())
	return s.wrapExisting(id)
}

// wrapExisting 给已存在的会话包一层字节计数器。
//
// 返回值就是包好的 writer 本身，不额外返回收尾回调：
// countingResponseWriter 是按次请求创建的，写字节前就交给 playback 用着，
// 请求结束时自然会失效，没有"忘记调用收尾函数"这种可能 ——
// 收尾回调一旦依赖调用方记得 defer，漏掉就会静默丢失一段实测字节。
func (s *Service) wrapExisting(id string) http.ResponseWriter {
	cw := &countingResponseWriter{}
	cw.onDone = func(bytes int64, _ int) { s.OnBytes(id, bytes) }
	return cw
}

// OnStreamOpen 实现 internal/playback.StreamMonitor：流代理分支。
//
// origin 恒为 true —— 调用方已经确认这条请求走的是流代理分支
// （PickAction 选中 ActionStream），字节流会经过自己的服务器。
func (s *Service) OnStreamOpen(r *http.Request, ev StreamEvent) http.ResponseWriter {
	return s.Wrap(r, ev, true)
}

// OnRedirectOpen 实现 internal/playback.StreamMonitor：302 直连分支。
//
// ⚠️ **只建会话、绝不上行**。302 的字节流由网盘直发给播放器，
// 自己服务器一个字节都没吐，所以这一支的 UploadedBytes 恒为 0，
// 播放记录也由 0028 那条 Emby 302 反代链负责落（见 writeStopRecord）。
//
// 这不是「顺手简化」，而是这一支全部存在的理由：这里若多记一个字节，
// 就是凭空把用户的上行账单算大了。把 CDN 直连按外网计费，
// 是这类功能最容易犯、也最伤人的错。
func (s *Service) OnRedirectOpen(r *http.Request, ev StreamEvent) {
	if s.opts.Settings == nil || !s.opts.Settings.Bool(KeyMonitorEnabled) {
		return
	}
	if ev.ItemName == "" && ev.StrmPath == "" {
		return
	}
	lan := requestIsLAN(r)
	// origin=false：302 直连。外网时是 CDN 直连（计 0），内网时是局域网。
	ev.State = classifyState(lan, false)
	ev.RequestType = RequestTypeRedirect
	s.Wrap(r, ev, false)
}

// OnStreamStop 实现 internal/playback.StreamMonitor：转交一次停止事件。
func (s *Service) OnStreamStop(id string) { s.OnStop(id) }

// OnBytes 会话结束时回报这次实际写出的字节数。
//
// ⚠️ 这里**不改计费数字**：计费口径是「码率 × 时长，每 5 秒累计」
// （照搬 参考实现 的 uploaded_bytes += bitrate/8*Δt），由 sample() 负责。
// 实际字节数只做两件事：
//
//  1. 记到会话的 measured 上，供前端展示「估算 / 实测」对照；
//  2. 与估算偏离过大时打一条告警 —— 这是码率估算失准的诚实信号，
//     静默忽略会让用户对账时无从下手。
func (s *Service) OnBytes(id string, bytes int64) {
	if bytes <= 0 {
		return
	}
	s.mu.Lock()
	var (
		found     bool
		measured  int64
		estimated int64
		started   time.Time
		item      string
	)
	for _, e := range s.sessions {
		if e.ID != id {
			continue
		}
		found = true
		e.MeasuredBytes += bytes
		measured = e.MeasuredBytes
		estimated = e.UploadedBytes
		started = e.StartedAt
		item = e.ItemName
		break
	}
	s.mu.Unlock()
	if !found {
		return
	}
	// 位数对齐后再比：估算为 0（刚起播还没采样）时不告警。
	if estimated <= 0 || measured <= 0 {
		return
	}
	drift := estimated - measured
	if drift < 0 {
		drift = -drift
	}
	if drift*100/estimated < 30 {
		return
	}
	elapsed := time.Since(started).Seconds()
	realBitrate := int64(0)
	if elapsed > 0 {
		realBitrate = measured * 8 / int64(elapsed)
	}
	s.logf("播放监控：%s 估算与实测偏离 %d%%（估算 %d 字节 / 实测 %d 字节，实测码率约 %d bps）",
		item, drift*100/estimated, estimated, measured, realBitrate)
}

// resumeLocked 判断是否属于「暂停后的消噪窗口内续播」，
// 并把停止标记清掉。**调用方必须已持锁**。
//
// 口径原文：「真正继续播放、连续播了约一分钟的量后，才重新显示『正在播放』」。
func (s *Service) resumeLocked(key string, now time.Time) bool {
	stopAt, wasStopped := s.stopped[key]
	if !wasStopped {
		return false
	}
	delete(s.stopped, key)
	return now.Sub(stopAt) >= resumeDebounce
}

// ResumeDebounceSeconds 暂停去噪窗口（秒）。
//
// 口径原文：「同一台设备在同一部片的同一位置停下，只会转交给媒体服务器一次」
// 「真正继续播放、连续播了约一分钟的量后，才重新显示『正在播放』」。
const ResumeDebounceSeconds = 60

var resumeDebounce = ResumeDebounceSeconds * time.Second

// OnStop 会话关闭（心跳丢失或显式停播）。
//
// 暂停消噪：同一个 (设备, 片名) 在消噪窗口内被多次判定为停播时，
// **只转交一次停止事件**。
func (s *Service) OnStop(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found *sessionEntry
	key := ""
	for k, e := range s.sessions {
		if e.ID == id {
			found = e
			key = k
			break
		}
	}
	if found == nil {
		return
	}
	stopKey := dedupKey(found.Client, found.UserAgent) + "|" + found.ItemName
	now := time.Now()
	if last, ok := s.stopped[stopKey]; ok && now.Sub(last) < resumeDebounce {
		// 消噪窗口内的重复停播 → 丢弃停止事件。
		s.logf("播放监控：同设备同片消噪窗口内重复停播，丢弃停止事件 %s", id)
		return
	}
	s.stopped[stopKey] = now
	delete(s.sessions, key)
}

// ─────────────────────────────────────────────────────────────────────────────
// 采样：码率 × 时长，每 sampleSeconds 累进当日桶
// ─────────────────────────────────────────────────────────────────────────────

// sample 每次采样：把所有会话的「码率 × 采样间隔」累进当日桶，
// 并关掉心跳丢失 idleSeconds 的会话（从实时列表消失）。
func (s *Service) sample(ctx context.Context) {
	now := time.Now()
	s.mu.Lock()
	var expired []domain.PlaySession
	incs := make([]trafficIncrement, 0, len(s.sessions))
	for key, e := range s.sessions {
		idle := s.idleSeconds()
		if now.Sub(e.LastSeenAt) > time.Duration(idle)*time.Second {
			// 心跳丢失：关会话，从实时列表消失 + 上报一次停止事件。
			expired = append(expired, e.PlaySession)
			delete(s.sessions, key)
			stopKey := dedupKey(e.Client, e.UserAgent) + "|" + e.ItemName
			s.stopped[stopKey] = now
			s.logf("播放监控：会话 %s 心跳丢失（超过 %d 秒），从列表移除", e.ID, idle)
			continue
		}
		// 局域网会话一律不计费（字节流走内网，不消耗上行）。
		if !e.State.Metered() {
			continue
		}
		// 上行字节 = 码率(bps)/8 × 采样间隔(秒)。
		bytes := e.Bitrate / 8 * int64(s.sampleSeconds())
		if bytes <= 0 {
			continue
		}
		e.UploadedBytes += bytes

		incs = append(incs, trafficIncrement{
			Day:      now.Format("2006-01-02"),
			UserID:   e.AppUserID,
			UserName: e.UserName,
			Bytes:    bytes,
		})
	}
	s.mu.Unlock()

	// 桶写库在锁外，避免把 SQLite 写延迟算进持锁时间。
	if s.opts.Traffic != nil {
		for _, inc := range incs {
			if inc.Bytes <= 0 {
				continue
			}
			if err := s.opts.Traffic.Accumulate(ctx, inc.Day, inc.UserID, inc.UserName, inc.Bytes); err != nil {
				s.logf("播放监控：当日流量桶写入失败 %v", err)
			}
		}
	}
	for _, sess := range expired {
		s.writeStopRecord(ctx, sess)
	}
}

// trafficIncrement 一次流量累加。
type trafficIncrement struct {
	Day      string
	UserID   int64
	UserName string
	Bytes    int64
}

// writeStopRecord 把一条已结束的播放写成 playback_records（历史记录查询用）。
//
// ⚠️ **CDN 直连（302）会话不在这里写**：0028 那条 Emby 302 反代链路
// （internal/playbackrecord，通过 SetRedirectObserver 挂的）已经在写同一张表，
// 两边都写会让同一次 302 播放在报告里算成两次（时长也翻倍）。
// 所以分工是：CDN 直连交给 0028 遗留链，流代理由本监控器写，
// 两边覆盖的分支互补、不重叠。
func (s *Service) writeStopRecord(ctx context.Context, sess domain.PlaySession) {
	if s.opts.Records == nil {
		return
	}
	if sess.State == domain.PlayStateCDN {
		// 302 直连由 0028 遗留链负责落记录，这里跳过以免重复。
		return
	}
	watched := int(time.Since(sess.StartedAt).Seconds())
	if watched < 0 {
		watched = 0
	}
	rec := &domain.PlaybackRecord{
		RuleID:         "1",
		EmbyUserID:     sess.EmbyUserID,
		AppUserID:      sess.AppUserID,
		Client:         sess.Client,
		DeviceID:       sess.DeviceID,
		ItemName:       sess.ItemName,
		StrmPath:       sess.StrmPath,
		PlaybackAt:     sess.StartedAt.UTC().Format(time.RFC3339),
		RequestType:    requestTypeForState(sess.State),
		ClientIP:       sess.ClientIP,
		UserAgent:      sess.UserAgent,
		ResponseStatus: responseStatusForState(sess.State),
		StorageSlug:    sess.StorageSlug,
		StorageType:    sess.StorageType,
		Timestamp:      sess.StartedAt.UTC().Format(time.RFC3339),
		ItemScope:      sess.ItemScope,
		AppSource:      PlaySourceAuto,
		AppClientIP:    sess.ClientIP,
		AppItemName:    sess.ItemName,
		AppStrmPath:    sess.StrmPath,
		WatchedSeconds: watched,
		// 局域网会话 UploadedBytes 恒为 0 —— 局域网字节流走内网，不消耗上行。
		UploadedBytes: sess.UploadedBytes,
	}
	// State/Metered 由存储层按 request_type 推导，这里预填便于排查。
	rec.State = domain.PlayStateFromRequestType(rec.RequestType)
	rec.Metered = rec.State.Metered()
	if _, err := s.opts.Records.Insert(ctx, rec); err != nil {
		s.logf("播放监控：结束记录落库失败 %v", err)
	}
}

// requestTypeForState 由三态反推 request_type（写入 playback_records）。
// 局域网在历史记录里无法与流代理区分 —— 它的 request_type 也是 stream，
// 但计费为 0（Metered 走 request_type 判断，局域网已在实时阶段排除）。
func requestTypeForState(s domain.PlayState) string {
	if s == domain.PlayStateCDN {
		return RequestTypeRedirect
	}
	return RequestTypeStream
}

// responseStatusForState 由三态反推 HTTP 响应码（写进历史记录便于排查）。
func responseStatusForState(s domain.PlayState) int {
	if s == domain.PlayStateCDN {
		return http.StatusFound // 302
	}
	return http.StatusOK
}

// ─────────────────────────────────────────────────────────────────────────────
// 对外查询
// ─────────────────────────────────────────────────────────────────────────────

// List 实时会话列表 + 顶部汇总。
func (s *Service) List(ctx context.Context, q domain.PlaySessionQuery) (domain.PlaySessionListResult, error) {
	var out domain.PlaySessionListResult
	out.Sessions = []domain.PlaySession{}
	now := time.Now()
	s.mu.Lock()
	items := make([]domain.PlaySession, 0, len(s.sessions))
	for _, e := range s.sessions {
		items = append(items, e.PlaySession)
	}
	s.mu.Unlock()

	// 会话列表按上次心跳时间倒序。
	sort.Slice(items, func(i, j int) bool { return items[i].LastSeenAt.After(items[j].LastSeenAt) })

	wanCount, lanCount, cdnCount := 0, 0, 0
	for _, it := range items {
		switch it.State {
		case domain.PlayStateLAN:
			lanCount++
		case domain.PlayStateCDN:
			cdnCount++
			wanCount++
		default:
			wanCount++
		}
		if it.State.Metered() {
			out.Summary.MeteredCount++
		}
		if q.State != "" && it.State != q.State {
			continue
		}
		if q.AppUserID > 0 && it.AppUserID != q.AppUserID {
			continue
		}
		out.Sessions = append(out.Sessions, it)
	}
	out.Summary.ActiveCount = len(items)
	// 外网会话数 = 总数 - 局域网会话数。顶部流量条口径：
	// 「当前在播会话数 + 其中计费中数」。
	out.Summary.WANCount = wanCount
	out.Summary.LANCount = lanCount
	out.Summary.CDNCount = cdnCount

	// 顶部流量条：今日/本月/累计来自日流量桶，不来自内存会话。
	if s.opts.Traffic != nil {
		today := now.Format("2006-01-02")
		month := now.Format("2006-01")
		t, m, total, err := s.opts.Traffic.TodayMonthTotal(ctx, today, month)
		if err == nil {
			out.Summary.TodayBytes = t
			out.Summary.MonthBytes = m
			out.Summary.TotalBytes = total
		}
	}
	return out, nil
}

// userNameLocked 解析用户名（**调用方必须已持锁**）。
func (s *Service) userNameLocked(userID int64) string {
	if userID <= 0 {
		return ""
	}
	users := s.opts.Users
	if users == nil {
		return ""
	}
	name, ok := users.DisplayName(userID)
	if !ok {
		return ""
	}
	return name
}

// DefaultStorageSlug storage_slug 的默认值。
//
// 参考实现 默认 '115-default'（115 盘语义），litepan 有 11 个存储驱动，
// 照抄会把「未指定存储」错标成 115。改用 litepan 的默认驱动 'local'。
const DefaultStorageSlug = "local"

// DefaultStorageType storage_type 的默认值。
const DefaultStorageType = "local"

// PlaySourceAuto app_source 默认值：litepan 自动链路。
const PlaySourceAuto = "auto"
