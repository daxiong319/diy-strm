package localhash

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile 在临时目录造一个指定大小的文件（内容为确定性可重复的字节序列）。
func writeFile(t *testing.T, dir, name string, size int64) string {
	t.Helper()
	p := filepath.Join(dir, name)
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte(i % 251)
	}
	if err := os.WriteFile(p, buf, 0o644); err != nil {
		t.Fatalf("造测试文件失败：%v", err)
	}
	return p
}

func mustHashes(t *testing.T, path string) (fileMd5, sliceMd5, sha1Hex, sha256Hex string) {
	t.Helper()
	hs, err := ComputeHashes(context.Background(), path)
	if err != nil {
		t.Fatalf("ComputeHashes(%s) 出错：%v", path, err)
	}
	return hs.FileMd5, hs.SliceMd5, hs.Sha1, hs.Sha256
}

func TestComputeHashesEmptyFile(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "empty.bin", 0)

	hs, err := ComputeHashes(context.Background(), p)
	if err != nil {
		t.Fatalf("空文件不应报错，got err=%v", err)
	}
	// 对齐 calculateUploadHashes：空文件返回空集合，不返回 32 个 0 的 MD5。
	if hs.FileMd5 != "" || hs.SliceMd5 != "" || hs.Sha1 != "" || hs.Sha256 != "" ||
		hs.PreHash != "" || hs.Gcid != "" {
		t.Fatalf("空文件应返回全空哈希集合，got %+v", hs)
	}
}

func TestComputeHashesSmallerThanOnePart(t *testing.T) {
	dir := t.TempDir()
	// 小于一个分片：1KB
	size := int64(1024)
	p := writeFile(t, dir, "small.bin", size)

	fileMd5, sliceMd5, sha1Hex, sha256Hex := mustHashes(t, p)

	data, _ := os.ReadFile(p)
	wantMd5 := md5.Sum(data)
	wantSha1 := sha1.Sum(data)
	wantSha256 := sha256.Sum256(data)

	if fileMd5 != hex.EncodeToString(wantMd5[:]) {
		t.Fatalf("FileMd5=%s, want %s", fileMd5, hex.EncodeToString(wantMd5[:]))
	}
	// 单分片（含不足一片）时 SliceMd5 直接等于全量 MD5（对齐 calculateUploadHashes）。
	if sliceMd5 != fileMd5 {
		t.Fatalf("小于一片时 SliceMd5 应等于 FileMd5，got slice=%s file=%s", sliceMd5, fileMd5)
	}
	if sha1Hex != hex.EncodeToString(wantSha1[:]) {
		t.Fatalf("Sha1=%s, want %s", sha1Hex, hex.EncodeToString(wantSha1[:]))
	}
	if sha256Hex != hex.EncodeToString(wantSha256[:]) {
		t.Fatalf("Sha256=%s, want %s", sha256Hex, hex.EncodeToString(wantSha256[:]))
	}
	// 全部小写 hex
	for name, v := range map[string]string{"fileMd5": fileMd5, "sha1": sha1Hex, "sha256": sha256Hex} {
		if v != strings.ToLower(v) {
			t.Fatalf("%s 应为小写 hex，got %s", name, v)
		}
	}
}

func TestComputeHashesExactlyOnePart(t *testing.T) {
	dir := t.TempDir()
	size := defaultUploadPartSize
	p := writeFile(t, dir, "exact.bin", size)

	fileMd5, sliceMd5, _, _ := mustHashes(t, p)
	// 恰好一片：fileSize > partSize 为 false，SliceMd5 仍等于全量 MD5。
	if sliceMd5 != fileMd5 {
		t.Fatalf("恰好一片时 SliceMd5 应等于 FileMd5，got slice=%s file=%s", sliceMd5, fileMd5)
	}
}

func TestComputeHashesPartBoundaryPlusOne(t *testing.T) {
	dir := t.TempDir()
	// 一片 + 1 字节：跨片边界，触发 SliceMd5 = MD5(分片MD5们用 "\n" 串联)
	size := defaultUploadPartSize + 1
	p := writeFile(t, dir, "cross.bin", size)

	fileMd5, sliceMd5, _, _ := mustHashes(t, p)
	if sliceMd5 == fileMd5 {
		t.Fatalf("跨片时 SliceMd5 不应等于全量 FileMd5（说明分片串联逻辑没生效）")
	}
	if fileMd5 == "" || sliceMd5 == "" {
		t.Fatalf("哈希不应为空")
	}

	// 独立重算一遍分片串联值做交叉验证
	data, _ := os.ReadFile(p)
	part1 := md5.Sum(data[:defaultUploadPartSize])
	part2 := md5.Sum(data[defaultUploadPartSize:])
	joined := hex.EncodeToString(part1[:]) + "\n" + hex.EncodeToString(part2[:])
	wantSlice := md5.Sum([]byte(joined))
	if sliceMd5 != hex.EncodeToString(wantSlice[:]) {
		t.Fatalf("SliceMd5=%s, want %s", sliceMd5, hex.EncodeToString(wantSlice[:]))
	}
}

func TestPartSizeForBranches(t *testing.T) {
	d := defaultUploadPartSize
	const gb = int64(1024 * 1024 * 1024)

	cases := []struct {
		name     string
		fileSize int64
		want     int64
	}{
		{"零字节", 0, d},
		{"小文件", 1024, d},
		{"恰好 999 片不升级", d * 999, d},
		{"999 片 + 1 → 双片", d*999 + 1, d * 2},
		{"恰好 1998 片仍是双片", d * 1998, d * 2},
		{"1998 片 + 1 进入多倍分支", d*1998 + 1, 5 * d},
	}
	for _, c := range cases {
		if got := partSizeFor(c.fileSize); got != c.want {
			t.Errorf("%s: partSizeFor(%d)=%d, want %d", c.name, c.fileSize, got, c.want)
		}
	}

	// 大文件多倍分支：25GB → multiple = ceil(25GB / (1999*10MB)) = ceil(1.28) = 2 → 不足 5 取 5
	if got := partSizeFor(25 * gb); got != 5*d {
		t.Errorf("25GB: partSizeFor=%d, want %d", got, 5*d)
	}
	// 100GB → ceil(100/19.99) = 6 → 6*d
	if got := partSizeFor(100 * gb); got != 6*d {
		t.Errorf("100GB: partSizeFor=%d, want %d", got, 6*d)
	}
	// 单调性：更大的文件分片不会更小
	prev := int64(0)
	for _, size := range []int64{0, d, d * 999, d * 1000, 25 * gb, 100 * gb, 500 * gb} {
		got := partSizeFor(size)
		if got < prev {
			t.Errorf("partSizeFor 非单调：size=%d 得到 %d < 之前 %d", size, got, prev)
		}
		prev = got
	}
}

func TestComputeHashesContextCancelled(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "cancel.bin", defaultUploadPartSize+1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 一开始就取消

	if _, err := ComputeHashes(ctx, p); err == nil {
		t.Fatalf("ctx 已取消时应返回错误")
	}
}

func TestComputeHashesRejectsNonRegularFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := ComputeHashes(context.Background(), dir); err == nil {
		t.Fatalf("目录不应被当作可哈希文件")
	}
}

func TestComputeHashesMissingFile(t *testing.T) {
	if _, err := ComputeHashes(context.Background(), filepath.Join(t.TempDir(), "nope.bin")); err == nil {
		t.Fatalf("不存在的文件应报错")
	}
}

func TestComputeHashesLeavesPreHashAndGcidEmpty(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "x.bin", 2048)

	hs, err := ComputeHashes(context.Background(), p)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	// PreHash：夸克实际秒传只消费 md5+sha1，本库无 preHash 构造实现，故留空。
	if hs.PreHash != "" {
		t.Errorf("PreHash 应为空，got %q", hs.PreHash)
	}
	// Gcid：光鸭需要调其服务端接口，本地无法计算。
	if hs.Gcid != "" {
		t.Errorf("Gcid 应为空，got %q", hs.Gcid)
	}
}
