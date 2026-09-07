package models

import (
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"

	"gorm.io/gorm/clause"
)

// Pan139DirCache 中国移动云盘（139）目录列表持久缓存（折中版）
//
// 只存目录指纹（上次列出该目录时父列表报告的 UTime）与扫描时间，不存子项明细——
// 子项明细已由 sync_files 表承担。作用：
//  1. 增量同步时指纹命中（UTime 未变且快照未过期）→ 整棵子树免 API 列表；
//  2. TTL 过期强制重列一次，兜底「目录 UTime 不随内容变化」的语义风险（漏检上限=TTL）；
//  3. 跨任务/跨重启复用，取代「每天首次同步强制全量」的成本。
type Pan139DirCache struct {
	BaseModel
	AccountID uint   `gorm:"not null;default:0;uniqueIndex:idx_pan139_dircache_acct_dir,priority:1"`
	DirID     string `gorm:"type:varchar(64);not null;default:'';uniqueIndex:idx_pan139_dircache_acct_dir,priority:2"`
	DirPath   string `gorm:"type:varchar(1024);not null;default:''"` // 快照时目录远程路径（便于排查，非键）
	MTime     int64  `gorm:"not null;default:0"`                     // 快照时父列表报告的目录修改时间（unix 秒，0=未知/入口目录不缓存）
	ScannedAt int64  `gorm:"not null;default:0"`                     // 快照写入时间（unix 秒）
}

// FindPan139DirCache 查询目录指纹快照
func FindPan139DirCache(accountID uint, dirID string) *Pan139DirCache {
	if dirID == "" {
		return nil
	}
	var row Pan139DirCache
	if err := db.Db.Where("account_id = ? AND dir_id = ?", accountID, dirID).First(&row).Error; err != nil {
		return nil
	}
	return &row
}

// UpsertPan139DirCache 写入/刷新目录指纹（列表成功后调用，权威刷新）
func UpsertPan139DirCache(accountID uint, dirID, dirPath string, mtime int64) {
	if dirID == "" || mtime <= 0 {
		return // 入口目录/UTime 不可信的目录不做指纹
	}
	row := Pan139DirCache{
		AccountID: accountID,
		DirID:     dirID,
		DirPath:   dirPath,
		MTime:     mtime,
		ScannedAt: time.Now().Unix(),
	}
	if err := db.Db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "account_id"}, {Name: "dir_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"dir_path", "m_time", "scanned_at"}),
	}).Create(&row).Error; err != nil {
		helpers.AppLogger.Warnf("写入移动云盘目录缓存失败 dir=%s：%v", dirID, err)
	}
}

// Pan139DirCacheFresh 判断快照是否指纹命中且未过期
func (row *Pan139DirCache) Pan139DirCacheFresh(mtime int64, ttlSeconds int64) bool {
	if row == nil || row.MTime <= 0 || row.ScannedAt <= 0 {
		return false
	}
	if row.MTime != mtime {
		return false
	}
	return time.Now().Unix()-row.ScannedAt < ttlSeconds
}
