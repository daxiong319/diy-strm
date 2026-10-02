package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	"litepan/internal/domain"
)

type embyIndexRepo struct{ db *DB }

const selectEmbyItemCols = `SELECT id,item_id,item_id_int,server_id,name,type,parent_id,series_id,series_name,
	season_id,season_name,library_id,path,pick_code,media_source_path,index_number,parent_index_number,
	production_year,premiere_date,date_created,date_created_time,date_modified,date_modified_time,is_folder,
	last_seen_sync_run,last_seen_at FROM emby_media_items`

func scanEmbyItem(row interface{ Scan(...any) error }) (domain.EmbyMediaItem, error) {
	var it domain.EmbyMediaItem
	var isFolder int
	err := row.Scan(&it.ID, &it.ItemID, &it.ItemIDInt, &it.ServerID, &it.Name, &it.Type, &it.ParentID,
		&it.SeriesID, &it.SeriesName, &it.SeasonID, &it.SeasonName, &it.LibraryID, &it.Path, &it.PickCode,
		&it.MediaSourcePath, &it.IndexNumber, &it.ParentIndexNumber, &it.ProductionYear, &it.PremiereDate,
		&it.DateCreated, &it.DateCreatedTime, &it.DateModified, &it.DateModifiedTime, &isFolder,
		&it.LastSeenSyncRun, &it.LastSeenAt)
	if err != nil {
		return domain.EmbyMediaItem{}, err
	}
	it.IsFolder = isFolder != 0
	return it, nil
}

func (r *embyIndexRepo) UpsertLibraries(ctx context.Context, libs []domain.EmbyLibrary) error {
	for _, lib := range libs {
		if strings.TrimSpace(lib.LibraryID) == "" {
			continue
		}
		_, err := r.db.write.ExecContext(ctx,
			`INSERT INTO emby_libraries(name,library_id,sync_path_id)
			 VALUES(?,?,?)
			 ON CONFLICT(library_id) DO UPDATE SET name=excluded.name, updated_at=CURRENT_TIMESTAMP`,
			lib.Name, lib.LibraryID, lib.SyncPathID)
		if err != nil {
			return wrapDB(err)
		}
	}
	return nil
}

func (r *embyIndexRepo) CleanupDeletedLibraries(ctx context.Context, activeLibraryIDs []string) error {
	if len(activeLibraryIDs) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(activeLibraryIDs)), ",")
	args := make([]any, 0, len(activeLibraryIDs))
	for _, id := range activeLibraryIDs {
		args = append(args, id)
	}
	tx, err := r.db.write.BeginTx(ctx, nil)
	if err != nil {
		return wrapDB(err)
	}
	defer func() { _ = tx.Rollback() }()

	// 先清理媒体库的同步路径关联，再删媒体库本身。
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM emby_library_sync_paths WHERE library_id NOT IN (`+placeholders+`)`, args...); err != nil {
		return wrapDB(err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM emby_libraries WHERE library_id NOT IN (`+placeholders+`)`, args...); err != nil {
		return wrapDB(err)
	}
	return wrapDB(tx.Commit())
}

func (r *embyIndexRepo) ListLibraries(ctx context.Context) ([]domain.EmbyLibrary, error) {
	rows, err := r.db.read.QueryContext(ctx, `SELECT id,name,library_id,sync_path_id FROM emby_libraries ORDER BY id`)
	if err != nil {
		return nil, wrapDB(err)
	}
	defer rows.Close()
	var out []domain.EmbyLibrary
	for rows.Next() {
		var lib domain.EmbyLibrary
		if err := rows.Scan(&lib.ID, &lib.Name, &lib.LibraryID, &lib.SyncPathID); err != nil {
			return nil, wrapDB(err)
		}
		out = append(out, lib)
	}
	return out, wrapDB(rows.Err())
}

func (r *embyIndexRepo) CreateOrUpdateItem(ctx context.Context, item *domain.EmbyMediaItem) error {
	if item == nil || strings.TrimSpace(item.ItemID) == "" {
		return domain.Errorf(domain.CodeValidation, "Emby 条目 ID 不能为空")
	}
	_, err := r.db.write.ExecContext(ctx,
		`INSERT INTO emby_media_items
		  (item_id,item_id_int,server_id,name,type,parent_id,series_id,series_name,season_id,season_name,
		   library_id,path,pick_code,media_source_path,index_number,parent_index_number,production_year,
		   premiere_date,date_created,date_created_time,date_modified,date_modified_time,is_folder,
		   last_seen_sync_run,last_seen_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(item_id) DO UPDATE SET
		   item_id_int=excluded.item_id_int, server_id=excluded.server_id, name=excluded.name,
		   type=excluded.type, parent_id=excluded.parent_id, series_id=excluded.series_id,
		   series_name=excluded.series_name, season_id=excluded.season_id, season_name=excluded.season_name,
		   library_id=excluded.library_id, path=excluded.path, pick_code=excluded.pick_code,
		   media_source_path=excluded.media_source_path, index_number=excluded.index_number,
		   parent_index_number=excluded.parent_index_number, production_year=excluded.production_year,
		   premiere_date=excluded.premiere_date, date_created=excluded.date_created,
		   date_created_time=excluded.date_created_time, date_modified=excluded.date_modified,
		   date_modified_time=excluded.date_modified_time, is_folder=excluded.is_folder,
		   last_seen_sync_run=excluded.last_seen_sync_run, last_seen_at=excluded.last_seen_at,
		   updated_at=CURRENT_TIMESTAMP`,
		item.ItemID, item.ItemIDInt, item.ServerID, item.Name, item.Type, item.ParentID, item.SeriesID,
		item.SeriesName, item.SeasonID, item.SeasonName, item.LibraryID, item.Path, item.PickCode,
		item.MediaSourcePath, item.IndexNumber, item.ParentIndexNumber, item.ProductionYear, item.PremiereDate,
		item.DateCreated, item.DateCreatedTime, item.DateModified, item.DateModifiedTime, boolToInt(item.IsFolder),
		item.LastSeenSyncRun, item.LastSeenAt)
	return wrapDB(err)
}

func (r *embyIndexRepo) GetItem(ctx context.Context, itemID string) (*domain.EmbyMediaItem, error) {
	if strings.TrimSpace(itemID) == "" {
		return nil, domain.Errorf(domain.CodeValidation, "Emby 条目 ID 不能为空")
	}
	row := r.db.read.QueryRowContext(ctx, selectEmbyItemCols+` WHERE item_id=?`, itemID)
	item, err := scanEmbyItem(row)
	if err != nil {
		return nil, wrapDB(err)
	}
	return &item, nil
}

func (r *embyIndexRepo) itemsByField(ctx context.Context, field, value string) ([]domain.EmbyMediaItem, error) {
	rows, err := r.db.read.QueryContext(ctx, selectEmbyItemCols+` WHERE `+field+`=? ORDER BY item_id_int`, value)
	if err != nil {
		return nil, wrapDB(err)
	}
	defer rows.Close()
	var out []domain.EmbyMediaItem
	for rows.Next() {
		item, err := scanEmbyItem(rows)
		if err != nil {
			return nil, wrapDB(err)
		}
		out = append(out, item)
	}
	return out, wrapDB(rows.Err())
}

func (r *embyIndexRepo) ItemsBySeasonID(ctx context.Context, seasonID string) ([]domain.EmbyMediaItem, error) {
	if strings.TrimSpace(seasonID) == "" {
		return nil, nil
	}
	return r.itemsByField(ctx, "season_id", seasonID)
}

func (r *embyIndexRepo) ItemsBySeriesID(ctx context.Context, seriesID string) ([]domain.EmbyMediaItem, error) {
	if strings.TrimSpace(seriesID) == "" {
		return nil, nil
	}
	return r.itemsByField(ctx, "series_id", seriesID)
}

func (r *embyIndexRepo) ItemsByLibraryID(ctx context.Context, libraryID string) ([]domain.EmbyMediaItem, error) {
	if strings.TrimSpace(libraryID) == "" {
		return nil, nil
	}
	return r.itemsByField(ctx, "library_id", libraryID)
}

func (r *embyIndexRepo) CountItems(ctx context.Context) (int64, error) {
	var total int64
	err := r.db.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM emby_media_items`).Scan(&total)
	return total, wrapDB(err)
}

// CleanupOrphanedItems 删除不在 validItemIDs 中的条目；validItemIDs 为空表示清空全部。
func (r *embyIndexRepo) CleanupOrphanedItems(ctx context.Context, validItemIDs []string) (int64, error) {
	if len(validItemIDs) == 0 {
		return r.deleteAllItems(ctx)
	}
	// 先取全部 item_id，再在内存里求差集，避免 SQL 变量数上限。
	rows, err := r.db.read.QueryContext(ctx, `SELECT id,item_id FROM emby_media_items`)
	if err != nil {
		return 0, wrapDB(err)
	}
	valid := make(map[string]struct{}, len(validItemIDs))
	for _, id := range validItemIDs {
		valid[id] = struct{}{}
	}
	var staleIDs []int64
	for rows.Next() {
		var id int64
		var itemID string
		if err := rows.Scan(&id, &itemID); err != nil {
			rows.Close()
			return 0, wrapDB(err)
		}
		if _, ok := valid[itemID]; !ok {
			staleIDs = append(staleIDs, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, wrapDB(err)
	}
	return r.deleteItemsByInternalIDs(ctx, staleIDs)
}

func (r *embyIndexRepo) CleanupStaleItemsByLibrarySyncRun(ctx context.Context, libraryID, syncRunID string) (int64, error) {
	if strings.TrimSpace(libraryID) == "" || strings.TrimSpace(syncRunID) == "" {
		return 0, nil
	}
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT id FROM emby_media_items WHERE library_id=? AND (last_seen_sync_run IS NULL OR last_seen_sync_run<>?)`,
		libraryID, syncRunID)
	if err != nil {
		return 0, wrapDB(err)
	}
	var staleIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, wrapDB(err)
		}
		staleIDs = append(staleIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, wrapDB(err)
	}
	if len(staleIDs) == 0 {
		return 0, nil
	}
	return r.deleteItemsByInternalIDs(ctx, staleIDs)
}

func (r *embyIndexRepo) DeleteItemByID(ctx context.Context, itemID string) error {
	if strings.TrimSpace(itemID) == "" {
		return nil
	}
	tx, err := r.db.write.BeginTx(ctx, nil)
	if err != nil {
		return wrapDB(err)
	}
	defer func() { _ = tx.Rollback() }()

	itemIDInt, _ := strconv.ParseInt(itemID, 10, 64)
	if _, err := tx.ExecContext(ctx, `DELETE FROM emby_media_sync_files WHERE emby_item_id=?`, itemIDInt); err != nil {
		return wrapDB(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM emby_media_items WHERE item_id=?`, itemID); err != nil {
		return wrapDB(err)
	}
	return wrapDB(tx.Commit())
}

func (r *embyIndexRepo) DeleteItemsBySeasonID(ctx context.Context, seasonID string) error {
	if strings.TrimSpace(seasonID) == "" {
		return nil
	}
	return r.deleteItemsByField(ctx, "season_id", seasonID)
}

func (r *embyIndexRepo) DeleteItemsBySeriesID(ctx context.Context, seriesID string) error {
	if strings.TrimSpace(seriesID) == "" {
		return nil
	}
	return r.deleteItemsByField(ctx, "series_id", seriesID)
}

func (r *embyIndexRepo) deleteItemsByField(ctx context.Context, field, value string) error {
	tx, err := r.db.write.BeginTx(ctx, nil)
	if err != nil {
		return wrapDB(err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM emby_media_sync_files WHERE emby_item_id IN
		   (SELECT item_id_int FROM emby_media_items WHERE `+field+`=?)`, value); err != nil {
		return wrapDB(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM emby_media_items WHERE `+field+`=?`, value); err != nil {
		return wrapDB(err)
	}
	return wrapDB(tx.Commit())
}

// deleteItemsByInternalIDs 删除内部主键集合对应的条目及其文件关联。
func (r *embyIndexRepo) deleteItemsByInternalIDs(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := r.db.write.BeginTx(ctx, nil)
	if err != nil {
		return 0, wrapDB(err)
	}
	defer func() { _ = tx.Rollback() }()

	const batchSize = 500
	var removed int64
	for start := 0; start < len(ids); start += batchSize {
		end := start + batchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]any, 0, len(batch))
		for _, id := range batch {
			args = append(args, id)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM emby_media_sync_files WHERE emby_item_id IN
			   (SELECT item_id_int FROM emby_media_items WHERE id IN (`+placeholders+`))`, args...); err != nil {
			return 0, wrapDB(err)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM emby_media_items WHERE id IN (`+placeholders+`)`, args...)
		if err != nil {
			return 0, wrapDB(err)
		}
		if n, err := res.RowsAffected(); err == nil {
			removed += n
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, wrapDB(err)
	}
	return removed, nil
}

func (r *embyIndexRepo) deleteAllItems(ctx context.Context) (int64, error) {
	tx, err := r.db.write.BeginTx(ctx, nil)
	if err != nil {
		return 0, wrapDB(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM emby_media_sync_files`); err != nil {
		return 0, wrapDB(err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM emby_media_items`)
	if err != nil {
		return 0, wrapDB(err)
	}
	n, _ := res.RowsAffected()
	return n, wrapDB(tx.Commit())
}

func (r *embyIndexRepo) CreateMediaSyncFile(ctx context.Context, rel *domain.EmbyMediaSyncFile) error {
	if rel == nil {
		return nil
	}
	// 表上没有唯一约束（老版也没有），因此按业务键查询去重后写入，保持「存在则跳过」语义。
	var count int64
	if err := r.db.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM emby_media_sync_files WHERE emby_item_id=? AND sync_file_id=? AND pick_code=?`,
		rel.EmbyItemID, rel.SyncFileID, rel.PickCode).Scan(&count); err != nil {
		return wrapDB(err)
	}
	if count > 0 {
		return nil
	}
	_, err := r.db.write.ExecContext(ctx,
		`INSERT INTO emby_media_sync_files(emby_item_id,sync_file_id,pick_code,sync_path_id,account_id,root_id,relative_path,file_name)
		 VALUES(?,?,?,?,?,?,?,?)`,
		rel.EmbyItemID, rel.SyncFileID, rel.PickCode, rel.SyncPathID, rel.AccountID, rel.RootID,
		rel.RelativePath, rel.FileName)
	return wrapDB(err)
}

func (r *embyIndexRepo) MediaSyncFilesByItemID(ctx context.Context, embyItemID int64) ([]domain.EmbyMediaSyncFile, error) {
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT id,emby_item_id,sync_file_id,pick_code,sync_path_id,account_id,root_id,relative_path,file_name
		 FROM emby_media_sync_files WHERE emby_item_id=? ORDER BY id`, embyItemID)
	if err != nil {
		return nil, wrapDB(err)
	}
	defer rows.Close()
	var out []domain.EmbyMediaSyncFile
	for rows.Next() {
		var rel domain.EmbyMediaSyncFile
		if err := rows.Scan(&rel.ID, &rel.EmbyItemID, &rel.SyncFileID, &rel.PickCode, &rel.SyncPathID,
			&rel.AccountID, &rel.RootID, &rel.RelativePath, &rel.FileName); err != nil {
			return nil, wrapDB(err)
		}
		out = append(out, rel)
	}
	return out, wrapDB(rows.Err())
}

func (r *embyIndexRepo) DeleteMediaSyncFilesBySyncFileID(ctx context.Context, syncFileID int64) error {
	_, err := r.db.write.ExecContext(ctx, `DELETE FROM emby_media_sync_files WHERE sync_file_id=?`, syncFileID)
	return wrapDB(err)
}

func (r *embyIndexRepo) DeleteMediaSyncFilesByPickCode(ctx context.Context, pickCode string) error {
	if strings.TrimSpace(pickCode) == "" {
		return nil
	}
	_, err := r.db.write.ExecContext(ctx, `DELETE FROM emby_media_sync_files WHERE pick_code=?`, pickCode)
	return wrapDB(err)
}

func (r *embyIndexRepo) CreateOrUpdateLibrarySyncPath(ctx context.Context, libraryID string, syncPathID int64, libraryName string) error {
	if strings.TrimSpace(libraryID) == "" {
		return nil
	}
	_, err := r.db.write.ExecContext(ctx,
		`INSERT INTO emby_library_sync_paths(library_id,sync_path_id,library_name)
		 VALUES(?,?,?)
		 ON CONFLICT(library_id,sync_path_id) DO UPDATE SET library_name=excluded.library_name, updated_at=CURRENT_TIMESTAMP`,
		libraryID, syncPathID, libraryName)
	return wrapDB(err)
}

func (r *embyIndexRepo) DeleteLibrarySyncPathsBySyncPathID(ctx context.Context, syncPathID int64) error {
	_, err := r.db.write.ExecContext(ctx, `DELETE FROM emby_library_sync_paths WHERE sync_path_id=?`, syncPathID)
	return wrapDB(err)
}

func (r *embyIndexRepo) LibraryIDsBySyncPathID(ctx context.Context, syncPathID int64) (map[string]string, error) {
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT library_id,library_name FROM emby_library_sync_paths WHERE sync_path_id=?`, syncPathID)
	if err != nil {
		return nil, wrapDB(err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, wrapDB(err)
		}
		out[id] = name
	}
	return out, wrapDB(rows.Err())
}

func (r *embyIndexRepo) CleanupAllLibraryData(ctx context.Context) error {
	tx, err := r.db.write.BeginTx(ctx, nil)
	if err != nil {
		return wrapDB(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		`DELETE FROM emby_library_sync_paths`,
		`DELETE FROM emby_media_sync_files`,
		`DELETE FROM emby_media_items`,
		`DELETE FROM emby_libraries`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return wrapDB(err)
		}
	}
	return wrapDB(tx.Commit())
}

func (r *embyIndexRepo) CleanupUnselectedLibraryData(ctx context.Context, selectedLibraryIDs []string) error {
	// 空列表时 NOT IN (NULL) 对所有行求值为假，清理会被静默跳过：
	// 「取消勾选全部媒体库」应走全量清理路径。
	if len(selectedLibraryIDs) == 0 {
		return r.CleanupAllLibraryData(ctx)
	}
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT library_id FROM emby_libraries WHERE library_id NOT IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(selectedLibraryIDs)), ",")+`)`, toAnySlice(selectedLibraryIDs)...)
	if err != nil {
		return wrapDB(err)
	}
	var unselected []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return wrapDB(err)
		}
		unselected = append(unselected, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return wrapDB(err)
	}
	if len(unselected) == 0 {
		return nil
	}
	return r.cleanupLibrariesByIDs(ctx, unselected)
}

func (r *embyIndexRepo) cleanupLibrariesByIDs(ctx context.Context, libraryIDs []string) error {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(libraryIDs)), ",")
	args := toAnySlice(libraryIDs)
	tx, err := r.db.write.BeginTx(ctx, nil)
	if err != nil {
		return wrapDB(err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM emby_library_sync_paths WHERE library_id IN (`+placeholders+`)`, args...); err != nil {
		return wrapDB(err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM emby_media_sync_files WHERE emby_item_id IN
		   (SELECT item_id_int FROM emby_media_items WHERE library_id IN (`+placeholders+`))`, args...); err != nil {
		return wrapDB(err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM emby_media_items WHERE library_id IN (`+placeholders+`)`, args...); err != nil {
		return wrapDB(err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM emby_libraries WHERE library_id IN (`+placeholders+`)`, args...); err != nil {
		return wrapDB(err)
	}
	return wrapDB(tx.Commit())
}

func (r *embyIndexRepo) GetSyncCursor(ctx context.Context, configID string) (domain.EmbySyncCursor, error) {
	cur := domain.EmbySyncCursor{ConfigID: configID}
	var updated sql.NullString
	err := r.db.read.QueryRowContext(ctx,
		`SELECT config_id,last_saved_cursor_at,last_sync_time,last_incremental_at,updated_at
		 FROM emby_sync_state WHERE config_id=?`, configID).
		Scan(&cur.ConfigID, &cur.LastSavedCursorAt, &cur.LastSyncTime, &cur.LastIncrementalAt, &updated)
	if err != nil {
		if isNoRows(err) {
			// 首次同步没有游标记录，返回零值而非错误。
			return domain.EmbySyncCursor{ConfigID: configID}, nil
		}
		return cur, wrapDB(err)
	}
	cur.UpdatedAt = parseTS(updated)
	return cur, nil
}

func (r *embyIndexRepo) AdvanceSyncCursor(ctx context.Context, configID string, cursorAt int64, incrementalAt int64) error {
	if strings.TrimSpace(configID) == "" {
		return nil
	}
	_, err := r.db.write.ExecContext(ctx,
		`INSERT INTO emby_sync_state(config_id,last_saved_cursor_at,last_incremental_at)
		 VALUES(?,?,?)
		 ON CONFLICT(config_id) DO UPDATE SET
		   last_saved_cursor_at=excluded.last_saved_cursor_at,
		   last_incremental_at=excluded.last_incremental_at,
		   updated_at=CURRENT_TIMESTAMP`,
		configID, cursorAt, incrementalAt)
	return wrapDB(err)
}

func (r *embyIndexRepo) TouchSyncTime(ctx context.Context, configID string, syncTime int64) error {
	if strings.TrimSpace(configID) == "" {
		return nil
	}
	_, err := r.db.write.ExecContext(ctx,
		`INSERT INTO emby_sync_state(config_id,last_sync_time) VALUES(?,?)
		 ON CONFLICT(config_id) DO UPDATE SET last_sync_time=excluded.last_sync_time, updated_at=CURRENT_TIMESTAMP`,
		configID, syncTime)
	return wrapDB(err)
}

func toAnySlice(values []string) []any {
	out := make([]any, 0, len(values))
	for _, v := range values {
		out = append(out, v)
	}
	return out
}

func isNoRows(err error) bool {
	return err == sql.ErrNoRows
}

// 保持 time 包引用（UpdatedAt 语义化时间字段）。
