package mediaupgrade

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gorm.io/gorm"

	"litepan/internal/moviepilot"
)

// libFile 是媒体库侧（或候选侧）的一个文件。
//
// 与 moviepilot.LocalFile 的区别是多了 ModTime 和 Slot：
// 洗版的快照复核要靠 ModTime 判断「库里的这一集还是不是当初那批文件」。
type libFile struct {
	// ignoreSize 让「体积这一步不参与判定」成为可能。
	//
	// 零值 false = 体积照常参与，所以真实枚举/扫描链路完全不受影响
	// （那里 Size 来自 stat，永远可信）。只有规则试算会置位：
	// 那里体积可能根本没填，而拿两个 0 去比大小（0 > 0 为假）
	// 会被判成 tie —— 用户看到「体积不占优」而他其实一次都没填过体积。
	ignoreSize bool

	Path    string
	Name    string
	Size    int64
	ModTime int64 // Unix 秒
	Quality *moviepilot.FileQuality
	Slot    Slot
}

// snapshot 转成快照条目。
func (f libFile) snapshot() fileSnapshot {
	return fileSnapshot{
		Path:    f.Path,
		Name:    f.Name,
		Size:    f.Size,
		ModTime: f.ModTime,
		SlotKey: f.Slot.Key(),
	}
}

// incompleteSuffix 未完成下载的临时后缀。
//
// 与 moviepilot.isSkipUploadFile 同口径 —— 洗版也绝不能把还在写入的 .part 当成现版，
// 否则会拿一个半截文件的体积去做「新版更大」的判定。
//
// 之所以在本包重写而不是复用：moviepilot 那边是未导出的私有函数。
// 两边各留一份是对的：organize 的上传口径与 wash 的比较口径本来就该独立演进。
var incompleteSuffix = []string{".!qb", ".part", ".partial", ".download", ".opdownload"}

// isIncompleteFile 判断是否为未完成下载的临时文件。
func isIncompleteFile(name string) bool {
	lower := strings.ToLower(name)
	for _, s := range incompleteSuffix {
		if strings.HasSuffix(lower, s) {
			return true
		}
	}
	return false
}

// collectLocalFiles 递归收集目录下的视频文件。
//
// 目录不存在返回空切片 + nil（与 moviepilot.CollectLocalFiles 同口径）：
// 待整理目录还没建出来是正常状态，不该报错。
func collectLocalFiles(root string) ([]libFile, error) {
	root = strings.TrimRight(strings.TrimSpace(root), "/")
	if root == "" {
		return nil, fmt.Errorf("目录为空")
	}
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []libFile
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			// 单个文件读不到不该让整次扫描失败，跳过即可。
			if os.IsNotExist(err) || os.IsPermission(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		name := filepath.Base(p)
		if isIncompleteFile(name) || !moviepilot.IsVideoFile(name) {
			return nil
		}
		q := moviepilot.ParseQualityFromName(name)
		out = append(out, libFile{
			Path:    p,
			Name:    name,
			Size:    info.Size(),
			ModTime: info.ModTime().Unix(),
			Quality: q,
			Slot:    SlotOf(name, q),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// embyIndexRow emby_media_items 里洗版关心的一行。
type embyIndexRow struct {
	Path       string
	Name       string
	SeriesName string
	Type       string
	Size       int64
	ModTime    int64
}

// embyIndexTypes 是会被当成媒体文件入库的类型。
//
// Series/Season/MusicVideo/BoxSet/Folder 这些是目录或容器型条目，
// 把它们当文件去比大小没有意义，直接排除。
var embyIndexTypes = map[string]bool{
	"Movie": true, "Episode": true, "Video": true,
}

// loadEmbyIndexFiles 从 emby_media_items 读媒体文件列表。
//
// emby 与 jellyfin 共用这张表：本仓只有一套 Emby 兼容索引
// （internal/store/migrations/0026_emby_index.sql），Jellyfin 的 API 与 Emby 同构，
// 索引过来的条目都落在这张表里。
func loadEmbyIndexFiles(db *gorm.DB) ([]libFile, error) {
	if db == nil {
		return nil, fmt.Errorf("缺少数据库句柄")
	}
	rows := make([]embyIndexRow, 0, 64)
	err := db.Raw(`
		SELECT path, name, series_name, type, date_modified_time
		FROM emby_media_items
		WHERE is_folder = 0 AND path <> ''
		ORDER BY path`).Scan(&rows).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	out := make([]libFile, 0, len(rows))
	for _, r := range rows {
		name := strings.TrimSpace(r.Name)
		if name == "" {
			name = filepath.Base(r.Path)
		}
		// 索引里的 Name 有时带路径前缀，取最后一段。
		if idx := strings.LastIndexAny(name, "/\\"); idx >= 0 && idx+1 < len(name) {
			name = name[idx+1:]
		}
		if !moviepilot.IsVideoFile(name) || isIncompleteFile(name) {
			continue
		}
		if r.Type != "" && !embyIndexTypes[r.Type] {
			continue
		}
		q := moviepilot.ParseQualityFromName(name)
		out = append(out, libFile{
			Path:    r.Path,
			Name:    name,
			ModTime: r.ModTime,
			Quality: q,
			Slot:    SlotOf(name, q),
		})
	}
	return out, nil
}

// statLibraryFile 在提交阶段复核一个路径当前是否可达，以及它的大小与修改时间。
//
// reachable=false 时**绝不能**按这个路径做任何删除动作：
// emby/jellyfin 源记的是网盘路径，本机文件系统上未必存在，
// 而「路径不存在」和「文件被移走」在按路径删除这件事上是同一种危险。
func statLibraryFile(path string) (size int64, modTime int64, reachable bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return 0, 0, false
	}
	return info.Size(), info.ModTime().Unix(), true
}

// embyTableExists 探测 emby_media_items 是否存在。
//
// 老库如果没有这张表（没启用过 Emby 索引），扫描不该报 SQL 错误，
// 应该明确告诉用户「Emby 源需要先启用 Emby 媒体索引」。
func embyTableExists(db *gorm.DB) bool {
	if db == nil {
		return false
	}
	var name string
	err := db.Raw(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='emby_media_items'`,
	).Scan(&name).Error
	if err != nil && err != gorm.ErrRecordNotFound && err != sql.ErrNoRows {
		return false
	}
	return name == "emby_media_items"
}

// listLibraryFiles 按扫描源枚举媒体库文件。
func listLibraryFiles(ctx context.Context, db *gorm.DB, rs *RuleSet) ([]libFile, error) {
	switch rs.Source {
	case SourceEmby, SourceJellyfin:
		if !embyTableExists(db) {
			return nil, fmt.Errorf("扫描源 %s 需要先启用 Emby 媒体索引（表 emby_media_items 不存在）", rs.Source)
		}
		return loadEmbyIndexFiles(db)
	default:
		return collectLocalFiles(rs.LibraryRoot)
	}
}

// listCandidateFiles 枚举候选文件。
//
// 候选 = 候选目录里的文件 ∪ 媒体库目录里的文件。
// 后者是刻意的：用户把一个更好的版本直接丢进已整理目录，它就该被认成新版，
// 否则「洗版」只能对还没整理的下载生效，实用性砍半。
func listCandidateFiles(ctx context.Context, db *gorm.DB, rs *RuleSet, library []libFile) ([]libFile, error) {
	seen := make(map[string]bool, len(library))
	out := make([]libFile, 0, len(library))
	for _, f := range library {
		seen[f.Path] = true
	}
	for _, root := range rs.CandidateRoots {
		files, err := collectLocalFiles(root)
		if err != nil {
			// 单个候选目录读不动不该让整次扫描失败，但要在返回里体现出来。
			continue
		}
		for _, f := range files {
			if seen[f.Path] {
				continue
			}
			seen[f.Path] = true
			out = append(out, f)
		}
	}
	out = append(out, library...)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
