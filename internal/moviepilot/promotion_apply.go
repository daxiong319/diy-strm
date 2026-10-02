package moviepilot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"litepan/internal/domain"
)

// promotionDefaultPatienceHours 未配置时的默认耐心时长。
const promotionDefaultPatienceHours = 12

// applyPromotionLadder 促销阶梯推进：
// 订阅长期没有新下载时逐级放宽允许的促销类型（提高命中率），
// 一旦出现新下载立刻重置回最高优先层；订阅不再处于订阅中状态时清除过滤条件。
func (s *Service) applyPromotionLadder(ctx context.Context, cfg *domain.MoviePilotConfig, client *Client) error {
	order := PromotionOrderList(cfg.PromotionOrder)
	if len(order) == 0 {
		return nil
	}
	patienceHours := cfg.PromotionPatienceHours
	if patienceHours <= 0 {
		patienceHours = promotionDefaultPatienceHours
	}
	patience := time.Duration(patienceHours) * time.Hour
	now := s.now()

	subs, err := client.ListSubscribes(ctx)
	if err != nil {
		return fmt.Errorf("获取订阅列表失败：%w", err)
	}
	ladders, err := s.repo.ListPromotionLadders(ctx)
	if err != nil {
		return fmt.Errorf("读取促销阶梯失败：%w", err)
	}
	ladderBySub := make(map[int64]*domain.MoviePilotPromotionLadder, len(ladders))
	for i := range ladders {
		l := &ladders[i]
		ladderBySub[l.SubscribeID] = l
	}

	// 每个 TMDB ID 最近一次下载时间，用于判定订阅是否「有新下载」
	latestDownload := s.latestDownloadTimes(ctx, client)

	active := make(map[int64]struct{}, len(subs))
	for _, sub := range subs {
		if ctx.Err() != nil {
			return nil
		}
		if sub == nil || sub.TmdbId <= 0 {
			continue
		}
		ladder := ladderBySub[sub.ID]
		if sub.State != "R" {
			// 订阅已停止/完成：清除促销过滤并删除阶梯状态
			if ladder != nil {
				if err := client.UpdateSubscribeInclude(ctx, sub.ID, ""); err != nil {
					s.log.Warn("MoviePilot 促销阶梯：清除订阅 include 失败", "subscribe", sub.Name, "err", err)
				}
				if err := s.repo.DeletePromotionLadder(ctx, sub.ID); err != nil {
					s.log.Warn("MoviePilot 促销阶梯：删除阶梯状态失败", "subscribe", sub.Name, "err", err)
				}
			}
			continue
		}
		active[sub.ID] = struct{}{}
		if ladder == nil {
			ladder = &domain.MoviePilotPromotionLadder{SubscribeID: sub.ID, Tier: 0, TierStartedAt: now.Unix()}
		}
		decision := AdvancePromotionLadder(order, ladder.Tier, ladder.TierStartedAt, now.Unix(), latestDownload[sub.TmdbId].Unix(), int64(patience/time.Second))
		if decision.Changed {
			ladder.Tier = decision.Tier
			ladder.TierStartedAt = decision.TierStartedAt
			if decision.Reset {
				s.log.Info("MoviePilot 促销阶梯：订阅有新下载，重置回最高优先层",
					"subscribe", sub.Name, "tier", LadderTierLabel(order, decision.Tier))
			}
			if decision.Relaxed {
				s.log.Info("MoviePilot 促销阶梯：当前层长时间无新下载，放宽",
					"subscribe", sub.Name, "tier", LadderTierLabel(order, decision.Tier), "hours", patienceHours)
				// 放宽后立即触发一次搜索，让新条件尽快生效
				if err := client.SearchSubscribe(ctx, sub.ID); err != nil {
					s.log.Warn("MoviePilot 促销阶梯放宽后触发搜索失败", "subscribe", sub.Name, "err", err)
				}
			}
			if err := s.repo.SavePromotionLadder(ctx, ladder); err != nil {
				s.log.Warn("MoviePilot 促销阶梯：保存阶梯状态失败", "subscribe", sub.Name, "err", err)
			}
		}
		want := PromotionTierIncludeRegex(order, ladder.Tier)
		if strings.TrimSpace(sub.Include) == want {
			continue
		}
		if err := client.UpdateSubscribeInclude(ctx, sub.ID, want); err != nil {
			s.log.Error("MoviePilot 促销阶梯：设置订阅 include 失败", "subscribe", sub.Name, "err", err)
			continue
		}
		s.log.Info("MoviePilot 促销阶梯：订阅允许促销已调整",
			"subscribe", sub.Name, "include", want, "tier", ladder.Tier+1, "total", len(order))
	}
	// 清理已不再活跃的阶梯状态
	for i := range ladders {
		l := &ladders[i]
		if _, ok := active[l.SubscribeID]; ok {
			continue
		}
		if _, known := ladderBySub[l.SubscribeID]; !known {
			continue
		}
		if err := s.repo.DeletePromotionLadder(ctx, l.SubscribeID); err != nil {
			s.log.Warn("MoviePilot 促销阶梯：清理失效阶梯失败", "subscribe_id", l.SubscribeID, "err", err)
		}
	}
	return nil
}

// latestDownloadTimes 汇总每个 TMDB ID 最近一次下载时间。
func (s *Service) latestDownloadTimes(ctx context.Context, client *Client) map[int64]time.Time {
	out := make(map[int64]time.Time)
	history, err := client.ListDownloadHistory(ctx, 1, 50)
	if err != nil {
		s.log.Warn("MoviePilot 促销阶梯：读取下载历史失败", "err", err)
		return out
	}
	for _, h := range history {
		if h == nil || h.TmdbId <= 0 {
			continue
		}
		at, err := time.ParseInLocation("2006-01-02 15:04:05", h.Date, time.Local)
		if err != nil {
			continue
		}
		if prev, ok := out[h.TmdbId]; !ok || at.After(prev) {
			out[h.TmdbId] = at
		}
	}
	return out
}

// autoDeleteSeeds 按保留时长清理已上传完成的做种任务。
// 仅删除上传已成功的任务：未上传完成的种子删掉会导致文件缺失。
func (s *Service) autoDeleteSeeds(ctx context.Context, cfg *domain.MoviePilotConfig, client *Client) error {
	retention := cfg.SeedRetentionHours
	if retention <= 0 {
		return nil
	}
	// 未配置 qBittorrent 时退化为通过 MoviePilot 删除（能力有限，仅覆盖管理中的任务）
	return s.autoDeleteSeedsViaMP(ctx, cfg, client, retention)
}

// autoDeleteSeedsViaMP 通过 MoviePilot 接口删除做种任务。
// 局限：MoviePilot 的下载列表只包含仍在其管理中的任务，纯做种的历史种子无法覆盖；
// 估算做种起点时优先取下载历史，取不到则回退任务更新时间（必然晚于真实起点，只会偏晚删除）。
func (s *Service) autoDeleteSeedsViaMP(ctx context.Context, cfg *domain.MoviePilotConfig, client *Client, retentionHours int) error {
	downloads, err := client.ListDownloads(ctx)
	if err != nil {
		return fmt.Errorf("获取下载任务失败：%w", err)
	}
	retention := time.Duration(retentionHours) * time.Hour
	now := s.now()
	for _, t := range downloads {
		if ctx.Err() != nil {
			return nil
		}
		if t == nil || strings.TrimSpace(t.Hash) == "" {
			continue
		}
		if t.Progress < 100 {
			continue
		}
		switch t.State {
		case "seeding", "completed", "paused":
		default:
			continue
		}
		task, fErr := s.repo.FindUploadTaskByHash(ctx, t.Hash)
		if fErr != nil || task == nil || task.Status != domain.MoviePilotUploadUploaded {
			continue
		}
		started := s.seedStartedAt(ctx, client, t.Hash)
		if started.IsZero() {
			started = task.UpdatedAt
		}
		if started.IsZero() || now.Sub(started) < retention {
			continue
		}
		if err := client.DeleteDownload(ctx, t.Hash, t.Name); err != nil {
			s.log.Error("MoviePilot 自动删种失败", "hash", t.Hash, "err", err)
			continue
		}
		s.log.Info("MoviePilot 自动删种完成", "hash", t.Hash, "title", task.Title,
			"seeded", formatDurationCN(now.Sub(started)), "retention_hours", retentionHours)
	}
	return nil
}

// seedStartedAt 由下载历史推断做种起点；找不到返回零值。
func (s *Service) seedStartedAt(ctx context.Context, client *Client, hash string) time.Time {
	history, err := client.ListDownloadHistory(ctx, 1, 50)
	if err != nil {
		return time.Time{}
	}
	for _, h := range history {
		if h == nil || h.DownloadHash != hash {
			continue
		}
		at, pErr := time.ParseInLocation("2006-01-02 15:04:05", h.Date, time.Local)
		if pErr != nil {
			return time.Time{}
		}
		return at
	}
	return time.Time{}
}

// formatDurationCN 中文时长格式（天/小时/分钟）。
func formatDurationCN(d time.Duration) string {
	if d >= 24*time.Hour {
		days := int(d / (24 * time.Hour))
		hours := int((d % (24 * time.Hour)) / time.Hour)
		return fmt.Sprintf("%dd%dh", days, hours)
	}
	hours := int(d / time.Hour)
	minutes := int((d % time.Hour) / time.Minute)
	return fmt.Sprintf("%dh%dm", hours, minutes)
}

// SubscribeSearch 触发订阅搜索（供 API 层调用）。
func (s *Service) SubscribeSearch(ctx context.Context, subscribeID int64) error {
	cfg, err := s.repo.LoadConfig(ctx)
	if err != nil {
		return domain.Wrap(domain.CodeInternal, err)
	}
	if strings.TrimSpace(cfg.BaseUrl) == "" || strings.TrimSpace(cfg.ApiToken) == "" {
		return domain.Errorf(domain.CodeValidation, "MoviePilot 配置不完整（地址或 API Token 为空）")
	}
	client := NewClient(cfg.BaseUrl, cfg.ApiToken)
	return client.SearchSubscribe(ctx, subscribeID)
}

// TestConnection 测试 MoviePilot 连接（供 API 层调用）。
// baseURL/token 为空时回退到已保存的配置，便于「先保存再测试」与「未保存直接测试」两种用法。
func (s *Service) TestConnection(ctx context.Context, baseURL, token string) error {
	baseURL = strings.TrimSpace(baseURL)
	token = strings.TrimSpace(token)
	if baseURL == "" || token == "" {
		cfg, err := s.repo.LoadConfig(ctx)
		if err != nil {
			return domain.Wrap(domain.CodeInternal, err)
		}
		if baseURL == "" {
			baseURL = cfg.BaseUrl
		}
		if token == "" {
			token = cfg.ApiToken
		}
	}
	if baseURL == "" || token == "" {
		return domain.Errorf(domain.CodeValidation, "MoviePilot 配置不完整（地址或 API Token 为空）")
	}
	return NewClient(baseURL, token).TestConnection(ctx)
}

// ---- 只读代理方法（供 API 层直接透传 MoviePilot 接口） ----

// ListSubscribes 列出 MoviePilot 订阅。
func (s *Service) ListSubscribes(ctx context.Context) ([]*Subscribe, error) {
	client, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	return client.ListSubscribes(ctx)
}

// CreateSubscribe 新增订阅。
func (s *Service) CreateSubscribe(ctx context.Context, req CreateSubscribeRequest) (int64, error) {
	client, err := s.client(ctx)
	if err != nil {
		return 0, err
	}
	return client.CreateSubscribe(ctx, &req)
}

// DeleteSubscribe 删除订阅。
func (s *Service) DeleteSubscribe(ctx context.Context, id int64) error {
	client, err := s.client(ctx)
	if err != nil {
		return err
	}
	return client.DeleteSubscribe(ctx, id)
}

// UpdateSubscribeStatus 修改订阅状态。
func (s *Service) UpdateSubscribeStatus(ctx context.Context, id int64, state string) error {
	client, err := s.client(ctx)
	if err != nil {
		return err
	}
	return client.UpdateSubscribeStatus(ctx, id, state)
}

// ListDownloads 列出下载任务。
func (s *Service) ListDownloads(ctx context.Context) ([]*DownloadTorrent, error) {
	client, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	return client.ListDownloads(ctx)
}

// client 依据已保存配置构造 MoviePilot 客户端。
func (s *Service) client(ctx context.Context) (*Client, error) {
	if s == nil || s.repo == nil {
		return nil, domain.Errorf(domain.CodeNotImplement, "MoviePilot 服务未配置")
	}
	cfg, err := s.repo.LoadConfig(ctx)
	if err != nil {
		return nil, domain.Wrap(domain.CodeInternal, err)
	}
	if strings.TrimSpace(cfg.BaseUrl) == "" || strings.TrimSpace(cfg.ApiToken) == "" {
		return nil, domain.Errorf(domain.CodeValidation, "MoviePilot 配置不完整（地址或 API Token 为空）")
	}
	return NewClient(cfg.BaseUrl, cfg.ApiToken), nil
}

// ListUploadTasks 分页列出上传任务。
func (s *Service) ListUploadTasks(ctx context.Context, page, pageSize int, status string) ([]domain.MoviePilotUploadTask, int64, error) {
	if s == nil || s.repo == nil {
		return nil, 0, domain.Errorf(domain.CodeNotImplement, "MoviePilot 服务未配置")
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 20
	}
	return s.repo.ListUploadTasks(ctx, page, pageSize, status)
}

// ListOrganizeHistory 列出整理历史。
func (s *Service) ListOrganizeHistory(ctx context.Context, limit int) ([]domain.MoviePilotOrganizeHistory, error) {
	if s == nil || s.repo == nil {
		return nil, domain.Errorf(domain.CodeNotImplement, "MoviePilot 服务未配置")
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	return s.repo.ListOrganizeHistory(ctx, limit)
}
