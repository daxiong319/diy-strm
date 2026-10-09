package app

import (
	"gorm.io/gorm"

	"litepan/internal/mediaupgrade"
)

// wireMediaUpgrade 构造洗版服务。
//
// 必须在 ddb.Init 之后调用：洗版与 discovery 共用同一个 GORM 句柄
// （media_upgrade_* 三张表由 0037 迁移建在主 SQLite 里，但访问走 gorm），
// 而 ddb.Init 是 discoverInit 里的第一步，早于任何业务服务的构造。
//
// 句柄为 nil 时返回 nil：接口层对 nil 的处理是「该操作不支持」，
// 而不是让管理页出现一个点开就 500 的入口。
func wireMediaUpgrade(st *storeBundle, db *gorm.DB) *mediaupgrade.Service {
	if db == nil || st == nil || st.settings == nil {
		return nil
	}
	return mediaupgrade.New(db, st.settings)
}
