package backuprestore

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// strmPayloadPrefix 是 STRM 目录在载荷里的命名空间（T14）。
//
// 载荷里其余条目都是固定文件名（配置、数据库、密钥、收藏），只有 STRM 目录
// 是一棵任意深度的目录树，所以单独给它一个前缀 + 前缀内逐条白名单校验，
// 而不是把 allowedPayloadName 直接改成接受任意路径 —— 那会让任意绝对路径、
// `..`、符号链接重新变成可写的目标。
const strmPayloadPrefix = "strm/"

// maxStrmPayloadFiles 给「一个备份里塞了几十万个小文件」设一个刹车。
//
// 这不是安全边界（大小上限才是），STRM 目录通常只有几百到几千个 .strm 文件，
// 超出这个量级基本可以断定是把整个媒体目录误配成了 STRM 目录。
const maxStrmPayloadFiles = 4096

const payloadManifestName = "payload/manifest.json"

type archiveSource struct {
	Name string
	Path string
	Data []byte
}

func buildPayloadArchive(destination string, manifest payloadManifest, sources []archiveSource) error {
	files := make([]payloadFile, 0, len(sources))
	byName := make(map[string]archiveSource, len(sources))
	for _, source := range sources {
		if !allowedPayloadName(source.Name) || source.Name == payloadManifestName {
			return fmt.Errorf("备份载荷路径无效：%s", source.Name)
		}
		if _, exists := byName[source.Name]; exists {
			return fmt.Errorf("备份载荷路径重复：%s", source.Name)
		}
		entry, err := inspectArchiveSource(source)
		if err != nil {
			return err
		}
		files = append(files, entry)
		byName[source.Name] = source
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	manifest.Files = files
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return err
	}

	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = out.Close()
		if !ok {
			_ = os.Remove(destination)
		}
	}()
	zw := zip.NewWriter(out)
	if err := writeZipBytes(zw, payloadManifestName, manifestJSON); err != nil {
		return err
	}
	for _, entry := range files {
		if err := writeZipSource(zw, byName[entry.Name], entry.Size); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func inspectArchiveSource(source archiveSource) (payloadFile, error) {
	h := sha256.New()
	var size int64
	if source.Path != "" {
		file, err := os.Open(source.Path)
		if err != nil {
			return payloadFile{}, err
		}
		size, err = io.Copy(h, io.LimitReader(file, maxPlainSize+1))
		closeErr := file.Close()
		if err != nil {
			return payloadFile{}, err
		}
		if closeErr != nil {
			return payloadFile{}, closeErr
		}
	} else {
		size = int64(len(source.Data))
		_, _ = h.Write(source.Data)
	}
	if size > maxPlainSize {
		return payloadFile{}, fmt.Errorf("备份载荷文件过大：%s", source.Name)
	}
	return payloadFile{Name: source.Name, Size: size, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func zipHeader(name string) *zip.FileHeader {
	header := &zip.FileHeader{Name: name, Method: zip.Store}
	header.SetMode(0o600)
	header.Modified = time.Unix(0, 0).UTC()
	return header
}

func writeZipBytes(zw *zip.Writer, name string, data []byte) error {
	entry, err := zw.CreateHeader(zipHeader(name))
	if err != nil {
		return err
	}
	_, err = entry.Write(data)
	return err
}

func writeZipSource(zw *zip.Writer, source archiveSource, size int64) error {
	entry, err := zw.CreateHeader(zipHeader(source.Name))
	if err != nil {
		return err
	}
	if source.Path == "" {
		_, err := entry.Write(source.Data)
		return err
	}
	file, err := os.Open(source.Path)
	if err != nil {
		return err
	}
	_, copyErr := io.CopyN(entry, file, size)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func extractPayloadArchive(archivePath, destination string) (payloadManifest, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return payloadManifest{}, fmt.Errorf("解析备份载荷：%w", err)
	}
	defer reader.Close()
	if len(reader.File) == 0 || len(reader.File) > maxPayloadFiles() {
		return payloadManifest{}, fmt.Errorf("备份载荷文件数无效")
	}
	entries := make(map[string]*zip.File, len(reader.File))
	for _, file := range reader.File {
		if file.FileInfo().IsDir() || !allowedPayloadName(file.Name) || entries[file.Name] != nil {
			return payloadManifest{}, fmt.Errorf("备份载荷包含非法条目：%s", file.Name)
		}
		if file.UncompressedSize64 > uint64(maxPlainSize) || file.CompressedSize64 > uint64(maxPlainSize) {
			return payloadManifest{}, fmt.Errorf("备份载荷条目过大：%s", file.Name)
		}
		entries[file.Name] = file
	}
	manifestEntry := entries[payloadManifestName]
	if manifestEntry == nil || manifestEntry.UncompressedSize64 > 1024*1024 {
		return payloadManifest{}, fmt.Errorf("备份载荷清单缺失或过大")
	}
	manifestReader, err := manifestEntry.Open()
	if err != nil {
		return payloadManifest{}, err
	}
	var manifest payloadManifest
	dec := json.NewDecoder(io.LimitReader(manifestReader, 1024*1024))
	dec.DisallowUnknownFields()
	decodeErr := dec.Decode(&manifest)
	closeErr := manifestReader.Close()
	if decodeErr != nil {
		return payloadManifest{}, fmt.Errorf("备份载荷清单无效：%w", decodeErr)
	}
	if closeErr != nil {
		return payloadManifest{}, closeErr
	}
	if manifest.FormatVersion != FormatVersion || (manifest.Scope != ScopeSettings && manifest.Scope != ScopeFull) {
		return payloadManifest{}, fmt.Errorf("备份载荷清单版本或范围无效")
	}
	if len(manifest.Files) == 0 || len(manifest.Files) > maxPayloadFiles()-1 || len(entries) != len(manifest.Files)+1 {
		return payloadManifest{}, fmt.Errorf("备份载荷文件数不匹配")
	}
	seen := make(map[string]bool, len(manifest.Files))
	for _, expected := range manifest.Files {
		if seen[expected.Name] || !allowedPayloadName(expected.Name) || expected.Name == payloadManifestName {
			return payloadManifest{}, fmt.Errorf("备份载荷清单包含非法条目：%s", expected.Name)
		}
		seen[expected.Name] = true
		entry := entries[expected.Name]
		if entry == nil || int64(entry.UncompressedSize64) != expected.Size {
			return payloadManifest{}, fmt.Errorf("备份载荷文件缺失或大小不匹配：%s", expected.Name)
		}
		if err := extractAndVerifyZipEntry(entry, destination, expected); err != nil {
			return payloadManifest{}, err
		}
	}
	return manifest, nil
}

func extractAndVerifyZipEntry(entry *zip.File, destination string, expected payloadFile) error {
	source, err := entry.Open()
	if err != nil {
		return err
	}
	defer source.Close()
	target := filepath.Join(destination, filepath.FromSlash(expected.Name))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, h), io.LimitReader(source, expected.Size+1))
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != expected.Size || hex.EncodeToString(h.Sum(nil)) != expected.SHA256 {
		return fmt.Errorf("备份载荷校验失败：%s", expected.Name)
	}
	return nil
}

func allowedPayloadName(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(name))
	if clean != name || clean == "." || strings.HasPrefix(clean, "../") {
		return false
	}
	// STRM 目录打成的是一个 zip 条目，名字本身不是 .strm，
	// 必须在进 strm/ 前缀分支之前单独放行，否则会被当成单个 .strm 挡掉。
	if name == strmArchiveEntry {
		return true
	}
	if strings.HasPrefix(name, strmPayloadPrefix) {
		return allowedStrmPayloadName(name)
	}
	switch name {
	case payloadManifestName,
		"settings/configs.json",
		"database/litepan.db",
		"data/secret.key",
		"data/litepan_favorites.json":
		return true
	default:
		return false
	}
}

// allowedStrmPayloadName 校验 STRM 命名空间里的条目。
//
// 三条限制，每条都对应一类真实事故：
//   - 后缀必须是 .strm：STRM 目录里只有指针文件，任何其它后缀都说明这个目录
//     配错了（多半是把媒体目录填成了 STRM 目录），顺手全量备份下去会让
//     一个几百 GB 的媒体库变成一个几百 GB 的备份。
//   - 段内不许出现 `.`/`..`/空段（filepath.Clean 之后仍等于原名即已挡掉）。
//   - 路径长度有上限：备份文件会原样落到网盘/容器上，超长名在部分目标上会被
//     静默截断成同名文件。
func allowedStrmPayloadName(name string) bool {
	rest := strings.TrimPrefix(name, strmPayloadPrefix)
	if rest == "" || !strings.HasSuffix(rest, ".strm") {
		return false
	}
	if len(rest) > 240 {
		return false
	}
	for _, part := range strings.Split(rest, "/") {
		switch part {
		case "", ".", "..":
			return false
		}
		if strings.ContainsAny(part, "\x00\r\n") {
			return false
		}
	}
	return true
}

// maxPayloadFiles 是载荷条目总数上限。
//
// 固定条目只有 5 个（清单 + 配置 + 数据库 + 密钥 + 收藏），剩下的额度全部
// 留给 STRM 目录；T14 之前这里硬编码 17，是为了给固定条目留冗余。
func maxPayloadFiles() int {
	return 5 + 1 + maxStrmPayloadFiles
}
