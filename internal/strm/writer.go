package strm

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// 解析文件名里的季集号，覆盖 AV 常见写法（S01E31 / S01.E31 / S01 第31集 / 第31集）与裸 s01e31 之外的中文集号。
var (
	seasonEpisodeRe  = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,2})[\s._-]*(?:e|ep)(\d{1,4})(?:[^a-z0-9]|$)`)
	chineseEpisodeRe = regexp.MustCompile(`第\s*(\d{1,4})\s*[集话話]`)
	chineseSeasonRe  = regexp.MustCompile(`第\s*(\d{1,2})\s*季`)
	seasonDirRe      = regexp.MustCompile(`(?i)^(season[\s._-]*\d{1,2}|s\d{1,2}|第\s*\d{1,3}\s*季|第\s*[一二三四五六七八九十]+\s*季)$`)
)

var strmSafeNameRepl = strings.NewReplacer(
	"/", "_",
	"\\", "_",
	":", "_",
	"*", "_",
	"?", "_",
	"\"", "_",
	"<", "_",
	">", "_",
	"|", "_",
)

func MediaStem(name string) string {
	ext := filepath.Ext(name)
	if ext == "" {
		return name
	}
	return strings.TrimSuffix(name, ext)
}

func SafeName(name string) string {
	return SafeStem(strings.TrimSpace(name))
}

// SafeStem 保留 STEM 前后空格，只替换非法字符，全空白/空串/点目录名兜底为 _。
func SafeStem(name string) string {
	if strings.TrimSpace(name) == "" {
		return "_"
	}
	name = strmSafeNameRepl.Replace(name)
	if name == "" || name == "." || name == ".." {
		return "_"
	}
	return name
}

// SafeDirSegments 把相对目录拆成安全目录段，容错前后斜杠与反斜杠并跳过 "." 和 ".."。
func SafeDirSegments(raw string) []string {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	raw = strings.Trim(raw, "/")
	if raw == "" {
		return nil
	}
	var out []string
	for _, seg := range strings.Split(raw, "/") {
		seg = strings.TrimSpace(seg)
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		out = append(out, SafeName(seg))
	}
	return out
}

// NormalizeGroupDir 归一化分组目录，返回存库用的相对路径（如 "电影/港台"，空为根目录）。
func NormalizeGroupDir(raw string) string {
	return strings.Join(SafeDirSegments(raw), "/")
}

// TaskRelDir 返回任务在 STRM 根下的相对目录（分组目录 + 输出文件夹）。
func TaskRelDir(groupDir, outputFolder string) string {
	segs := SafeDirSegments(groupDir)
	segs = append(segs, SafeName(outputFolder))
	return strings.Join(segs, "/")
}

func LocalRelPath(outputFolder string, relDirs []string, fileName string, isoFilenameEnabled bool) string {
	parts := SafeDirSegments(outputFolder)
	for _, dir := range relDirs {
		parts = append(parts, SafeName(dir))
	}
	parts = append(parts, LocalStrmFileName(fileName, isoFilenameEnabled))
	return filepath.Join(parts...)
}

func LegacyLocalRelPath(outputFolder string, relDirs []string, fileName string) string {
	return LocalRelPath(outputFolder, relDirs, fileName, false)
}

func LocalStrmFileName(fileName string, isoFilenameEnabled bool) string {
	if isoFilenameEnabled && isISOFileName(fileName) {
		return SafeName(fileName) + ".strm"
	}
	return SafeStem(MediaStem(fileName)) + ".strm"
}

func isISOFileName(fileName string) bool {
	return strings.EqualFold(filepath.Ext(strings.TrimSpace(fileName)), ".iso")
}

func MigrateLegacyISOStrmFile(root, outputFolder string, relDirs []string, fileName, fileID string, isoFilenameEnabled bool) (bool, error) {
	if !isoFilenameEnabled || !isISOFileName(fileName) {
		return false, nil
	}
	legacyRelPath := LegacyLocalRelPath(outputFolder, relDirs, fileName)
	currentRelPath := LocalRelPath(outputFolder, relDirs, fileName, true)
	if legacyRelPath == currentRelPath {
		return false, nil
	}
	legacyPath := filepath.Join(root, legacyRelPath)
	currentPath := filepath.Join(root, currentRelPath)
	if pathHasOversizedComponent(legacyPath) || pathHasOversizedComponent(currentPath) {
		return false, nil
	}
	content, err := os.ReadFile(legacyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !strmContentReferencesFile(string(content), fileID) {
		return false, nil
	}
	if _, err := os.Stat(currentPath); err == nil {
		return true, os.Remove(legacyPath)
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(currentPath), 0o755); err != nil {
		return false, err
	}
	if err := os.Rename(legacyPath, currentPath); err != nil {
		return false, err
	}
	return true, nil
}

// MediaInfo 记录一条媒体文件的落盘信息，供生成日志输出可排查的明细。
// SourcePath（源文件路径）与 Title（剧名/片名）在只有文件名的调用点可能为空，其余字段总是尽力填充。
type MediaInfo struct {
	SourcePath string
	Title      string
	Season     int
	Episode    int
	HasSeason  bool
	HasEpisode bool
}

// DescribeMediaInfo 从源文件完整路径、文件名与相对目录推导日志用的媒体信息。
// 标题优先取源路径中"季/集所在目录"的上一级（即剧名目录），其次退回文件名去扩展名后的剧名部分。
func DescribeMediaInfo(sourcePath, fileName string, relDirs []string) MediaInfo {
	name := strings.TrimSpace(fileName)
	if name == "" {
		name = filepath.Base(strings.TrimSpace(sourcePath))
	}
	info := MediaInfo{
		SourcePath: strings.TrimSpace(sourcePath),
		Title:      MediaStem(name),
	}
	season, episode, ok := parseEpisodeName(name)
	if ok {
		if season > 0 {
			info.Season, info.HasSeason = season, true
		}
		if episode > 0 {
			info.Episode, info.HasEpisode = episode, true
		}
	}
	if info.Title != "" {
		info.Title = strings.SplitN(info.Title, ".[", 2)[0]
	}
	dirs := relDirs
	if len(dirs) == 0 && info.SourcePath != "" {
		dirs = splitDirs(displayDirOf(info.SourcePath))
	}
	if title := seriesTitle(dirs, info.HasEpisode); title != "" {
		info.Title = title
	}
	if mediaTitleKey(info.Title) == "" {
		info.Title = ""
	}
	return info
}

// parseEpisodeName 从文件名解析季号与集号，返回是否识别为剧集。
// 优先 AV 风格的 SxxExx（含第N集），其次中文"第N集"，季号缺失时按 0 返回。
func parseEpisodeName(name string) (season, episode int, ok bool) {
	if m := seasonEpisodeRe.FindStringSubmatch(name); m != nil {
		season, _ = strconv.Atoi(m[1])
		episode, _ = strconv.Atoi(m[2])
		return season, episode, true
	}
	if m := chineseEpisodeRe.FindStringSubmatch(name); m != nil {
		episode, _ = strconv.Atoi(m[1])
		if sm := chineseSeasonRe.FindStringSubmatch(name); sm != nil {
			season, _ = strconv.Atoi(sm[1])
		}
		return season, episode, true
	}
	return 0, 0, false
}

// splitDirs 把斜杠/反斜杠分隔的相对目录串拆成非空目录段。
func splitDirs(raw string) []string {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	raw = strings.Trim(raw, "/")
	if raw == "" {
		return nil
	}
	var out []string
	for _, seg := range strings.Split(raw, "/") {
		if seg = strings.TrimSpace(seg); seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// displayDirOf 取路径的目录部分（保留原始分隔符）。
func displayDirOf(path string) string {
	if idx := strings.LastIndexAny(path, `/\`); idx >= 0 {
		return path[:idx]
	}
	return ""
}

// seriesTitle 从相对目录里找剧名目录：集号已知时取"季目录"的上一级，否则取最深层目录。
func seriesTitle(relDirs []string, hasEpisode bool) string {
	segs := relDirs
	if len(segs) == 0 {
		return ""
	}
	idx := len(segs) - 1
	if hasEpisode && idx > 0 && seasonDirRe.MatchString(strings.TrimSpace(segs[idx])) {
		idx--
	}
	return strings.TrimSpace(segs[idx])
}

// mediaTitleKey 判断标题是否是真正的媒体名而不是路径/占位符。
func mediaTitleKey(title string) string {
	title = strings.TrimSpace(title)
	if title == "" || title == "." || title == ".." || title == "/" || title == "\\" {
		return ""
	}
	return title
}

func strmContentReferencesFile(content, fileID string) bool {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return false
	}
	return strings.Contains(content, "/"+EncodeFileKey(fileID)+"/t/")
}

func TaskOutputDir(strmDir, outputFolder string) string {
	segs := SafeDirSegments(outputFolder)
	if len(segs) == 0 {
		return ""
	}
	root := strings.TrimSpace(strmDir)
	if root == "" {
		root = "strm"
	}
	return filepath.Join(append([]string{root}, segs...)...)
}

func DeleteTaskOutput(strmDir, outputFolder string) error {
	dir := TaskOutputDir(strmDir, outputFolder)
	if dir == "" {
		return nil
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(dir)
}

// removeStrmScrapeIndex 删除 STRM 刮削海报墙索引，路径与 strmscrape.TaskIndexPath 一致。
func removeStrmScrapeIndex(dataDir string, taskID int64) {
	base := filepath.Join(strings.TrimSpace(dataDir), "strmscrape", strconv.FormatInt(taskID, 10)+".sqlite")
	for _, p := range []string{base, base + "-wal", base + "-shm"} {
		_ = os.Remove(p)
	}
}

func WriteStrmFile(rootDir, relPath, content string) error {
	full := filepath.Join(rootDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content+"\n"), 0o644)
}
