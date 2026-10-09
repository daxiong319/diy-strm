package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"litepan/internal/driver"
	"litepan/internal/file"
	"litepan/internal/mediaorganize"
	"litepan/internal/mediaorganize/tmdb"
)

// 刮削落盘的装配（T14）。
//
// 这一层只做三件事：把 TMDB 客户端、包上传下载器、以及两者的组合
// 塞给 mediaorganize。所有「该不该写」的判断都在 mediaorganize 里，
// 这里一行判断都不做 —— 否则同一个开关就要在两处各解释一次。

// scrapeNFOSink 把落盘器接到网盘驱动上。
type scrapeNFOSink struct {
	files *file.Service
	// tmpDir 是本地暂存目录：网盘驱动只接受「本地文件 → 网盘」，
	// 所以写网盘前必须先落本地。
	tmpDir string

	mu     sync.Mutex
	dirIDs map[string]string
}

func (a *scrapeNFOSink) WriteText(ctx context.Context, accountID int64, parentID, name string, body []byte) error {
	return a.WriteBinary(ctx, accountID, parentID, name, body)
}

func (a *scrapeNFOSink) WriteBinary(ctx context.Context, accountID int64, parentID, name string, body []byte) error {
	if a.files == nil {
		return fmt.Errorf("网盘落盘未装配")
	}
	local, err := a.stage(name, body)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(local) }()
	_, err = a.files.UploadLocal(ctx, accountID, driver.LocalUploadRequest{
		LocalPath:      local,
		FileName:       name,
		ParentID:       parentID,
		ConflictPolicy: "overwrite",
	})
	return err
}

// Exists 检查目标目录里是否已有同名文件。
//
// 只在能列出目录时才敢回答 true：驱动报错时返回「不存在」而不是错误，
// 因为多写一个 NFO 的代价远小于因为一个探测失败就不写元数据。
func (a *scrapeNFOSink) Exists(ctx context.Context, accountID int64, parentID, name string) (bool, error) {
	if a.files == nil {
		return false, nil
	}
	items, err := a.files.List(ctx, accountID, parentID, false)
	if err != nil {
		return false, nil
	}
	for _, item := range items {
		if item.Name == name {
			return true, nil
		}
	}
	return false, nil
}

func (a *scrapeNFOSink) stage(name string, body []byte) (string, error) {
	if err := os.MkdirAll(a.tmpDir, 0o755); err != nil {
		return "", err
	}
	local := filepath.Join(a.tmpDir, sanitizeStageName(name))
	if err := os.WriteFile(local, body, 0o644); err != nil {
		return "", err
	}
	return local, nil
}

// sanitizeStageName 让暂存文件名不会逃出暂存目录。
func sanitizeStageName(name string) string {
	clean := strings.ReplaceAll(strings.TrimSpace(name), "\\", "_")
	clean = strings.ReplaceAll(clean, "/", "_")
	clean = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, clean)
	clean = strings.Trim(clean, ". ")
	if clean == "" {
		return "meta.bin"
	}
	if len(clean) > 180 {
		clean = clean[len(clean)-180:]
	}
	return clean
}

// tmdbImageSource 适配 TMDB 图片下载（自动走配置的图片反代）。
type tmdbImageSource struct {
	client *tmdb.Client
}

func (a tmdbImageSource) DownloadImage(ctx context.Context, posterPath, size string) ([]byte, error) {
	if a.client == nil {
		return nil, fmt.Errorf("TMDB 客户端未装配")
	}
	return a.client.DownloadImage(ctx, posterPath, size)
}

// tmdbWorkSource 适配作品详情查询。
type tmdbWorkSource struct {
	client *tmdb.Client
}

func (a tmdbWorkSource) Lookup(ctx context.Context, tmdbID, mediaType string) (json.RawMessage, error) {
	if a.client == nil {
		return nil, fmt.Errorf("TMDB 客户端未装配")
	}
	kind := strings.TrimSpace(mediaType)
	if !strings.EqualFold(kind, mediaorganize.MediaTypeTV) {
		kind = mediaorganize.MediaTypeMovie
	}
	return a.client.Lookup(ctx, tmdbID, kind)
}

// wireScrapeNFO 构造刮削落盘器。
//
// 注意：即使 mo_scrape_nfo_enabled=false 也会装配好客户端 —— 配置可能
// 在进程运行期间被打开，而 clients 的构造没有副作用。
func wireScrapeNFO(
	files *file.Service,
	settingsFor func() tmdb.Options,
	dataDir string,
) *mediaorganize.ScrapeNFOService {
	client := tmdb.NewClient(settingsFor())
	svc := &mediaorganize.ScrapeNFOService{
		Staging: filepath.Join(dataDir, "nfo-staging"),
	}
	svc.Images = tmdbImageSource{client: client}
	svc.Uploader = &scrapeNFOSink{
		files:  files,
		tmpDir: svc.Staging,
		dirIDs: map[string]string{},
	}
	return svc
}
