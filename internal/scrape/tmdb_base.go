package scrape

import (
	"diy-strm/internal/models"
	"diy-strm/internal/tmdb"
	"context"
)

// 从tmdb刮削元数据
type TmdbBase struct {
	scrapePath *models.ScrapePath
	ctx        context.Context
	Client     *tmdb.Client
}
