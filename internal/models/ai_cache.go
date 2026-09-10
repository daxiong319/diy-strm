package models

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"diy-strm/internal/db"
)

// AIParseCache AI 识别结果缓存（借鉴 tgto123 ai_media_parser exact 缓存）：
// 相同「目录名+文件名」输入不再重复调用 AI（省钱省时），命中后仍走 TMDB 校验。
type AIParseCache struct {
	BaseModel
	InputHash  string    `gorm:"uniqueIndex;size:64" json:"input_hash"` // sha256(目录名 + 文件名)
	InputText  string    `gorm:"size:512" json:"input_text"`
	ResultName string    `json:"result_name"`
	ResultYear int       `json:"result_year"`
	ResultJSON string    `gorm:"type:text" json:"result_json"` // 完整识别 JSON（含季集/质量维度，旧记录为空）
	CreatedAt  time.Time `json:"created_at"`
}

func (AIParseCache) TableName() string { return "ai_parse_cache" }

// AICacheKey 生成缓存键
func AICacheKey(hintName, fileName string) string {
	h := sha256.Sum256([]byte(hintName + "\x00" + fileName))
	return hex.EncodeToString(h[:])
}

// GetAIParseCache 按缓存键读取识别结果；未命中返回 false
func GetAIParseCache(inputHash string) (name string, year int, ok bool) {
	var row AIParseCache
	if err := db.Db.Where("input_hash = ?", inputHash).First(&row).Error; err != nil {
		return "", 0, false
	}
	return row.ResultName, row.ResultYear, true
}

// GetAIParseCacheFull 按缓存键读取完整缓存行（含识别 JSON）；未命中返回 nil
func GetAIParseCacheFull(inputHash string) *AIParseCache {
	var row AIParseCache
	if err := db.Db.Where("input_hash = ?", inputHash).First(&row).Error; err != nil {
		return nil
	}
	return &row
}

// SaveAIParseCache 写入/更新缓存（同键覆盖；resultJSON 为识别完整 JSON，可空）
func SaveAIParseCache(inputHash, inputText, resultName string, resultYear int, resultJSON string) {
	row := AIParseCache{
		InputHash:  inputHash,
		InputText:  inputText,
		ResultName: resultName,
		ResultYear: resultYear,
		ResultJSON: resultJSON,
		CreatedAt:  time.Now(),
	}
	var existing AIParseCache
	if err := db.Db.Where("input_hash = ?", inputHash).First(&existing).Error; err == nil {
		row.ID = existing.ID
		row.CreatedAt = existing.CreatedAt
		_ = db.Db.Save(&row).Error
		return
	}
	_ = db.Db.Create(&row).Error
}

// DeleteAIParseCache 删除缓存条目（TMDB 校验失败时调用，避免坏缓存）
func DeleteAIParseCache(inputHash string) {
	_ = db.Db.Where("input_hash = ?", inputHash).Delete(&AIParseCache{}).Error
}

// MarshalAIParseResult 识别结果转 JSON（缓存存储用；失败返回空串）
func MarshalAIParseResult(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
