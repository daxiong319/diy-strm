package casintake

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// 压缩包解压上限。CAS 清单本身只有几百字节，压缩包的合理体积来自
// 「一次打包多个影片的清单」，因此比单个 .cas 的 10MB 上限宽松得多，
// 但仍必须封顶：TG 上任何人都能给 bot 发文件，无上限等于给了对方一个
// 撑爆内存/磁盘的入口。
const (
	// maxArchiveFileBytes 单个压缩包的下载上限。
	maxArchiveFileBytes = 256 << 20
	// maxArchiveUnpackedBytes 解压后所有条目累计上限（zip bomb 防护）。
	maxArchiveUnpackedBytes = 64 << 20
	// maxArchiveEntryBytes 解压后单个条目上限。
	maxArchiveEntryBytes = 8 << 20
	// maxArchiveEntries 压缩包内条目数上限。
	maxArchiveEntries = 200
	// maxArchiveCasCount 单个压缩包里最多处理多少个 .cas。
	maxArchiveCasCount = 100
)

// casItem 从压缩包中取出的一个 .cas。
type casItem struct {
	// Name 清单文件名（用作通知与保存时的显示名）。
	Name string
	// Content CAS 清单文本。
	Content string
}

// archiveExtensions 支持的压缩包后缀（小写）。
//
// 只列出标准库能处理的格式：zip 用 archive/zip，tar/tar.gz/tgz 用
// archive/tar + compress/gzip。.7z/.rar 需要第三方库，按「不新增依赖」
// 的约定不支持，但要在错误里说清楚，避免用户以为是程序坏了。
var archiveExtensions = []string{".zip", ".tar", ".tar.gz", ".tgz", ".gz"}

// isArchiveFileName 判断是否为受支持的压缩包。
func isArchiveFileName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	// .tar.gz 比 .gz 长，先匹配长后缀，避免 ".tar.gz" 被 ".gz" 抢先判定后
	// 仍走 gzip 分支（结果一样，但语义上要按 tar 处理才能取出多个条目）。
	for _, ext := range archiveExtensions {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// isUnsupportedArchiveName 判断是否为「看起来是压缩包但本程序不支持」的格式。
// 用于给出可执行的提示，而不是静默丢弃。
func isUnsupportedArchiveName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, ext := range []string{".7z", ".rar", ".tar.bz2", ".tbz2", ".tar.xz", ".txz", ".bz2", ".xz", ".zipx"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// isCasIntakeName 判断是否为 CAS 接收需要处理的对象：.cas 清单或压缩包。
func isCasIntakeName(name string) bool {
	return isCasFileName(name) || isArchiveFileName(name) || isUnsupportedArchiveName(name)
}

// extractCasFromArchive 解压压缩包并取出其中所有 .cas 清单。
//
// data 是压缩包的原始字节。返回值按压缩包内的顺序排列；压缩包里没有
// .cas 时返回明确错误，由调用方提示用户。
//
// 安全约束（TG 上任何人都能给 bot 发文件）：
//   - 全程内存解压，不落盘，因此不存在路径穿越写文件的问题；含 ".."
//     的条目仍然拒绝，避免后续用条目名拼路径时留下隐患。
//   - 累计解压字节数、单条目字节数、条目数、.cas 个数全部封顶。
//   - 不递归解压压缩包里的压缩包：嵌套是 zip bomb 的经典形态，遇到时
//     明确提示用户单独发送内层文件。
func extractCasFromArchive(name string, data []byte) ([]casItem, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("压缩包内容为空")
	}
	lower := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractCasFromZip(data)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return extractCasFromTarGz(data)
	case strings.HasSuffix(lower, ".tar"):
		return extractCasFromTar(bytes.NewReader(data))
	case strings.HasSuffix(lower, ".gz"):
		return extractCasFromGz(data)
	default:
		return nil, fmt.Errorf("不支持的压缩包格式：%s", filepath.Base(name))
	}
}

// extractCasFromZip 解压 zip 并取出 .cas 条目。
func extractCasFromZip(data []byte) ([]casItem, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		// 加密 zip 的目录区能读出来但条目无法解密，zip.NewReader 本身不报错；
		// 这里报错说明连目录都读不了，多半是文件损坏或根本不是 zip。
		return nil, fmt.Errorf("读取 zip 失败：%w", err)
	}
	if len(reader.File) > maxArchiveEntries {
		return nil, fmt.Errorf("压缩包内条目过多（%d 个，上限 %d）", len(reader.File), maxArchiveEntries)
	}

	items := make([]casItem, 0, 4)
	var unpacked int64
	for _, f := range reader.File {
		entryName := strings.TrimSpace(f.Name)
		if entryName == "" || strings.HasSuffix(entryName, "/") {
			continue // 目录条目
		}
		if err := validateArchiveEntryName(entryName); err != nil {
			return nil, err
		}
		if isArchiveFileName(entryName) {
			// 嵌套压缩包：不解，避免 zip bomb。
			continue
		}
		if !isCasFileName(entryName) {
			continue
		}
		if f.UncompressedSize64 > maxArchiveEntryBytes {
			return nil, fmt.Errorf("压缩包内条目 %s 过大（%d 字节，上限 %d）",
				filepath.Base(entryName), f.UncompressedSize64, int64(maxArchiveEntryBytes))
		}
		content, n, err := readZipEntryLimited(f, maxArchiveEntryBytes)
		if err != nil {
			return nil, fmt.Errorf("解压条目 %s 失败：%w", filepath.Base(entryName), err)
		}
		unpacked += n
		if unpacked > maxArchiveUnpackedBytes {
			return nil, fmt.Errorf("压缩包解压后体积超过上限（%d 字节）", int64(maxArchiveUnpackedBytes))
		}
		items = append(items, casItem{Name: filepath.Base(entryName), Content: content})
		if len(items) > maxArchiveCasCount {
			return nil, fmt.Errorf("压缩包内 .cas 过多（上限 %d 个）", maxArchiveCasCount)
		}
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("压缩包里没有找到 .cas 文件")
	}
	return items, nil
}

// readZipEntryLimited 读取单个 zip 条目，最多 maxBytes 字节。
// 用 LimitReader 而非只信 UncompressedSize64：头部声明的大小可能被伪造。
func readZipEntryLimited(f *zip.File, maxBytes int64) (string, int64, error) {
	rc, err := f.Open()
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = rc.Close() }()
	buf, err := io.ReadAll(io.LimitReader(rc, maxBytes+1))
	if err != nil {
		return "", 0, err
	}
	if int64(len(buf)) > maxBytes {
		return "", 0, fmt.Errorf("条目解压后超过 %d 字节", maxBytes)
	}
	return string(buf), int64(len(buf)), nil
}

// extractCasFromTarGz 解压 .tar.gz / .tgz。
func extractCasFromTarGz(data []byte) ([]casItem, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("读取 gzip 失败：%w", err)
	}
	defer func() { _ = gz.Close() }()
	return extractCasFromTar(gz)
}

// extractCasFromTar 从 tar 流中取出 .cas 条目。
func extractCasFromTar(r io.Reader) ([]casItem, error) {
	tr := tar.NewReader(r)
	items := make([]casItem, 0, 4)
	var unpacked int64
	entries := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("读取 tar 失败：%w", err)
		}
		entries++
		if entries > maxArchiveEntries {
			return nil, fmt.Errorf("压缩包内条目过多（上限 %d）", maxArchiveEntries)
		}
		entryName := strings.TrimSpace(hdr.Name)
		if entryName == "" || hdr.Typeflag == tar.TypeDir {
			continue
		}
		if err := validateArchiveEntryName(entryName); err != nil {
			return nil, err
		}
		if isArchiveFileName(entryName) || !isCasFileName(entryName) {
			continue
		}
		if hdr.Size > maxArchiveEntryBytes {
			return nil, fmt.Errorf("压缩包内条目 %s 过大（%d 字节，上限 %d）",
				filepath.Base(entryName), hdr.Size, int64(maxArchiveEntryBytes))
		}
		buf, err := io.ReadAll(io.LimitReader(tr, maxArchiveEntryBytes+1))
		if err != nil {
			return nil, fmt.Errorf("解压条目 %s 失败：%w", filepath.Base(entryName), err)
		}
		if int64(len(buf)) > maxArchiveEntryBytes {
			return nil, fmt.Errorf("压缩包内条目 %s 超过 %d 字节", filepath.Base(entryName), int64(maxArchiveEntryBytes))
		}
		unpacked += int64(len(buf))
		if unpacked > maxArchiveUnpackedBytes {
			return nil, fmt.Errorf("压缩包解压后体积超过上限（%d 字节）", int64(maxArchiveUnpackedBytes))
		}
		items = append(items, casItem{Name: filepath.Base(entryName), Content: string(buf)})
		if len(items) > maxArchiveCasCount {
			return nil, fmt.Errorf("压缩包内 .cas 过多（上限 %d 个）", maxArchiveCasCount)
		}
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("压缩包里没有找到 .cas 文件")
	}
	return items, nil
}

// extractCasFromGz 解压单个 .gz（非 tar.gz）。
// 文件名取去掉 .gz 后的名字，其内容可能是 .cas 也可能是普通文件。
func extractCasFromGz(data []byte) ([]casItem, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("读取 gzip 失败：%w", err)
	}
	defer func() { _ = gz.Close() }()
	buf, err := io.ReadAll(io.LimitReader(gz, maxArchiveEntryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("解压 gzip 失败：%w", err)
	}
	if int64(len(buf)) > maxArchiveEntryBytes {
		return nil, fmt.Errorf("解压后超过 %d 字节", int64(maxArchiveEntryBytes))
	}
	// 单个 .gz 没有内部文件名，只有把内容本身当清单才有意义。
	// 这里不强求内容以 .cas 命名，交给后续 ParseManifestV2 判定。
	return []casItem{{Name: "解压的.cas", Content: string(buf)}}, nil
}

// validateArchiveEntryName 拒绝含上级目录的条目名。
// 本程序在内存里解压、不落盘，但条目名会参与日志与显示，
// 提前拒绝可以避免后续有人改成落盘实现时留下路径穿越。
func validateArchiveEntryName(name string) error {
	cleaned := filepath.ToSlash(name)
	if strings.Contains(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return fmt.Errorf("压缩包内条目路径非法：%s", filepath.Base(name))
	}
	return nil
}
