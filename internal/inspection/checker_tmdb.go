package inspection

import (
	"context"
	"fmt"
	"strings"
)

// TMDBChecker 巡检「TMDB 检查与修正」。
//
// 它只做两件事，因为只有这两件事能安全地自动做：
//
//  1. 编号失效：本地记着 tmdb_id，查 TMDB 返回 404。这条能确定地判定为失效
//     ——同一个 ID 在 TMDB 上不存在，不会是网络抖动造成的误判（网络错误不落 Finding）。
//     修复方式是「清掉这个失效编号」，把资源留给下一次人工确认或重新匹配。
//
//  2. 改名预览：本地文件名与 TMDB 上的标题/季集号对不上。改名是可逆的
//     （改回去就行），所以提供预览 + 执行，默认不自动执行。
//
// 它**不做**的是「按标题去 TMDB 搜一个最像的然后改 ID」。同名电影多到离谱
// （重映版、第一部/续作），自动改错编号比不改糟糕得多 —— 改错之后
// 刮削出来的是另一部电影，而用户要等到看片才发现。
type TMDBChecker struct {
	KeyName   string
	LabelText string
	// Items 是待检条目（本地侧）。
	Items func(ctx context.Context) ([]TMDBCheckItem, error)
	// Client 是 TMDB 查询端口。返回 ErrTMDBNotFound 表示编号失效，
	// 返回其它错误一律不产生 Finding（网络问题不该被当成编号问题）。
	Client TMDBLookup
}

// ErrTMDBNotFound 表示 TMDB 上查不到这个编号。
var ErrTMDBNotFound = fmt.Errorf("TMDB 编号失效")

// TMDBLookup 是巡检用到的 TMDB 查询能力。
type TMDBLookup interface {
	// Lookup 返回标题、年份与季集信息；编号失效时返回 ErrTMDBNotFound。
	Lookup(ctx context.Context, tmdbID int64, mediaType string) (TMDBMeta, error)
}

// TMDBMeta 是巡检需要的 TMDB 元数据子集。
type TMDBMeta struct {
	Title    string
	Year     int
	MediaType string
	Seasons  []TMDBSeasonMeta
}

// TMDBSeasonMeta 是一个季的元数据。
type TMDBSeasonMeta struct {
	SeasonNumber int
	EpisodeCount int
	EpisodeNames map[int]string
}

// TMDBCheckItem 是一个待检条目的本地侧信息。
type TMDBCheckItem struct {
	// Kind 区分「影片」与「剧集季」，决定用哪个 media_type 查 TMDB。
	Kind      string
	TMDBID    int64
	LocalName string
	LocalPath string
	// Season/Episode 在 Kind 为剧集季时有效。
	Season  int
	Episode int
	// RefID 是执行改名时要回填的位置（记录 ID）。
	RefID int64
}

func (c *TMDBChecker) Key() string   { return c.KeyName }
func (c *TMDBChecker) Label() string { return c.LabelText }

func (c *TMDBChecker) Scan(ctx context.Context) ([]Finding, error) {
	if c.Items == nil || c.Client == nil {
		return nil, nil
	}
	items, err := c.Items(ctx)
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, item := range items {
		meta, err := c.Client.Lookup(ctx, item.TMDBID, tmdbMediaType(item.Kind))
		if err != nil {
			// 只有「确定失效」才报。网络/限流错误一律放过 ——
			// 把一次超时当成编号失效去清编号，代价是一次错误的刮削修正。
			if err == ErrTMDBNotFound {
				out = append(out, c.invalidIDFinding(item))
			}
			continue
		}
		out = append(out, c.renameFinding(item, meta)...)
	}
	return out, nil
}

func (c *TMDBChecker) invalidIDFinding(item TMDBCheckItem) Finding {
	target := fmt.Sprintf("kind=%s tmdb_id=%d path=%s", item.Kind, item.TMDBID, item.LocalPath)
	return Finding{
		CheckerKey: c.Key(),
		Kind:       "tmdb_id_invalid",
		Target:     target,
		Detail: map[string]any{
			"tmdb_id": item.TMDBID,
			"name":    item.LocalName,
		},
		Repair: RepairAction{
			Kind:   RepairNone,
			Label:  "仅报告失效编号",
			Params: map[string]string{"ref_id": fmt.Sprintf("%d", item.RefID)},
			Preview: fmt.Sprintf("TMDB 上查不到编号 %d（%s）。修复方式是清掉这个失效编号，"+
				"让下一次整理重新匹配。系统不会自动换成别的编号。", item.TMDBID, item.LocalPath),
			Reversible: false,
		},
	}
}

// renameFinding 比对本地文件名与 TMDB 元数据，产出改名预览。
func (c *TMDBChecker) renameFinding(item TMDBCheckItem, meta TMDBMeta) []Finding {
	want := expectedName(item, meta)
	if want == "" || want == item.LocalName {
		return nil
	}
	return []Finding{{
		CheckerKey: c.Key(),
		Kind:       "tmdb_name_mismatch",
		Target:     fmt.Sprintf("kind=%s tmdb_id=%d path=%s", item.Kind, item.TMDBID, item.LocalPath),
		Detail: map[string]any{
			"tmdb_id":   item.TMDBID,
			"local":     item.LocalName,
			"expected":  want,
			"tmdb_ttl":  meta.Title,
		},
		Repair: RepairAction{
			Kind:   RepairRenameFile,
			Label:  fmt.Sprintf("改名为 %s", want),
			Params: map[string]string{"ref_id": fmt.Sprintf("%d", item.RefID), "new_name": want},
			Preview: fmt.Sprintf("把 %s 改名为 %s（TMDB 标题：%s）。改名可逆，"+
				"改回去只需要再改一次名字。", item.LocalName, want, meta.Title),
			Reversible: true,
		},
	}}
}

func tmdbMediaType(kind string) string {
	if strings.EqualFold(kind, "tv") || strings.EqualFold(kind, "series") {
		return "tv"
	}
	return "movie"
}

// expectedName 算出 TMDB 口径下应有的文件名。
//
// 这里刻意只给剧集季生成期望名：剧集的规范名依赖季集模板（用户可自定义），
// 系统猜错模板会毁掉一整季的文件名。影片的名字是确定的，值得给预览。
func expectedName(item TMDBCheckItem, meta TMDBMeta) string {
	if meta.MediaType != "movie" {
		return ""
	}
	ext := fileExt(item.LocalName)
	if meta.Title == "" {
		return ""
	}
	name := meta.Title
	if meta.Year > 0 {
		name = fmt.Sprintf("%s (%d)", name, meta.Year)
	}
	if ext == "" {
		return name
	}
	return name + ext
}

func fileExt(name string) string {
	idx := strings.LastIndex(name, ".")
	if idx < 0 || idx == len(name)-1 {
		return ""
	}
	return name[idx:]
}