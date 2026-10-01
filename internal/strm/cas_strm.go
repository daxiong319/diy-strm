package strm

import (
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// casPlayStrmPrefix 是 CAS 播放直链前缀。首行以此开头的 .strm 由 CAS 链路（源文件已删、
// 播放时秒传恢复）维护，扫描器不得把它当作"云端已不存在"的过期文件清理。
const casPlayStrmPrefix = "/cas/play/"

// casPlayStrmReadLimit 读取 .strm 首行时最多读取的字节数（前缀在最前面，无需读全文）。
const casPlayStrmReadLimit = 2048

// CASStrmContent 返回 CAS 播放直链内容，与 internal/api/cas.go 的 casPlayURL 保持一致。
func CASStrmContent(recordID uint, fileName string) string {
	return casPlayStrmPrefix + strconv.FormatUint(uint64(recordID), 10) + "/" + url.PathEscape(fileName)
}

// RewriteCASStrmReferences 把 STRM 根目录下所有指向 sourceFileID 的 .strm 首行改写成 CAS 播放直链。
// 返回 matched（命中改写的文件数）与 updated（实际写盘的文件数）。matched 为 0 表示尚无对应 STRM。
func RewriteCASStrmReferences(strmDir, sourceFileID string, recordID uint, fileName string) (matched, updated int, err error) {
	if strings.TrimSpace(sourceFileID) == "" {
		return 0, 0, nil
	}
	content := CASStrmContent(recordID, fileName)
	_, matched, updated, err = rewriteStrmFiles(strmDir, func(line string) (string, bool) {
		if !strmContentReferencesFile(line, sourceFileID) {
			return "", false
		}
		return content, true
	})
	return matched, updated, err
}

// isCasPlayStrmContent 判断 .strm 首行是否为 CAS 播放直链。
func isCasPlayStrmContent(content string) bool {
	first := strings.TrimSpace(content)
	if idx := strings.IndexByte(first, '\n'); idx >= 0 {
		first = strings.TrimSpace(first[:idx])
	}
	return strings.HasPrefix(first, casPlayStrmPrefix)
}

// isCasPlayStrmFile 读取 .strm 首行判断是否为 CAS 播放直链（读失败按非 CAS 处理，交回原清理逻辑）。
func isCasPlayStrmFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, casPlayStrmReadLimit)
	n, rerr := f.Read(buf)
	if rerr != nil && n <= 0 {
		return false
	}
	return isCasPlayStrmContent(string(buf[:n]))
}

// dirContainsCasPlayStrm 判断目录（含子目录）内是否存在 CAS 播放 .strm。
func dirContainsCasPlayStrm(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".strm") {
			return nil
		}
		if isCasPlayStrmFile(path) {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// pruneMissingRemoteDir 清理"远端已消失目录"内的非 CAS 内容：普通过期 .strm 及其旁路元数据删除，
// CAS 播放 .strm 及仍存放它们的目录保留，避免打断"播放 → 秒传恢复 → 302"链路。
// 返回删除的 .strm 数。
func pruneMissingRemoteDir(path string) (int64, error) {
	var removed int64
	werr := filepath.WalkDir(path, func(p string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".strm") {
			return nil
		}
		if isCasPlayStrmFile(p) {
			return nil
		}
		if rerr := removeStaleStrmAndSameStemSidecars(p); rerr != nil {
			return rerr
		}
		removed++
		return nil
	})
	if werr != nil {
		return removed, werr
	}
	if err := removeEmptyDirs(path); err != nil {
		return removed, err
	}
	// 目录内仅剩 CAS 内容时删除会失败（目录非空），属预期保留。
	_ = os.Remove(path)
	return removed, nil
}
