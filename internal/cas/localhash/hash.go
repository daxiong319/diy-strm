// Package localhash 为本地磁盘文件计算 CAS 五哈希指纹（秒传清单）。
//
// 与网盘路径（internal/cas/litepan/adapter.go 从 FileItem.Hash 读取）互补：
// 本地文件没有任何云端元数据，必须真正读盘算一遍。
//
// 分片与 SliceMd5 规则逐字对齐 drivers/189Cloud/upload.go 的 uploadPartSize / calculateUploadHashes
// （该实现是驱动私有函数，此处按其算法重新实现，不直接调用）。
package localhash

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"os"
	"strings"

	"litepan/internal/cas/cloud189"
	"litepan/internal/domain"
)

// defaultUploadPartSize 对齐 drivers/189Cloud/transport.go:45 的 defaultUploadPartSize。
const defaultUploadPartSize int64 = 10 * 1024 * 1024

// partSizeFor 计算天翼云盘上传分片大小，逐字对齐 drivers/189Cloud/upload.go:412 的 uploadPartSize。
//
//	fileSize > default*2*999 → multiple = ceil(fileSize / (1999*default))，不足 5 取 5，返回 multiple*default
//	fileSize > default*999   → default*2
//	否则                     → default
func partSizeFor(fileSize int64) int64 {
	defaultSize := defaultUploadPartSize
	if fileSize > defaultSize*2*999 {
		multiple := (fileSize + 1999*defaultSize - 1) / (1999 * defaultSize)
		if multiple < 5 {
			multiple = 5
		}
		return multiple * defaultSize
	}
	if fileSize > defaultSize*999 {
		return defaultSize * 2
	}
	return defaultSize
}

// ComputeHashes 单遍顺序读取本地文件，恒定内存地算出 CAS 五哈希。
//
// 计算项：全量 MD5(FileMd5)、全量 SHA1、全量 SHA256、分片 MD5 → SliceMd5。
// 全部小写 hex。
//
// PreHash 留空：夸克实际秒传（drivers/Quark/upload.go:696 的 RapidUploadByHashes）
// 只消费 md5 + sha1 两个特征，preHash 在本代码库中没有任何构造实现，仅在
// internal/cas/cloud189/cas.go:33 作为存量字段透传，故本地无法也不应构造。
// Gcid 留空：光鸭 GCID 必须调用其服务端接口计算，本地无法得出。
//
// 空文件返回全空哈希集合且不报错（对齐 calculateUploadHashes 对 fileSize==0 的处理）。
func ComputeHashes(ctx context.Context, path string) (cloud189.HashSet, error) {
	f, err := os.Open(path)
	if err != nil {
		return cloud189.HashSet{}, domain.Wrap(domain.CodeValidation, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return cloud189.HashSet{}, domain.Wrap(domain.CodeValidation, err)
	}
	if !info.Mode().IsRegular() {
		return cloud189.HashSet{}, domain.Errorf(domain.CodeValidation, "不是普通文件：%s", path)
	}
	fileSize := info.Size()

	// 空文件：对齐 calculateUploadHashes，返回空集合而不是 32 个 0 的 MD5，
	// 避免被 hashSetIsEmpty 之外的调用方误当成有效指纹。
	if fileSize == 0 {
		return cloud189.HashSet{}, nil
	}

	md5Hasher := md5.New()
	sha1Hasher := sha1.New()
	sha256Hasher := sha256.New()

	partSize := partSizeFor(fileSize)
	partHexes, err := hashParts(ctx, f, partSize, fileSize, md5Hasher, sha1Hasher, sha256Hasher)
	if err != nil {
		return cloud189.HashSet{}, err
	}
	if len(partHexes) == 0 {
		return cloud189.HashSet{}, domain.Errorf(domain.CodeValidation, "本地文件未生成有效分片校验值：%s", path)
	}

	fileMd5 := hex.EncodeToString(md5Hasher.Sum(nil))
	sliceMd5 := fileMd5
	// 逐字对齐 calculateUploadHashes 结尾：仅当文件大于一个分片时才用分片串联的 MD5。
	if fileSize > partSize {
		sum := md5.Sum([]byte(strings.Join(partHexes, "\n")))
		sliceMd5 = hex.EncodeToString(sum[:])
	}

	return cloud189.HashSet{
		FileMd5:  fileMd5,
		SliceMd5: sliceMd5,
		Sha1:     hex.EncodeToString(sha1Hasher.Sum(nil)),
		Sha256:   hex.EncodeToString(sha256Hasher.Sum(nil)),
		// PreHash/Gcid 见函数注释：本地无法计算，留空。
	}, nil
}

// hashParts 单遍顺序读取并把数据同时喂给全量哈希与逐分片 MD5。
// 恒定内存：缓冲区固定为 partSize，不累积原始数据，只累积每片的 32 字节 hex。
func hashParts(
	ctx context.Context,
	f *os.File,
	partSize, fileSize int64,
	full ...hash.Hash,
) ([]string, error) {
	buf := make([]byte, partSize)
	partHexes := make([]string, 0, (fileSize/partSize)+1)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		n, readErr := io.ReadFull(f, buf)
		if readErr == io.EOF {
			break
		}
		if readErr == io.ErrUnexpectedEOF {
			readErr = nil
		}
		if n > 0 {
			chunk := buf[:n]
			for _, h := range full {
				h.Write(chunk)
			}
			sum := md5.Sum(chunk)
			partHexes = append(partHexes, hex.EncodeToString(sum[:]))
		}
		if readErr != nil {
			return nil, domain.Wrap(domain.CodeValidation, readErr)
		}
		if n < len(buf) {
			break
		}
	}
	return partHexes, nil
}
