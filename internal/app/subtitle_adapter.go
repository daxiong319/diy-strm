package app

import (
	"context"
	"strings"

	"litepan/internal/mediaorganize"
	"litepan/internal/subtitle"
)

// subtitleProcessorAdapter 把字幕服务接到整理流程的完成钩子上（Muvyo 移植③）。
//
// 依赖方向刻意是 app → subtitle：mediaorganize 只认自己的 SubtitleProcessor
// 接口，不认识 subtitle 包，因此不会出现 mediaorganize ↔ subtitle 的循环依赖。
type subtitleProcessorAdapter struct {
	svc *subtitle.Service
}

func (a subtitleProcessorAdapter) ProcessOrganizedVideo(
	ctx context.Context,
	videoPath, title string,
	year, season, episode int,
	mediaType string,
	tmdbID int64,
) error {
	if a.svc == nil {
		return nil
	}
	cfg := a.svc.Config()
	// 总开关与自动匹配开关任一未开都不处理：
	//   - Enabled 关 = 字幕模块整体停用；
	//   - AutoMatch 关 = 用户只想手动搜字幕，整理流程不得替他下载。
	// 注意 auto_download 不再单独拦一道：ProcessVideo 内部按该开关决定
	// 「只挑不下载」还是「挑完就下」，重复判断会让语义分叉。
	if !cfg.Enabled || !cfg.AutoMatch {
		return nil
	}
	name := strings.TrimSpace(videoPath)
	if name == "" {
		return nil
	}
	meta := subtitle.MediaMeta{
		Title:     strings.TrimSpace(title),
		Year:      year,
		Season:    season,
		Episode:   episode,
		MediaType: strings.TrimSpace(mediaType),
		TmdbId:    tmdbID,
	}
	// force=false：已有同名字幕时跳过，避免每轮整理都覆盖用户手工校正过的字幕。
	_, err := a.svc.ProcessVideo(ctx, name, meta, false)
	return err
}

// 编译期断言：适配器必须满足整理流程的钩子接口。
var _ mediaorganize.SubtitleProcessor = subtitleProcessorAdapter{}
