package moviepilot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"litepan/internal/domain"
)

// ---------------------------------------------------------------------------
// MoviePilot 降级兜底（次数触发自动补找）
//
// 语义严格对齐参考实现 ResourceSearchConfig 的 mp_fallback_* 配置文案：
//
//   - mp_fallback_enabled「次数触发自动补找」：总开关，只控制搜索次数与订阅轮次规则，
//     不影响独立的定时搜索下载；兜底调用失败时本地订阅继续运行（本实现不因兜底失败中断订阅）。
//   - mp_fallback_search_enabled「资源搜索无结果时启用」：只对已选定影片和季号生效，
//     「报错和取消不计数」——只有搜索正常完成、候选为 0 时才累加 SearchCount。
//   - mp_fallback_search_threshold「累计无结果次数」：同一影片或同一季累计达到该次数后触发，默认 3。
//   - mp_fallback_subscription_enabled「本地订阅无进展时启用」：仅统计正常完成的搜索轮次，
//     有进展重置计数，有待入库任务时不触发。
//   - mp_fallback_subscription_threshold「连续无进展轮数」：同一影片或同一季连续未补到资源达该轮数后触发，默认 3。
//   - 动作 subscribe / download（默认）/ download_then_subscribe；
//     确认 MP 已接管订阅后本地订阅自动暂停，仅提交下载不暂停。
//
// 阈值语义（本实现的精确定义）：
//
//	搜索侧 —— 以「影片+季」为粒度**累计**无结果次数（跨轮累加，不要求连续）；
//	          一次成功的搜索若候选为 0 则 +1，一旦候选 > 0 立即清零。
//	订阅侧 —— 以「影片+季」为粒度统计**连续**无进展轮数；
//	          一轮正常完成但未补到任何资源则 +1，有任何新收录立即清零。
//
// 配置落点说明：参考实现把 mp_fallback_* 放在 ResourceSearchConfig（资源搜索配置）里，
// 而非 MoviePilot 连接配置里。目标仓库的对应位置是 discovery 包（本任务禁止修改），
// 因此这里把兜底配置**复用 MoviePilotConfig 单行表**（movie_pilot_configs）：
// 开启兜底本身以 MoviePilot 已配置为前提，放在同一张单行表可避免跨包改动，
// 且语义上仍属「MoviePilot 相关配置」。
// ---------------------------------------------------------------------------

// FallbackTarget 兜底对象（对齐参考实现「已选定影片和季号」的粒度）。
//
// 计数与阈值判定都以「影片+季」为粒度：同一影片的不同季互不干扰，
// 不同影片的同季也互不干扰。
type FallbackTarget struct {
	MediaType     string // movie / tv
	TmdbId        int64
	Title         string
	Season        int
	TotalEpisodes int
	// SubscribeID 本地订阅 ID；为 0 表示没有可自动暂停的本地订阅。
	SubscribeID int64
	// TargetDir MP 侧保存路径（可选）。
	TargetDir string
}

// MediaKey 该对象的「影片+季」唯一键。
func (t FallbackTarget) MediaKey() string {
	return domain.MoviePilotFallbackMediaKey(t.MediaType, t.TmdbId, t.Season)
}

// FallbackConfig 兜底运行时配置（读自 MoviePilotConfig 单行表）。
type FallbackConfig struct {
	Enabled               bool
	SearchEnabled         bool
	SearchThreshold       int
	SearchAction          string
	SubscriptionEnabled   bool
	SubscriptionThreshold int
	SubscriptionAction    string
}

// loadFallbackConfig 读取兜底配置；未配置 MoviePilot 时返回默认值（开关关闭、阈值 3、动作 download）。
func (s *Service) loadFallbackConfig(ctx context.Context) FallbackConfig {
	defaults := FallbackConfig{
		SearchThreshold:       domain.DefaultMoviePilotFallbackThreshold,
		SearchAction:          domain.MoviePilotFallbackActionDownload,
		SubscriptionThreshold: domain.DefaultMoviePilotFallbackThreshold,
		SubscriptionAction:    domain.MoviePilotFallbackActionDownload,
	}
	if s == nil || s.repo == nil {
		return defaults
	}
	cfg, err := s.repo.LoadConfig(ctx)
	if err != nil || cfg == nil {
		return defaults
	}
	return FallbackConfig{
		Enabled:               cfg.MpFallbackEnabled,
		SearchEnabled:         cfg.MpFallbackSearchEnabled,
		SearchThreshold:       domain.NormalizeMoviePilotFallbackThreshold(cfg.MpFallbackSearchThreshold),
		SearchAction:          domain.NormalizeMoviePilotFallbackAction(cfg.MpFallbackSearchAction),
		SubscriptionEnabled:   cfg.MpFallbackSubscriptionEnabled,
		SubscriptionThreshold: domain.NormalizeMoviePilotFallbackThreshold(cfg.MpFallbackSubscriptionThreshold),
		SubscriptionAction:    domain.NormalizeMoviePilotFallbackAction(cfg.MpFallbackSubscriptionAction),
	}
}

// FallbackSummary 兜底概览（供观测端点展示与配置校验）。
type FallbackSummary struct {
	Configured            bool
	Enabled               bool
	SearchEnabled         bool
	SearchThreshold       int
	SearchAction          string
	SubscriptionEnabled   bool
	SubscriptionThreshold int
	SubscriptionAction    string
	ActiveCount           int64
}

// FallbackSummary 返回兜底概览。
func (s *Service) FallbackSummary(ctx context.Context) (FallbackSummary, error) {
	if s == nil || s.repo == nil {
		return FallbackSummary{}, domain.Errorf(domain.CodeNotImplement, "MoviePilot 服务未配置")
	}
	cfg := s.loadFallbackConfig(ctx)
	out := FallbackSummary{
		Configured:            s.hasMoviePilotConfig(ctx),
		Enabled:               cfg.Enabled,
		SearchEnabled:         cfg.SearchEnabled,
		SearchThreshold:       cfg.SearchThreshold,
		SearchAction:          cfg.SearchAction,
		SubscriptionEnabled:   cfg.SubscriptionEnabled,
		SubscriptionThreshold: cfg.SubscriptionThreshold,
		SubscriptionAction:    cfg.SubscriptionAction,
	}
	n, err := s.repo.CountActiveFallbacks(ctx)
	if err != nil {
		return FallbackSummary{}, err
	}
	out.ActiveCount = n
	return out, nil
}

// ListFallbacks 分页列出兜底记录。
func (s *Service) ListFallbacks(ctx context.Context, page, pageSize int, status string) ([]domain.MoviePilotFallback, int64, error) {
	if s == nil || s.repo == nil {
		return nil, 0, domain.Errorf(domain.CodeNotImplement, "MoviePilot 服务未配置")
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 20
	}
	return s.repo.ListFallbacks(ctx, page, pageSize, status)
}

// hasMoviePilotConfig 是否已配置 MoviePilot（地址与 Token 均非空）。
// 对齐参考实现 moviepilot_guard.has_moviepilot_config：没配置就明确拒绝，绝不静默降级。
func (s *Service) hasMoviePilotConfig(ctx context.Context) bool {
	if s == nil || s.repo == nil {
		return false
	}
	cfg, err := s.repo.LoadConfig(ctx)
	if err != nil || cfg == nil {
		return false
	}
	return strings.TrimSpace(cfg.BaseUrl) != "" && strings.TrimSpace(cfg.ApiToken) != ""
}

// requireMoviePilotConfig 读取 MoviePilot 客户端；未配置时返回 nil 与明确错误。
// 对齐参考实现 moviepilot_guard.require_moviepilot_config。
func (s *Service) requireMoviePilotConfig(ctx context.Context) (*Client, error) {
	if !s.hasMoviePilotConfig(ctx) {
		return nil, domain.Errorf(domain.CodeValidation,
			"未配置 MoviePilot 地址或 API Token，已拒绝降级兜底（不静默降级）")
	}
	return s.client(ctx)
}

// —— 计数与触发 ——

// RecordFallbackSearch 记录一次「搜索正常完成但无结果」，达到阈值则触发降级。
//
// 「报错和取消不计数」：调用方必须只在搜索正常完成时调用本方法；
// 搜索报错/被取消的路径不要调用。
//
// 返回触发后的兜底记录（未触发返回 nil）与是否触发。
func (s *Service) RecordFallbackSearch(ctx context.Context, target FallbackTarget) (*domain.MoviePilotFallback, bool, error) {
	cfg := s.loadFallbackConfig(ctx)
	if !cfg.Enabled || !cfg.SearchEnabled || target.TmdbId <= 0 {
		return nil, false, nil
	}
	key := target.MediaKey()
	rec, err := s.repo.GetFallback(ctx, key, domain.MoviePilotFallbackTriggerSearch)
	if err != nil && !isFallbackNotFound(err) {
		return nil, false, err
	}
	if rec == nil {
		rec = &domain.MoviePilotFallback{
			MediaKey:  key,
			Trigger:   domain.MoviePilotFallbackTriggerSearch,
			MediaType: target.MediaType,
			TmdbId:    target.TmdbId,
			Title:     target.Title,
			Season:    target.Season,
			Status:    domain.MoviePilotFallbackPending,
			Action:    cfg.SearchAction,
		}
	}
	rec.SearchCount++
	rec.Title = target.Title
	rec.Action = cfg.SearchAction
	if rec.SearchCount < cfg.SearchThreshold {
		if err := s.repo.SaveFallback(ctx, rec); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	// 达到阈值：触发一次降级（触发后清零，等待下一轮重新累计）。
	rec.SearchCount = 0
	rec.Status = domain.MoviePilotFallbackPending
	if err := s.repo.SaveFallback(ctx, rec); err != nil {
		return nil, false, err
	}
	return rec, true, nil
}

// ResetFallbackSearch 搜索有结果时清零搜索计数（有进展重置）。
func (s *Service) ResetFallbackSearch(ctx context.Context, target FallbackTarget) error {
	if target.TmdbId <= 0 {
		return nil
	}
	key := target.MediaKey()
	rec, err := s.repo.GetFallback(ctx, key, domain.MoviePilotFallbackTriggerSearch)
	if err != nil {
		if isFallbackNotFound(err) {
			return nil
		}
		return err
	}
	if rec == nil || rec.SearchCount == 0 {
		return nil
	}
	rec.SearchCount = 0
	return s.repo.SaveFallback(ctx, rec)
}

// RecordFallbackSubscription 记录一轮「订阅正常完成但无进展」，达到阈值则触发降级。
//
// 「仅统计正常完成的搜索轮次；有进展重置；有待入库任务时不触发」：
//   - 调用方只在订阅轮次正常完成时调用（失败轮次不调用）；
//   - 有进展的路径请调用 ResetFallbackSubscription；
//   - 存在待入库任务时不触发（由本方法内部判定）。
func (s *Service) RecordFallbackSubscription(ctx context.Context, target FallbackTarget) (*domain.MoviePilotFallback, bool, error) {
	cfg := s.loadFallbackConfig(ctx)
	if !cfg.Enabled || !cfg.SubscriptionEnabled || target.TmdbId <= 0 {
		return nil, false, nil
	}
	// 有待入库任务时不触发：避免本地正好有文件在入库、以及 MP 正准备接手时重复触发。
	pending, err := s.repo.HasPendingTransferTasks(ctx)
	if err != nil {
		return nil, false, err
	}
	if pending {
		return nil, false, nil
	}
	key := target.MediaKey()
	rec, err := s.repo.GetFallback(ctx, key, domain.MoviePilotFallbackTriggerSubscription)
	if err != nil && !isFallbackNotFound(err) {
		return nil, false, err
	}
	if rec == nil {
		rec = &domain.MoviePilotFallback{
			MediaKey:  key,
			Trigger:   domain.MoviePilotFallbackTriggerSubscription,
			MediaType: target.MediaType,
			TmdbId:    target.TmdbId,
			Title:     target.Title,
			Season:    target.Season,
			Status:    domain.MoviePilotFallbackPending,
			Action:    cfg.SubscriptionAction,
		}
	}
	rec.SubscriptionCount++
	rec.Title = target.Title
	rec.Action = cfg.SubscriptionAction
	if rec.SubscriptionCount < cfg.SubscriptionThreshold {
		if err := s.repo.SaveFallback(ctx, rec); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	rec.SubscriptionCount = 0
	rec.Status = domain.MoviePilotFallbackPending
	if err := s.repo.SaveFallback(ctx, rec); err != nil {
		return nil, false, err
	}
	return rec, true, nil
}

// ResetFallbackSubscription 订阅有进展时清零轮次计数（有进展重置）。
func (s *Service) ResetFallbackSubscription(ctx context.Context, target FallbackTarget) error {
	if target.TmdbId <= 0 {
		return nil
	}
	key := target.MediaKey()
	rec, err := s.repo.GetFallback(ctx, key, domain.MoviePilotFallbackTriggerSubscription)
	if err != nil {
		if isFallbackNotFound(err) {
			return nil
		}
		return err
	}
	if rec == nil || rec.SubscriptionCount == 0 {
		return nil
	}
	rec.SubscriptionCount = 0
	return s.repo.SaveFallback(ctx, rec)
}

// TriggerFallback 在本地订阅一轮正常完成但无收录时推进兜底计数。
//
// search=true 走「搜索次数」规则（mp_fallback_search_*），
// search=false 走「订阅轮次」规则（mp_fallback_subscription_*）。
// 达到阈值时执行降级并按需暂停本地订阅，返回给调用方拼进摘要的消息。
//
// 设计说明：同一轮同时推进两条规则的计数是刻意的——参考实现的两组配置分别描述
// 「资源搜索无结果」和「本地订阅无进展」两种触发场景；两个开关各自独立生效，
// 谁先达到自己的阈值谁先触发；只想启用其一时只开对应开关即可
// （另一个开关关闭时完全不计数、不触发）。
func (s *Service) TriggerFallback(ctx context.Context, target FallbackTarget, search bool) (string, bool) {
	if s == nil || s.repo == nil || target.TmdbId <= 0 {
		return "", false
	}
	cfg := s.loadFallbackConfig(ctx)
	if !cfg.Enabled {
		return "", false
	}
	var (
		rec *domain.MoviePilotFallback
		hit bool
		err error
	)
	if search {
		if !cfg.SearchEnabled {
			return "", false
		}
		rec, hit, err = s.RecordFallbackSearch(ctx, target)
	} else {
		if !cfg.SubscriptionEnabled {
			return "", false
		}
		rec, hit, err = s.RecordFallbackSubscription(ctx, target)
	}
	if err != nil || !hit || rec == nil {
		return "", false
	}
	// 显式 guard：未配置 MoviePilot 明确拒绝，绝不静默降级。
	// 这里提前判断是为了在未配置时给出清晰摘要，executeFallback 内部同样有 guard。
	if !s.hasMoviePilotConfig(ctx) {
		rec.Status = domain.MoviePilotFallbackFailed
		rec.Message = "未配置 MoviePilot 地址或 API Token，已拒绝降级兜底（不静默降级）"
		if err := s.repo.SaveFallback(ctx, rec); err != nil {
			s.logWarn("MoviePilot 兜底：保存拒绝状态出错", err)
		}
		return "降级兜底被拒绝：" + rec.Message, true
	}
	s.executeFallback(ctx, rec, target)
	source := "搜索次数"
	if !search {
		source = "订阅轮次"
	}
	return fmt.Sprintf("已达%s阈值，转交 MoviePilot 兜底（%s）：%s", source, rec.Action, rec.Message), true
}

// —— 执行降级 ——

// executeFallback 执行一次降级：按动作把影片/季转交 MoviePilot 处理。
//
// 动作语义：
//   - subscribe：仅添加 MoviePilot 订阅；
//   - download（默认）：仅让 MP 搜索下载；
//   - download_then_subscribe：先让 MP 搜索下载，无可用资源再添加订阅。
//
// 「确认 MP 已接管订阅后本地订阅自动暂停，仅提交下载不暂停」：
// 只有当动作确实建立了 MP 订阅（subscribe / download_then_subscribe 中 MP 无下载资源而转为订阅）
// 时才暂停本地订阅。
//
// 兜底失败**不影响本地订阅继续运行**：错误只记录到状态记录与日志，不向调用方中断。
func (s *Service) executeFallback(ctx context.Context, rec *domain.MoviePilotFallback, target FallbackTarget) {
	if rec == nil {
		return
	}
	// 显式 guard：未配置 MoviePilot 明确拒绝，绝不静默降级。
	client, err := s.requireMoviePilotConfig(ctx)
	if err != nil {
		s.failFallback(ctx, rec, err)
		return
	}

	rec.Status = domain.MoviePilotFallbackRunning
	rec.Progress = 10
	if err := s.repo.SaveFallback(ctx, rec); err != nil {
		s.logWarn("MoviePilot 兜底：保存运行状态出错", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	action := domain.NormalizeMoviePilotFallbackAction(rec.Action)
	subscribed := false
	var msgParts []string

	switch action {
	case domain.MoviePilotFallbackActionSubscribe:
		if id, err := s.addSubscribeForFallback(runCtx, client, target, rec); err != nil {
			msgParts = append(msgParts, "添加 MoviePilot 订阅失败："+err.Error())
		} else {
			subscribed = true
			rec.ExternalID = fmt.Sprintf("%d", id)
			msgParts = append(msgParts, fmt.Sprintf("已添加 MoviePilot 订阅（ID %d）", id))
		}
	case domain.MoviePilotFallbackActionDownloadThenSubscribe:
		downloaded, derr := s.downloadForFallback(runCtx, client, target, rec)
		if derr != nil {
			msgParts = append(msgParts, "MoviePilot 搜索下载失败："+derr.Error())
		}
		if downloaded {
			msgParts = append(msgParts, "已让 MoviePilot 搜索下载")
		} else {
			// 无可用资源 → 转订阅
			if id, err := s.addSubscribeForFallback(runCtx, client, target, rec); err != nil {
				msgParts = append(msgParts, "无可用下载资源，添加订阅亦失败："+err.Error())
			} else {
				subscribed = true
				rec.ExternalID = fmt.Sprintf("%d", id)
				msgParts = append(msgParts, fmt.Sprintf("无可用下载资源，已转为 MoviePilot 订阅（ID %d）", id))
			}
		}
	default: // download
		downloaded, derr := s.downloadForFallback(runCtx, client, target, rec)
		if derr != nil {
			msgParts = append(msgParts, "MoviePilot 搜索下载失败："+derr.Error())
		} else if downloaded {
			msgParts = append(msgParts, "已让 MoviePilot 搜索下载")
		} else {
			msgParts = append(msgParts, "MoviePilot 未找到可下载资源")
		}
	}

	rec.Progress = 100
	rec.Message = strings.Join(msgParts, "；")
	// 只有真正产生了外部动作（建立了 MP 订阅，或拿到了 MP 侧下载 ID）才算成功。
	// 注意：不能拿 hasMoviePilotConfig() 当成功条件——执行到此处说明 requireMoviePilotConfig()
	// 已经成功返回过，该判断恒为 true，会把「搜索下载失败」「未找到可下载资源」也标成成功。
	if subscribed || rec.ExternalID != "" {
		rec.Status = domain.MoviePilotFallbackSucceeded
	} else {
		rec.Status = domain.MoviePilotFallbackFailed
	}

	// 「确认 MP 已接管订阅后本地订阅自动暂停并保留已入库集数和记录；仅提交下载不暂停」
	if subscribed && target.SubscribeID != 0 {
		if err := s.pauseLocalSubscription(runCtx, target.SubscribeID); err != nil {
			s.logWarn(fmt.Sprintf("MoviePilot 兜底：暂停本地订阅 #%d 失败", target.SubscribeID), err)
		} else {
			rec.Message += "；MP 已接管订阅，本地订阅已自动暂停（保留已入库集数与记录）"
		}
	}

	if err := s.repo.SaveFallback(ctx, rec); err != nil {
		s.logWarn("MoviePilot 兜底：保存结果状态出错", err)
	}
	s.logInfo(fmt.Sprintf("MoviePilot 兜底：%s（%s）→ %s", rec.Title, rec.MediaKey, rec.Message))
}

// pauseLocalSubscription 暂停本地订阅；由 SetSubscriptionPauser 注入的实现处理。
// 未注入时不做任何事（兜底失败不影响本地订阅继续运行）。
func (s *Service) pauseLocalSubscription(ctx context.Context, subscribeID int64) error {
	if s == nil || s.subscriptionPauser == nil {
		return nil
	}
	return s.subscriptionPauser.PauseSubscription(ctx, subscribeID)
}

// addSubscribeForFallback 为降级记录添加 MoviePilot 订阅，返回 MP 侧订阅 ID。
func (s *Service) addSubscribeForFallback(ctx context.Context, client *Client, target FallbackTarget, rec *domain.MoviePilotFallback) (int64, error) {
	if rec.TmdbId <= 0 {
		return 0, fmt.Errorf("缺少 TMDB ID，无法添加 MoviePilot 订阅")
	}
	req := &CreateSubscribeRequest{
		Name:         rec.Title,
		Type:         rec.MediaType,
		TmdbId:       rec.TmdbId,
		Season:       rec.Season,
		TotalEpisode: target.TotalEpisodes,
		SavePath:     target.TargetDir,
	}
	id, err := client.CreateSubscribe(ctx, req)
	if err != nil {
		return 0, err
	}
	// 订阅已建立：触发一次搜索让 MP 立即开始找资源（失败不阻断）。
	if err := client.SearchSubscribe(ctx, id); err != nil {
		s.logWarn(fmt.Sprintf("MoviePilot 兜底：触发订阅搜索失败（ID %d）", id), err)
	}
	return id, nil
}

// downloadForFallback 让 MoviePilot 搜索并下载资源。
//
// 返回是否已成功提交下载。实现方式：复用现有已确认可用的订阅搜索接口
// （先确保存在订阅，再触发搜索下载），随后统计该订阅下是否已有下载任务；
// 无法判定时返回 false，交由调用方按动作回退到订阅。
func (s *Service) downloadForFallback(ctx context.Context, client *Client, target FallbackTarget, rec *domain.MoviePilotFallback) (bool, error) {
	if rec.TmdbId <= 0 {
		return false, fmt.Errorf("缺少 TMDB ID")
	}
	id, err := s.addSubscribeForFallback(ctx, client, target, rec)
	if err != nil {
		return false, err
	}
	downloads, derr := client.ListDownloads(ctx)
	if derr != nil {
		// 订阅已建立：保留 ExternalID 以便调用方判定已有外部动作。
		rec.ExternalID = fmt.Sprintf("%d", id)
		return false, derr
	}
	for _, d := range downloads {
		if d == nil {
			continue
		}
		// MP 下载任务带 media 信息，匹配 TMDB 视为该影片已有下载。
		if downloadMatchesTmdb(d, rec.TmdbId) {
			if rec.DownloadEpisodes == "" {
				rec.DownloadEpisodes = d.SeasonEpisode
			}
			rec.ExternalID = fmt.Sprintf("%d", id)
			return true, nil
		}
	}
	rec.ExternalID = fmt.Sprintf("%d", id)
	return false, nil
}

// failFallback 记录兜底失败（不中断调用方）。
func (s *Service) failFallback(ctx context.Context, rec *domain.MoviePilotFallback, cause error) {
	rec.Status = domain.MoviePilotFallbackFailed
	rec.Message = cause.Error()
	if err := s.repo.SaveFallback(ctx, rec); err != nil {
		s.logWarn("MoviePilot 兜底：保存失败状态出错", err)
	}
	s.logWarn(fmt.Sprintf("MoviePilot 兜底：影视 %s 降级被拒绝", rec.Title), cause)
}

// downloadMatchesTmdb 判断 MP 下载任务是否属于指定 TMDB 影视。
func downloadMatchesTmdb(d *DownloadTorrent, tmdbID int64) bool {
	if d == nil || tmdbID <= 0 || d.Media == nil {
		return false
	}
	return mediaValueMatchesTmdb(d.Media, tmdbID)
}

// mediaValueMatchesTmdb 从 MP 下载条目的 media 字段里比对 tmdb_id。
func mediaValueMatchesTmdb(media map[string]any, tmdbID int64) bool {
	for _, k := range []string{"tmdb_id", "tmdbid"} {
		if v, ok := media[k]; ok {
			if id, ok := mpToInt64(v); ok && id == tmdbID {
				return true
			}
		}
	}
	return false
}

// mpToInt64 宽松数值转换（MP 返回的 JSON 数字可能是 float64/string）。
func mpToInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	case string:
		var out int64
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%d", &out); err != nil {
			return 0, false
		}
		return out, true
	default:
		return 0, false
	}
}

// isFallbackNotFound 判定兜底记录「不存在」。
func isFallbackNotFound(err error) bool {
	if err == nil {
		return false
	}
	if ae, ok := domain.AsAppError(err); ok {
		return ae.Code == domain.CodeNotFound
	}
	return false
}

// logWarn / logInfo 兜底日志（s.log 为空时回退默认日志器）。
func (s *Service) logWarn(msg string, err error) {
	if s == nil {
		return
	}
	s.log.Warn(msg, "err", err)
}

func (s *Service) logInfo(msg string) {
	if s == nil {
		return
	}
	s.log.Info(msg)
}
