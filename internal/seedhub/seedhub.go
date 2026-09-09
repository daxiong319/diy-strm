// Package seedhub SeedHub 资源源接入（对齐 tgto123 新版 seedhub_client 黑盒形状）：
// 网盘分享结果只读展示；磁力/ED2K 可复制/离线。
// 凭据（API 地址 + Token）本机加密存储于 discovery_settings。
package seedhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/discovery"
	"diy-strm/internal/helpers"

	"gorm.io/gorm"
)

const (
	settingAPIURL   = "seedhub_api_url"
	settingAPIToken = "seedhub_api_token"

	searchPath = "/api/v1/resources/search"
)

// Config SeedHub 接入配置
type Config struct {
	APIURL string
	Token  string
}

// GetConfig 读取配置（未配置 ok=false）
func GetConfig() (Config, bool) {
	cfg := Config{}
	for key, dst := range map[string]*string{
		settingAPIURL:   &cfg.APIURL,
		settingAPIToken: &cfg.Token,
	} {
		var row struct {
			Value string
		}
		if err := db.Db.Table("discovery_settings").Select("value").Where("`key` = ?", key).Take(&row).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				helpers.AppLogger.Warnf("SeedHub 读取 %s 失败：%v", key, err)
			}
			return Config{}, false
		}
		var v string
		if json.Unmarshal([]byte(row.Value), &v) == nil {
			*dst = strings.TrimRight(strings.TrimSpace(v), "/")
		}
	}
	// Token 存的是密文，解密
	if cfg.Token != "" {
		if plain, err := helpers.DecryptLocalSecret(cfg.Token); err == nil {
			cfg.Token = plain
		} else {
			return Config{}, false
		}
	}
	if cfg.APIURL == "" || cfg.Token == "" {
		return Config{}, false
	}
	return cfg, true
}

// SaveConfig 保存配置（Token 加密落盘）
func SaveConfig(apiURL, token string) error {
	enc, err := helpers.EncryptLocalSecret(strings.TrimSpace(token))
	if err != nil {
		return err
	}
	for key, value := range map[string]string{
		settingAPIURL:   strings.TrimRight(strings.TrimSpace(apiURL), "/"),
		settingAPIToken: enc,
	} {
		raw, _ := json.Marshal(value)
		setting := discovery.DiscoverySetting{Key: key, Value: string(raw), UpdatedAt: time.Now()}
		if err := db.Db.Save(&setting).Error; err != nil {
			return err
		}
	}
	return nil
}

// IsConfigured 是否已配置
func IsConfigured() bool {
	_, ok := GetConfig()
	return ok
}

// ResourceItem SeedHub 资源条目（归一化到 media_discovery 资源卡片语义）
type ResourceItem struct {
	ItemKey   string `json:"item_key"`
	Title     string `json:"title"`
	Slug      string `json:"slug,omitempty"`
	ShareURL  string `json:"share_url,omitempty"`
	LinkType  string `json:"link_type"` // magnet / ed2k / 115 / 123 / guangya 分享
	Size      string `json:"size,omitempty"`
	Remark    string `json:"remark,omitempty"`
	IsOffline bool   `json:"is_offline"`
}

// SearchResources 资源检索
func SearchResources(ctx context.Context, cfg Config, title, mediaType string, tmdbID int64) ([]ResourceItem, error) {
	payload := map[string]any{"title": title, "media_type": mediaType}
	if tmdbID > 0 {
		payload["tmdb_id"] = tmdbID
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.APIURL+searchPath, strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errors.New("SeedHub 令牌无效或已过期")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SeedHub HTTP %d：%s", resp.StatusCode, truncate(string(body), 120))
	}
	var out struct {
		Success bool `json:"success"`
		Data    struct {
			Items []struct {
				Key      string `json:"item_key"`
				Title    string `json:"title"`
				Slug     string `json:"slug"`
				ShareURL string `json:"share_url"`
				LinkType string `json:"link_type"`
				Size     string `json:"size"`
				Remark   string `json:"remark"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("SeedHub 响应解析失败：%s", truncate(string(body), 120))
	}
	items := make([]ResourceItem, 0, len(out.Data.Items))
	for _, r := range out.Data.Items {
		linkType := strings.ToLower(strings.TrimSpace(r.LinkType))
		offline := linkType == "magnet" || linkType == "ed2k"
		key := r.Key
		if key == "" {
			key = r.Slug
		}
		if key == "" {
			key = r.ShareURL
		}
		items = append(items, ResourceItem{
			ItemKey:   "seedhub:" + url.PathEscape(key),
			Title:     r.Title,
			Slug:      r.Slug,
			ShareURL:  r.ShareURL,
			LinkType:  r.LinkType,
			Size:      r.Size,
			Remark:    r.Remark,
			IsOffline: offline,
		})
	}
	return items, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
