package backuprestore

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// STRM 目录打包（T14）。
//
// 备份对象从「只有 SQLite 快照」扩到「config + STRM 目录」：STRM 目录是
// 用户花时间刮削出来的指针文件，丢了只能重新刮一遍，而它在磁盘上的体量
// 通常只有几百 KB —— 值得放进备份，不值得单独设计一套格式。
//
// 这里刻意**打包成一个 zip 条目**而不是把每个 .strm 拆成载荷里的一个条目：
// STRM 目录是活的（用户随时在加新剧），逐文件记账会让 manifest 随剧集数
// 线性膨胀；而包内路径仍逐条走 allowedStrmPayloadName 的同一套校验，
// 解包时重新落盘并保持相对路径（.strm 之间的相对层级对某些播放器有意义）。

const (
	// strmArchiveEntry 是 STRM 目录包在载荷里的条目名。
	strmArchiveEntry = strmPayloadPrefix + "dir.zip"
	// maxStrmArchiveUncompressed 是 STRM 包解开后的体积上限。
	// 体量按「几万个 .strm」估算，1 GiB 已经远超真实使用量，
	// 超出的多半是拿媒体目录冒充了 STRM 目录。
	maxStrmArchiveUncompressed = int64(1 << 30)
	// maxStrmArchiveEntries 是 STRM 包里的条目数上限。
	maxStrmArchiveEntries = maxStrmPayloadFiles * 4
)

// buildSTRMArchive 把 STRM 目录打成一个 zip，返回 zip 字节。
//
// 目录不存在返回 ok=false 而不是错误：绝大多数部署根本没开 STRM 生成，
// 让「没这个目录」变成一次备份失败毫无道理。
func buildSTRMArchive(root string) (data []byte, count int, ok bool, err error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, 0, false, nil
	}
	info, statErr := os.Stat(root)
	if statErr != nil || !info.IsDir() {
		if statErr != nil && !os.IsNotExist(statErr) {
			return nil, 0, false, fmt.Errorf("读取 STRM 目录：%w", statErr)
		}
		return nil, 0, false, nil
	}

	relPaths, walkErr := collectSTRMFiles(root)
	if walkErr != nil {
		return nil, 0, false, walkErr
	}
	if len(relPaths) == 0 {
		return nil, 0, false, nil
	}

	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(writeSTRMZip(pw, root, relPaths))
	}()
	limited := io.LimitReader(pr, maxStrmArchiveUncompressed+1)
	data, err = io.ReadAll(limited)
	if err != nil {
		return nil, 0, false, fmt.Errorf("打包 STRM 目录：%w", err)
	}
	if int64(len(data)) > maxStrmArchiveUncompressed {
		return nil, 0, false, fmt.Errorf("STRM 目录过大，已超过备份上限")
	}
	if int64(len(data)) > maxPlainSize {
		return nil, 0, false, fmt.Errorf("STRM 目录过大，已超过备份上限")
	}
	return data, len(relPaths), true, nil
}

// collectSTRMFiles 收集 STRM 目录下的 .strm 文件（相对路径，已排序）。
//
// 只收 .strm：STRM 目录里出现别的东西时，全量备份会让备份体积失控，
// 而用户对这些文件没有任何「备份」预期。符号链接一律跳过 —— 跟着链接
// 走出去等于把任意目录打包进来。
func collectSTRMFiles(root string) ([]string, error) {
	var out []string
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			// 单个条目读不到就跳过，不因为一个坏文件废掉整次备份。
			return nil //nolint:nilerr // 只跳过当前条目
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil //nolint:nilerr // 同上，跳过这一条
		}
		name := strmPayloadPrefix + filepath.ToSlash(rel)
		if !allowedStrmPayloadName(name) {
			return nil
		}
		out = append(out, name)
		if len(out) > maxStrmArchiveEntries {
			return errTooManySTRMEntries
		}
		return nil
	})
	if walkErr == errTooManySTRMEntries {
		return nil, fmt.Errorf("STRM 目录条目过多，已超过备份上限")
	}
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Strings(out)
	return out, nil
}

var errTooManySTRMEntries = fmt.Errorf("too many strm entries")

// writeSTRMZip 写 zip 内容。
func writeSTRMZip(w io.Writer, root string, relPaths []string) error {
	zw := zip.NewWriter(w)
	for _, name := range relPaths {
		if err := writeSTRMZipEntry(zw, filepath.Join(root, strings.TrimPrefix(name, strmPayloadPrefix)), name); err != nil {
			return err
		}
	}
	return zw.Close()
}

func writeSTRMZipEntry(zw *zip.Writer, source, name string) error {
	in, err := os.Open(source)
	if err != nil {
		// 文件在收集之后被删掉/改名：跳过，不让整次备份失败。
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer in.Close()
	entry, err := zw.CreateHeader(zipHeader(name))
	if err != nil {
		return err
	}
	_, copyErr := io.CopyN(entry, in, maxPlainSize+1)
	if copyErr != nil && copyErr != io.EOF {
		return copyErr
	}
	return nil
}

// strmFilesFromArchive 解开 STRM 包，返回相对路径 → 内容。
//
// 复用外层那套校验思路：先验每条路径，再按 expected size 截断读取，
// 写盘时用 MkdirAll + O_EXCL，避免包里的路径逃出目标目录。
func strmFilesFromArchive(data []byte) (map[string][]byte, error) {
	if int64(len(data)) > maxStrmArchiveUncompressed {
		return nil, fmt.Errorf("STRM 目录包过大")
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("解析 STRM 目录包：%w", err)
	}
	if len(reader.File) > maxStrmArchiveEntries {
		return nil, fmt.Errorf("STRM 目录包条目过多")
	}
	out := make(map[string][]byte, len(reader.File))
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		// zip 里的条目名已经带了 strm/ 前缀（写进去时加的），不要再加一次。
		name := filepath.ToSlash(file.Name)
		if !allowedStrmPayloadName(name) {
			return nil, fmt.Errorf("STRM 目录包包含非法条目：%s", file.Name)
		}
		if file.UncompressedSize64 > uint64(maxStrmArchiveUncompressed) {
			return nil, fmt.Errorf("STRM 目录包条目过大：%s", file.Name)
		}
		rc, openErr := file.Open()
		if openErr != nil {
			return nil, openErr
		}
		body, readErr := io.ReadAll(io.LimitReader(rc, maxStrmArchiveUncompressed+1))
		closeErr := rc.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if int64(len(body)) > maxStrmArchiveUncompressed {
			return nil, fmt.Errorf("STRM 目录包条目过大：%s", file.Name)
		}
		out[strings.TrimPrefix(name, strmPayloadPrefix)] = body
	}
	return out, nil
}

// strmSourceCount 报告 STRM 目录里有多少个文件会被备份（用于列表展示）。
func strmSourceCount(root string) int {
	paths, err := collectSTRMFiles(strings.TrimSpace(root))
	if err != nil {
		return 0
	}
	return len(paths)
}

// restoreSTRMFiles 把 STRM 内容写到目标目录。
//
// 整体替换而不是合并：合并会让「备份里没有的剧集」永远留在磁盘上，
// 而用户以为恢复之后目录就等于备份时的样子了。写之前先把旧目录挪到
// 一旁的临时位置，全部写成功才删；中途失败则把旧目录挪回来。
func restoreSTRMFiles(destination string, files map[string][]byte) error {
	if len(files) == 0 {
		// 备份里没有 STRM 目录：不动现有目录。删除一个用户正在用的目录，
		// 而备份里恰好没有这一项，代价远大于收益。
		return nil
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	retired := ""
	if _, err := os.Stat(destination); err == nil {
		retired = filepath.Join(parent, ".strm-restore-old-"+filepath.Base(destination))
		_ = os.RemoveAll(retired)
		if err := os.Rename(destination, retired); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	ok := false
	defer func() {
		if ok {
			if retired != "" {
				_ = os.RemoveAll(retired)
			}
			return
		}
		_ = os.RemoveAll(destination)
		if retired != "" {
			_ = os.Rename(retired, destination)
		}
	}()
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	for name, body := range files {
		if !allowedStrmPayloadName(strmPayloadPrefix + name) {
			return fmt.Errorf("STRM 文件名无效：%s", name)
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(target, body, 0o600); err != nil {
			return err
		}
	}
	ok = true
	return nil
}

// strmArchiveContext 只是为了让签名读起来像个普通的上下文用法。
