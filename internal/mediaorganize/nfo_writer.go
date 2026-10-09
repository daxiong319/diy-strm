package mediaorganize

import (
	"context"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

// 刮削落盘（T14）：把 TMDB 的刮削结果写成 NFO + 海报。
//
// 格式说明：这里写的是 Emby/Kodi/Jellyfin 共同接受的通用 NFO schema，
// 不是从哪个项目逆向出来的私有结构 —— 通用格式的好处是别人拿现成的
// NFO 编辑器也能改，坏处是字段随版本漂移，所以只写各版本都认的核心字段，
// 少写一个的代价仅仅是少一列信息，而不是「这个文件格式不对」。

const (
	// MediaTypeTV 与 MediaTypeMovie 是动作元数据里 media_type 的取值。
	MediaTypeTV    = "tv"
	MediaTypeMovie = "movie"

	// nfoPosterWidth 是海报下载的宽度档位（TMDB 的 w500 足够清晰，
	// 再大只是让备份体积翻倍）。
	nfoPosterWidth = "w500"
)

// ScrapeNFOWriter 是 NFO + 海报的落盘能力（T14）。
//
// 用接口注入而不是直接在整理流程里写文件：整理成功与否不应该被
// 「海报 URL 404」改变，而这层实现必然要碰网络。
type ScrapeNFOWriter interface {
	// WriteWorkMetadata 为一部作品（电影或剧集根目录）落 NFO 与海报。
	WriteWorkMetadata(ctx context.Context, in ScrapeWorkInput) error
	// WriteSeasonMetadata 为一季落季级 NFO。
	WriteSeasonMetadata(ctx context.Context, in ScrapeSeasonInput) error
	// WriteEpisodeMetadata 为一集落集级 NFO。
	WriteEpisodeMetadata(ctx context.Context, in ScrapeEpisodeInput) error
}

// ScrapeWorkInput 是作品级元数据落盘的输入。
type ScrapeWorkInput struct {
	// Target 是作品所在目录的绝对路径（本地）或网盘上的引用路径。
	Dir string
	// AccountID > 0 时 Dir 视为本地暂存路径，最终内容上传到该网盘账号的
	// ParentID 下；AccountID == 0 时 Dir 就是最终位置。
	AccountID     int64
	ParentID      string
	Title         string
	OriginalTitle string
	Year          int
	MediaType     string
	TMDBID        int64
	Plot          string
	PosterURL     string
	FanartURL     string
}

// ScrapeSeasonInput 是季级元数据。
type ScrapeSeasonInput struct {
	AccountID int64
	ParentID  string
	Dir       string
	Title     string
	Season    int
	Plot      string
	Premiered string
}

// ScrapeEpisodeInput 是集级元数据。
type ScrapeEpisodeInput struct {
	AccountID int64
	ParentID  string
	Dir       string
	Title     string
	Season    int
	Episode   int
	Plot      string
	Aired     string
	TMDBID    int64
	ShowTitle string
}

// ---- NFO 结构 ----

type movieNFO struct {
	XMLName       xml.Name `xml:"movie"`
	Title         string   `xml:"title"`
	OriginalTitle string   `xml:"originaltitle,omitempty"`
	SortTitle     string   `xml:"sorttitle,omitempty"`
	Year          int      `xml:"year,omitempty"`
	Plot          string   `xml:"plot,omitempty"`
	TMDBID        int64    `xml:"tmdbid,omitempty"`
	ID            string   `xml:"id,omitempty"`
	Genre         []string `xml:"genre,omitempty"`
}

type tvshowNFO struct {
	XMLName       xml.Name `xml:"tvshow"`
	Title         string   `xml:"title"`
	OriginalTitle string   `xml:"originaltitle,omitempty"`
	SortTitle     string   `xml:"sorttitle,omitempty"`
	Year          int      `xml:"year,omitempty"`
	Plot          string   `xml:"plot,omitempty"`
	TMDBID        int64    `xml:"tmdbid,omitempty"`
	ID            string   `xml:"id,omitempty"`
	Premiered     string   `xml:"premiered,omitempty"`
	Genre         []string `xml:"genre,omitempty"`
}

type seasonNFO struct {
	XMLName   xml.Name `xml:"season"`
	Title     string   `xml:"title,omitempty"`
	Season    string   `xml:"seasonnumber"`
	Plot      string   `xml:"plot,omitempty"`
	Premiered string   `xml:"premiered,omitempty"`
}

type episodeNFO struct {
	XMLName   xml.Name `xml:"episodedetails"`
	Title     string   `xml:"title"`
	Season    string   `xml:"season"`
	Episode   string   `xml:"episode"`
	Plot      string   `xml:"plot,omitempty"`
	Aired     string   `xml:"aired,omitempty"`
	TMDBID    string   `xml:"tmdbid,omitempty"`
	ShowTitle string   `xml:"showtitle,omitempty"`
}

// marshalNFO 生成 NFO 文本。
//
// 手写 XML 头而不是靠 xml.Header：后者在部分 Go 版本上不保证第一行就是
// 声明，而部分媒体服务器对文件开头的空白很敏感。
func marshalNFO(value any) ([]byte, error) {
	body, err := xml.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(body, '\n')...), nil
}

// fileNFOBase 返回作品根目录里 NFO 的基名（不带扩展名）。
//
// 剧集和电影同名不同扩展名：Emby 对剧集读 tvshow.nfo，对电影读 <filename>.nfo。
func fileNFOBase(title string, mediaType string, year int) string {
	clean := sanitizeNFOFileName(title)
	if clean == "" {
		clean = "media"
	}
	// 剧集：Emby 读剧集目录下的 tvshow.nfo；
	// 电影：Emby 读与视频同名的 <片名>.nfo（年份后缀是社区惯例，去掉也能认，
	// 但带上更不容易和同名的另一部撞车）。
	if mediaType == MediaTypeTV {
		return "tvshow"
	}
	if year > 0 {
		return fmt.Sprintf("%s (%d)", clean, year)
	}
	return clean
}

// sanitizeNFOFileName 清掉文件名里的非法字符。
func sanitizeNFOFileName(name string) string {
	name = strings.TrimSpace(name)
	replacer := strings.NewReplacer("/", "", "\\", "", ":", "", "*", "", "?", "", "\"", "", "<", "", ">", "", "|", "", "\n", "", "\r", "")
	out := strings.TrimSpace(replacer.Replace(name))
	for strings.Contains(out, "  ") {
		out = strings.ReplaceAll(out, "  ", " ")
	}
	return strings.Trim(out, ". ")
}

// tmdbIDString 返回集级 NFO 用的 tmdbid（这里放剧集的 TMDB ID）。
func tmdbIDString(id int64) string {
	if id <= 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

// formatPremiered 把年份格式化成 premiered 字段（YYYY-MM-DD）。
func formatPremiered(year int) string {
	if year <= 0 {
		return ""
	}
	if year > 9999 {
		return ""
	}
	return fmt.Sprintf("%04d-01-01", year)
}

// formatAired 把集号+年份格式化成 aired 字段。
func formatAired(year, season, episode int) string {
	if year <= 0 || season <= 0 || episode <= 0 {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", year, season, episode)
}
