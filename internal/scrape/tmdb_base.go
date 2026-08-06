package scrape

import (
	"context"

	"diy-strm/internal/models"
	"diy-strm/internal/tmdb"
)

// 从 TMDB 刮削元数据
type TmdbBase struct {
	scrapePath *models.ScrapePath
	ctx        context.Context
	Client     *tmdb.Client
}
