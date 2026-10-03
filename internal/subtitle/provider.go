package subtitle

import (
	"context"
	"path/filepath"
	"strings"
)

// SubtitleFormat 是字幕文件格式。
type SubtitleFormat string

const (
	FormatSRT SubtitleFormat = "srt"
	FormatASS SubtitleFormat = "ass"
	FormatSSA SubtitleFormat = "ssa"
	FormatSUB SubtitleFormat = "sub"
	FormatSUP SubtitleFormat = "sup"
	FormatVTT SubtitleFormat = "vtt"
)

// SearchRequest 是一次字幕检索的输入。
type SearchRequest struct {
	Title         string
	OriginalTitle string
	Year          int
	Season        int
	Episode       int
	MediaType     string
	TmdbId        int64
	ImdbId        string
	Languages     []string

	VideoFileName string
	VideoPath     string
	VideoSize     int64
	VideoHash     string
}

// Candidate 是一个候选字幕条目。
type Candidate struct {
	Provider      string            `json:"provider"`
	Slug          string            `json:"slug"`
	Title         string            `json:"title"`
	Language      string            `json:"language"`
	Format        SubtitleFormat    `json:"format"`
	HashMatched   bool              `json:"hash_matched"`
	ReleaseGroup  string            `json:"release_group"`
	DownloadURL   string            `json:"download_url"`
	PageURL       string            `json:"page_url"`
	Publisher     string            `json:"publisher"`
	Rating        float64           `json:"rating"`
	DownloadCount int64             `json:"download_count"`
	FileName      string            `json:"file_name"`
	Extra         map[string]string `json:"extra,omitempty"`
}

// Provider 是字幕来源的统一接口。
type Provider interface {
	Name() string
	DisplayName() string
	Search(ctx context.Context, req SearchRequest) ([]Candidate, error)
	Download(ctx context.Context, candidate Candidate, destPath string) error
	HealthCheck(ctx context.Context) error
	RequiresCredential() bool
}

// NormalizeLanguage 把各站点五花八门的语言写法收敛成内部规范值。
//
// 裸 "zh" 不折叠成 "zh-cn"：站点标 zh 往往是"中文但未声明简繁"，折叠会丢信息，
// 而打分时 zh 与 zh-cn/zh-tw 之间本来就互相认。
func NormalizeLanguage(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.ReplaceAll(s, "_", "-")
	if s == "" {
		return ""
	}
	compact := strings.ReplaceAll(s, " ", "")

	switch {
	case strings.Contains(compact, "chinese") && (strings.Contains(compact, "trad") || strings.Contains(compact, "繁")):
		return "zh-tw"
	case strings.Contains(compact, "chinese") && (strings.Contains(compact, "simp") || strings.Contains(compact, "简")):
		return "zh-cn"
	case compact == "chinese" || compact == "zho" || compact == "chi":
		return "zh"
	}

	switch {
	case strings.Contains(compact, "繁"), compact == "zh-tw", compact == "zh-hk",
		compact == "zht", compact == "cht":
		return "zh-tw"
	case compact == "zh":
		return "zh"
	case strings.Contains(compact, "简"), compact == "zh-cn",
		compact == "zhs", compact == "chs":
		return "zh-cn"
	case compact == "en", compact == "eng", compact == "english":
		return "en"
	case compact == "ja", compact == "jpn", compact == "japanese", compact == "日语":
		return "ja"
	case compact == "ko", compact == "kor", compact == "korean", compact == "韩语":
		return "ko"
	}

	// 其余取主标签（zh-cn-x 之类丢掉修饰）。
	for _, sep := range []string{"-", "(", " "} {
		if i := strings.Index(compact, sep); i > 0 {
			return compact[:i]
		}
	}
	return compact
}

// DetectFormat 从文件名/格式串识别字幕格式。
func DetectFormat(name string) SubtitleFormat {
	s := strings.ToLower(strings.TrimSpace(name))
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	switch {
	case strings.HasSuffix(s, ".srt"):
		return FormatSRT
	case strings.HasSuffix(s, ".ass"):
		return FormatASS
	case strings.HasSuffix(s, ".ssa"):
		return FormatSSA
	case strings.HasSuffix(s, ".sub"):
		return FormatSUB
	case strings.HasSuffix(s, ".sup"):
		return FormatSUP
	case strings.HasSuffix(s, ".vtt"):
		return FormatVTT
	}
	// 兼容只给裸格式名的场景（部分站点的 subtype 字段）。
	if ext := filepath.Ext(s); ext == "" {
		switch s {
		case "srt":
			return FormatSRT
		case "ass":
			return FormatASS
		case "ssa":
			return FormatSSA
		case "sub":
			return FormatSUB
		case "sup":
			return FormatSUP
		case "vtt":
			return FormatVTT
		}
	}
	return ""
}
