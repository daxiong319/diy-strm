package mediaorganize

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ScrapeNFOService 是 NFO + 海报落盘的真实实现（T14）。
//
// 落盘分两段：先写本地暂存，再按目标搬到最终位置。
//   - 目标是本地目录：直接写。
//   - 目标是网盘：网盘驱动只接受「本地文件 → 网盘」，所以必须先落本地
//     再上传 —— 这也是整个模块里唯一必须写两遍的理由。
//
// 海报下载失败只记警告，不让整个整理任务失败：整理的结果是文件到位，
// 海报是锦上添花，为一张 404 失败的 404 把文件留在原地更糟。
type ScrapeNFOService struct {
	// Images 下载 TMDB 图片；为 nil 时只写 NFO。
	Images ImageDownloader
	// Uploader 把本地文件送到网盘；AccountID > 0 时必填。
	Uploader NFOSink
	// Log 记警告。
	Log Logger
	// Staging 是上传用的本地暂存目录。
	Staging string
}

// ImageDownloader 下载 TMDB 图片。
//
// 只声明一个方法：整理流程只需要「给我一张图」，而它背后是
// tmdb.Client 的镜像站配置，不该由调用方决定用哪个。
type ImageDownloader interface {
	DownloadImage(ctx context.Context, posterPath, size string) ([]byte, error)
}

// NFOSink 把本地文件搬到最终位置（网盘或本地）。
type NFOSink interface {
	// WriteText 把文本内容写到 name（不存在的文件）。
	WriteText(ctx context.Context, accountID int64, parentID, name string, body []byte) error
	// WriteBinary 把二进制内容写到 name（不存在的文件）。
	WriteBinary(ctx context.Context, accountID int64, parentID, name string, body []byte) error
	// Exists 报告同名文件是否已存在（已存在时应当跳过而不是覆盖）。
	Exists(ctx context.Context, accountID int64, parentID, name string) (bool, error)
}

// Logger 是本模块要用的最小日志能力。
type Logger interface {
	Warn(msg string, args ...any)
}

type nopLogger struct{}

func (nopLogger) Warn(string, ...any) {}

// WriteWorkMetadata 落作品级 NFO 与海报。
func (s *ScrapeNFOService) WriteWorkMetadata(ctx context.Context, in ScrapeWorkInput) error {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return errors.New("标题为空，不写 NFO")
	}
	mediaType := normalizeMediaType(in.MediaType)
	year := in.Year
	base := fileNFOBase(title, mediaType, year)
	if mediaType == MediaTypeTV {
		body, err := marshalNFO(tvshowNFO{
			Title:         title,
			OriginalTitle: strings.TrimSpace(in.OriginalTitle),
			SortTitle:     sortTitle(title),
			Year:          year,
			Plot:          strings.TrimSpace(in.Plot),
			TMDBID:        in.TMDBID,
			ID:            tmdbIDString(in.TMDBID),
			Premiered:     formatPremiered(year),
		})
		if err != nil {
			return err
		}
		if err := s.write(ctx, in.AccountID, in.ParentID, in.Dir, "tvshow.nfo", body); err != nil {
			return err
		}
		// 剧集海报有 fanart.jpg / poster.jpg 两种社区约定，两个都写最保险，
		// 但只在有源图时才写，避免落一堆 0 字节文件。
		return s.writeImages(ctx, in.AccountID, in.ParentID, in.Dir, map[string]string{
			"poster.jpg": in.PosterURL,
			"fanart.jpg": in.FanartURL,
		})
	}
	body, err := marshalNFO(movieNFO{
		Title:         title,
		OriginalTitle: strings.TrimSpace(in.OriginalTitle),
		SortTitle:     sortTitle(title),
		Year:          year,
		Plot:          strings.TrimSpace(in.Plot),
		TMDBID:        in.TMDBID,
		ID:            tmdbIDString(in.TMDBID),
	})
	if err != nil {
		return err
	}
	if err := s.write(ctx, in.AccountID, in.ParentID, in.Dir, base+".nfo", body); err != nil {
		return err
	}
	return s.writeImages(ctx, in.AccountID, in.ParentID, in.Dir, map[string]string{
		base + "-poster.jpg": in.PosterURL,
		"poster.jpg":         in.PosterURL,
	})
}

// WriteSeasonMetadata 落季级 NFO。
func (s *ScrapeNFOService) WriteSeasonMetadata(ctx context.Context, in ScrapeSeasonInput) error {
	if in.Season <= 0 {
		return fmt.Errorf("季号无效：%d", in.Season)
	}
	season := strconv.Itoa(in.Season)
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = "Season " + season
	}
	body, err := marshalNFO(seasonNFO{
		Title:     title,
		Season:    season,
		Plot:      strings.TrimSpace(in.Plot),
		Premiered: strings.TrimSpace(in.Premiered),
	})
	if err != nil {
		return err
	}
	return s.write(ctx, in.AccountID, in.ParentID, in.Dir, "season"+season+".nfo", body)
}

// WriteEpisodeMetadata 落集级 NFO。
//
// Emby 读单集目录里的 <集名>.nfo；平铺存放（单文件一集）时也用同一套文件名，
// 因为两种布局下 Emby 都是「从视频文件名推导 NFO 名」。
func (s *ScrapeNFOService) WriteEpisodeMetadata(ctx context.Context, in ScrapeEpisodeInput) error {
	if in.Season <= 0 || in.Episode <= 0 {
		return fmt.Errorf("集号无效：S%02dE%02d", in.Season, in.Episode)
	}
	base := episodeBaseName(in.Title, in.Season, in.Episode)
	body, err := marshalNFO(episodeNFO{
		Title:     strings.TrimSpace(in.Title),
		Season:    strconv.Itoa(in.Season),
		Episode:   strconv.Itoa(in.Episode),
		Plot:      strings.TrimSpace(in.Plot),
		Aired:     strings.TrimSpace(in.Aired),
		TMDBID:    tmdbIDString(in.TMDBID),
		ShowTitle: strings.TrimSpace(in.ShowTitle),
	})
	if err != nil {
		return err
	}
	return s.write(ctx, in.AccountID, in.ParentID, in.Dir, base+".nfo", body)
}

// episodeBaseName 生成集级 NFO 基名。
//
// 有标题时用「SxxEyy 标题」这种社区通行命名，和整理出来的视频名保持一致；
// 没有标题时退回 SxxEyy —— 宁可名字朴素，也不要因为缺标题就不写 NFO。
func episodeBaseName(title string, season, episode int) string {
	code := fmt.Sprintf("S%02dE%02d", season, episode)
	clean := sanitizeNFOFileName(title)
	if clean == "" {
		return code
	}
	return code + " " + clean
}

// write 写一个文本文件到最终位置。
//
// 目标账号 > 0 才走网盘；账号为 0 就是本地目录。
// 注意 Dir 在网盘路径下不参与定位（网盘只认 ID），但仍然要求非空 ——
// 解析不出目录说明整理动作的落点不可信，宁可报错也不往未知位置写。
func (s *ScrapeNFOService) write(ctx context.Context, accountID int64, parentID, dir, name string, body []byte) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("目标目录为空")
	}
	if s.cloud(accountID) {
		return s.Uploader.WriteText(ctx, accountID, parentID, name, body)
	}
	return s.writeLocal(dir, name, body)
}

// cloud 判断这次落盘是否走网盘。
func (s *ScrapeNFOService) cloud(accountID int64) bool {
	return accountID > 0 && s.Uploader != nil
}

// writeLocal 写到本地目录（整理目标就是本地时用）。
func (s *ScrapeNFOService) writeLocal(dir, name string, body []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), body, 0o644)
}

// writeImages 下载并写海报。
//
// 已有同名文件就跳过：整理是反复跑的流程，每次重下几百 KB 的图
// 既慢又会让用户在网盘里看到一堆重复版本。
func (s *ScrapeNFOService) writeImages(ctx context.Context, accountID int64, parentID, dir string, sources map[string]string) error {
	if s.Images == nil {
		return nil
	}
	for name, source := range sources {
		source = strings.TrimSpace(source)
		if source == "" {
			continue
		}
		if s.cloud(accountID) {
			exists, err := s.Uploader.Exists(ctx, accountID, parentID, name)
			if err == nil && exists {
				continue
			}
		} else if fileExists(filepath.Join(dir, name)) {
			continue
		}
		data, err := s.Images.DownloadImage(ctx, source, nfoPosterWidth)
		if err != nil || len(data) == 0 {
			// 降级为警告：整理已经成功了，不能因为一张图失败就回滚。
			s.logger().Warn("刮削元数据落盘时图片下载失败，已跳过", "file", name, "error", err)
			continue
		}
		if s.cloud(accountID) {
			err = s.Uploader.WriteBinary(ctx, accountID, parentID, name, data)
		} else {
			err = s.writeLocal(dir, name, data)
		}
		if err != nil {
			s.logger().Warn("刮削元数据落盘时图片写入失败，已跳过", "file", name, "error", err)
			continue
		}
	}
	return nil
}

func (s *ScrapeNFOService) logger() Logger {
	if s.Log == nil {
		return nopLogger{}
	}
	return s.Log
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// normalizeMediaType 把动作元数据里的 media_type 归一。
//
// 空值默认按电影处理：电影是更常见的形态，猜错的后果是给电影目录里
// 放一个 tvshow.nfo（Emby 忽略它），而反过来给剧集放 movie.nfo
// 会让整季的元数据都读不到。
func normalizeMediaType(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case MediaTypeTV, "tvshow", "series", "tv_series", "剧集", "电视剧":
		return MediaTypeTV
	default:
		return MediaTypeMovie
	}
}

// sortTitle 生成排序用的名字（去掉年份与季集后缀）。
func sortTitle(title string) string {
	clean := sanitizeNFOFileName(title)
	lower := strings.ToLower(clean)
	for _, prefix := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(clean[len(prefix):])
		}
	}
	return clean
}

// ---- TMDB 详情 → 落盘输入 ----

// tmdbWorkDetail 是从 TMDB 详情里取出的落盘所需字段。
type tmdbWorkDetail struct {
	Title         string
	OriginalTitle string
	Year          int
	Plot          string
	PosterURL     string
	FanartURL     string
}

// ParseTMDBWorkDetail 从 TMDB 详情 JSON 里抽出落盘所需字段。
//
// 抽出来单独放，是为了不让「解析 TMDB 响应」和「写文件」揉在一起：
// 前者是纯函数，可以直接用真实响应样本测；后者要碰网盘。
func ParseTMDBWorkDetail(raw json.RawMessage) (tmdbWorkDetail, error) {
	var payload struct {
		Title         string `json:"title"`
		Name          string `json:"name"`
		OriginalTitle string `json:"original_title"`
		OriginalName  string `json:"original_name"`
		Overview      string `json:"overview"`
		PosterPath    string `json:"poster_path"`
		BackdropPath  string `json:"backdrop_path"`
		ReleaseDate   string `json:"release_date"`
		FirstAirDate  string `json:"first_air_date"`
	}
	if len(raw) == 0 {
		return tmdbWorkDetail{}, errors.New("TMDB 详情为空")
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return tmdbWorkDetail{}, fmt.Errorf("解析 TMDB 详情：%w", err)
	}
	title := strings.TrimSpace(payload.Title)
	if title == "" {
		title = strings.TrimSpace(payload.Name)
	}
	original := strings.TrimSpace(payload.OriginalTitle)
	if original == "" {
		original = strings.TrimSpace(payload.OriginalName)
	}
	date := strings.TrimSpace(payload.ReleaseDate)
	if date == "" {
		date = strings.TrimSpace(payload.FirstAirDate)
	}
	return tmdbWorkDetail{
		Title:         title,
		OriginalTitle: original,
		Year:          yearFromDate(date),
		Plot:          strings.TrimSpace(payload.Overview),
		PosterURL:     strings.TrimSpace(payload.PosterPath),
		FanartURL:     strings.TrimSpace(payload.BackdropPath),
	}, nil
}

// yearFromDate 从 YYYY-MM-DD 里取年份。
func yearFromDate(date string) int {
	if len(date) < 4 {
		return 0
	}
	year, err := strconv.Atoi(date[:4])
	if err != nil || year <= 0 || year > 9999 {
		return 0
	}
	return year
}

// WorkDetailLookup 查询一个作品的 TMDB 详情（原始 JSON）。
//
// 只声明一个方法：落盘侧要的是「这个 id 的详情长什么样」，
// 至于背后走不走镜像站、要不要重试，那是客户端自己的事。
type WorkDetailLookup interface {
	Lookup(ctx context.Context, tmdbID, mediaType string) (json.RawMessage, error)
}
