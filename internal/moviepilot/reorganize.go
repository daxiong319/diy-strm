package moviepilot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
)

// ReorganizeFile 整理历史「重新整理」入口：
// 用户手动指定 TMDB ID/标题/媒体类型后，对单文件重新执行整理
// （TMDB 校验直接按指定 ID 查详情，不再名称搜索）。
// fileID/parentID 为网盘文件 ID 与父目录 ID（ExtraJSON 中记录）。
// 返回整理成功的目标相对目录（相对已整理根目录）。
func ReorganizeFile(ctx context.Context, account *models.Account, fileID, parentID, fileName string, media *IdentifyResult) (string, error) {
	if account == nil {
		return "", errors.New("账号不存在")
	}
	if fileID == "" || fileName == "" {
		return "", errors.New("记录中缺少网盘文件信息，文件可能已被移动或删除")
	}
	if media == nil {
		return "", errors.New("请填写媒体信息")
	}
	if strings.TrimSpace(media.Title) == "" {
		return "", errors.New("请填写媒体标题")
	}
	if media.TmdbId <= 0 {
		return "", errors.New("请填写 TMDB ID")
	}
	if media.Category != "tv" {
		media.Category = "movie"
	}
	entry := organizeEntry{ID: fileID, Name: fileName, ParentID: parentID}
	dirCache := map[string]string{}
	root := organizeRootForAccount(account.ID)
	relDir, err := organizeOneFile(ctx, account, entry, root, dirCache, media)
	if err != nil {
		return "", fmt.Errorf("重新整理失败：%v", err)
	}
	return relDir, nil
}

// organizeRootForAccount 重整理目标根目录：
// 优先取该账号自动整理配置的已整理根目录，否则按全局上传根目录推导（父目录/已整理）。
func organizeRootForAccount(accountID uint) string {
	if cfg, err := models.GetAutoOrganizeConfigByAccount(accountID); err == nil && cfg != nil {
		if r := strings.Trim(cfg.OrganizedRoot, "/"); r != "" {
			return r
		}
	}
	root := organizeRootPath(models.MoviePilotConfigGlobal.UploadRoot)
	if root != "" {
		helpers.AppLogger.Debugf("重整理使用全局整理根目录：%s", root)
	}
	return root
}
