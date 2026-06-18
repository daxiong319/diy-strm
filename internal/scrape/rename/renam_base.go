package rename

import (
	"diy-strm/internal/models"
	"context"
)

type RenameBase struct {
	scrapePath *models.ScrapePath
	ctx        context.Context
}
