package rename

import (
	"context"

	"diy-strm/internal/models"
)

type RenameBase struct {
	scrapePath *models.ScrapePath
	ctx        context.Context
}
