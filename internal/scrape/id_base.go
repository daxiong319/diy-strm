package scrape

import (
	"diy-strm/internal/models"
	"context"
)

type IdBase struct {
	tmdbImpl   TmdbImpl
	scrapePath *models.ScrapePath
	ctx        context.Context
}
