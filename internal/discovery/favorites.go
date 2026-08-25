package discovery

import (
	"fmt"
	"strings"
	"time"

	"diy-strm/internal/db"

	"gorm.io/gorm"
)

// DiscoveryFavorite 收藏条目（entity_key 唯一，如 tmdb:movie:123 / douban:35465232 / bangumi:326808）
type DiscoveryFavorite struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	EntityKey string    `gorm:"unique;size:128;index" json:"entity_key"`
	Source    string    `gorm:"size:16;index" json:"source"`  // tmdb/douban/bangumi/anilist
	MediaType string    `gorm:"size:8" json:"media_type"`     // movie/tv
	ExternalID string   `gorm:"size:32;index" json:"external_id"`
	TMDBID    int64     `json:"tmdb_id"`
	Title     string    `gorm:"size:255" json:"title"`
	OriginalTitle string `gorm:"size:255" json:"original_title,omitempty"`
	Poster    string    `gorm:"size:512" json:"poster"`
	Overview  string    `gorm:"type:text" json:"overview,omitempty"`
	VoteAvg   float64   `json:"vote_avg"`
	Year      int       `json:"year"`
	CreatedAt time.Time `json:"created_at"`
}

func (DiscoveryFavorite) TableName() string { return "discovery_favorites" }

// ListFavorites 收藏列表（新→旧）
func ListFavorites() ([]DiscoveryFavorite, error) {
	var list []DiscoveryFavorite
	err := db.Db.Order("id desc").Find(&list).Error
	return list, err
}

// UpsertFavorite 添加收藏（按 entity_key 去重）
func UpsertFavorite(fav *DiscoveryFavorite) error {
	fav.EntityKey = normalizeEntityKey(fav.Source, fav.MediaType, fav.ExternalID)
	var exist DiscoveryFavorite
	if err := db.Db.Where("entity_key = ?", fav.EntityKey).First(&exist).Error; err == nil {
		fav.ID = exist.ID
		db.Db.Model(&exist).Updates(map[string]any{
			"title": fav.Title, "poster": fav.Poster, "vote_avg": fav.VoteAvg,
		})
		return nil
	}
	return db.Db.Create(fav).Error
}

// DeleteFavorite 删除收藏
func DeleteFavorite(id uint) error {
	return db.Db.Delete(&DiscoveryFavorite{}, id).Error
}

// IsFavorite 是否已收藏
func IsFavorite(entityKey string) (bool, error) {
	var count int64
	err := db.Db.Model(&DiscoveryFavorite{}).Where("entity_key = ?", entityKey).Count(&count).Error
	return count > 0, err
}

// FavoriteKeysByIDs 批量判断收藏状态（返回已收藏的 entity_key 集合）
func FavoriteKeysByIDs(keys []string) (map[string]bool, error) {
	result := make(map[string]bool, len(keys))
	if len(keys) == 0 {
		return result, nil
	}
	var rows []DiscoveryFavorite
	if err := db.Db.Where("entity_key IN ?", keys).Find(&rows).Error; err != nil {
		return result, err
	}
	for _, row := range rows {
		result[row.EntityKey] = true
	}
	return result, nil
}

// normalizeEntityKey 归一化实体键 source:type:id
func normalizeEntityKey(source, mediaType, externalID string) string {
	return strings.ToLower(strings.TrimSpace(source)) + ":" +
		strings.ToLower(strings.TrimSpace(mediaType)) + ":" + strings.TrimSpace(externalID)
}

// DiscoverySubjectCache 豆瓣/番剧目录条目缓存 + TMDB 匹配结果
// （对应 tgto123 media_douban_subjects / media_anime_subjects 的合并简化版）
type DiscoverySubjectCache struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CatalogKey string   `gorm:"size:128;index" json:"catalog_key"` // 目录键 douban:movie_hot_gaia / anime:bangumi:calendar
	Source    string    `gorm:"size:16;index" json:"source"`       // douban/bangumi/anilist
	ExternalID string   `gorm:"size:32;index" json:"external_id"`
	Title     string    `gorm:"size:255" json:"title"`
	OriginalTitle string `gorm:"size:255" json:"original_title,omitempty"`
	MediaType string    `gorm:"size:8" json:"media_type"`
	Poster    string    `gorm:"size:512" json:"poster"`
	Rating    float64   `json:"rating"`
	ReleaseDate string  `gorm:"size:32" json:"release_date,omitempty"`
	Sort      int       `json:"sort"` // 在目录中的顺序
	Payload   string    `gorm:"type:text" json:"-"` // 原始 JSON（详情弹窗用）
	// TMDB 匹配结果
	TMDBID      int64  `json:"tmdb_id"`
	TMDBTitle   string `gorm:"size:255" json:"tmdb_title,omitempty"`
	MatchScore  float64 `json:"match_score"`
	MatchedAt   *time.Time `json:"matched_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (DiscoverySubjectCache) TableName() string { return "discovery_subject_cache" }

// ReplaceCatalogItems 全量替换某目录的缓存条目
func ReplaceCatalogItems(catalogKey string, items []DiscoverySubjectCache) error {
	return db.Db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("catalog_key = ?", catalogKey).Delete(&DiscoverySubjectCache{}).Error; err != nil {
			return err
		}
		for i := range items {
			items[i].CatalogKey = catalogKey
			items[i].Sort = i
		}
		if len(items) == 0 {
			return nil
		}
		return tx.CreateInBatches(items, 100).Error
	})
}

// CatalogItems 读取目录缓存条目（按目录内排序）
func CatalogItems(catalogKey string) ([]DiscoverySubjectCache, error) {
	var items []DiscoverySubjectCache
	err := db.Db.Where("catalog_key = ?", catalogKey).Order("sort asc").Find(&items).Error
	return items, err
}

// SubjectByEntityKey 按 entity_key（source:type:id）查找目录缓存条目
// （同一番剧可能存在于多个目录，取最新一条）
func SubjectByEntityKey(entityKey string) (*DiscoverySubjectCache, error) {
	source, _, externalID := splitEntityKey(entityKey)
	if source == "" || externalID == "" {
		return nil, fmt.Errorf("非法 entity_key：%s", entityKey)
	}
	var item DiscoverySubjectCache
	err := db.Db.Where("source = ? AND external_id = ?", source, externalID).
		Order("id desc").First(&item).Error
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// UpdateSubjectCacheTMDB 写入 TMDB 匹配结果（更新该实体的所有缓存行）
func UpdateSubjectCacheTMDB(entityKey string, tmdbID int64, tmdbTitle string, score float64) error {
	source, _, externalID := splitEntityKey(entityKey)
	if source == "" || externalID == "" {
		return fmt.Errorf("非法 entity_key：%s", entityKey)
	}
	now := time.Now()
	return db.Db.Model(&DiscoverySubjectCache{}).
		Where("source = ? AND external_id = ?", source, externalID).
		Updates(map[string]any{
			"tmdb_id":     tmdbID,
			"tmdb_title":  tmdbTitle,
			"match_score": score,
			"matched_at":  &now,
		}).Error
}

// splitEntityKey 解析 entity_key 为 source/mediaType/externalID 三段
func splitEntityKey(entityKey string) (string, string, string) {
	parts := strings.SplitN(entityKey, ":", 3)
	switch len(parts) {
	case 3:
		return parts[0], parts[1], parts[2]
	case 2:
		return parts[0], "", parts[1]
	default:
		return "", "", ""
	}
}
