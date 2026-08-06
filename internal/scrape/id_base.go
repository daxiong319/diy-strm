package scrape

import (
	"context"

	"diy-strm/internal/models"
)

type IdBase struct {
	tmdbImpl   TmdbImpl
	scrapePath *models.ScrapePath
	ctx        context.Context
}
