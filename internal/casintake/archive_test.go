package casintake

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"litepan/internal/cas"
	"litepan/internal/cas/cloud189"
)

// sampleCAS 是一份真实的 V2 清单（天翼云盘：只有 md5/sliceMd5）。
// 取用户实际发来的压缩包中的内容，保证测试走的是真实形态。
const sampleCAS = "eyJuYW1lIjoi5Zac5qyi6auY5YW054ixLkxvdmUuTXkuV2F5LjIwMjYuMjE2MHAuV0VCLURMLkFBQy5IMjY0LUhEU1dFQi5ta3YiLCJzaXplIjoxMTE0NDExNTMwNywibWQ1IjoiN0VFMzcyM0VBNzE0MTVCQkJCMTRGNTY2OTgzRDE3RUUiLCJzbGljZU1kNSI6IkYyRjY5NUM5RUIzMzZFREE5MzEyMDg4MkRFM0FGQTM3IiwiY3JlYXRlX3RpbWUiOiIxNzkwNjE5ODEyIn0="

// quarkCAS 是一份夸克清单（只有 preHash），用于验证同一压缩包里
// 不同网盘的 .cas 会被分别识别。
const quarkCAS = `{"name":"测试影片.mkv","size":123456,"preHash":"ABCDEF0123456789","create_time":"1790619812"}`

// makeZip 用给定条目构造 zip 字节。
func makeZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range entries {
		f, err := w.Create(name)
		if err != nil {
			t.Fatalf("构造 zip 条目 %s 失败：%v", name, err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatalf("写入 zip 条目 %s 失败：%v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("关闭 zip 失败：%v", err)
	}
	return buf.Bytes()
}

// TestIsArchiveFileName 表驱动覆盖后缀判定。
func TestIsArchiveFileName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"喜欢高兴爱 (2026).zip", true},
		{"a.ZIP", true},
		{"x.tar", true},
		{"x.tar.gz", true},
		{"x.tgz", true},
		{"x.gz", true},
		{"影片.mkv.cas", false},
		{"x.7z", false}, // 不支持，交给 isUnsupportedArchiveName
		{"x.rar", false},
		{"x.txt", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isArchiveFileName(c.name); got != c.want {
			t.Errorf("isArchiveFileName(%q) = %v，期望 %v", c.name, got, c.want)
		}
	}
}

// TestIsCasIntakeName 确认接收判定同时覆盖 .cas 与压缩包。
func TestIsCasIntakeName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"影片.mkv.cas", true},
		{"喜欢高兴爱 (2026).zip", true},
		{"x.7z", true}, // 需要给出「不支持」提示，因此也要进入处理流程
		{"随手发的照片.jpg", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isCasIntakeName(c.name); got != c.want {
			t.Errorf("isCasIntakeName(%q) = %v，期望 %v", c.name, got, c.want)
		}
	}
}

// TestExtractCasFromZipSingle 覆盖用户实际场景：目录 + 一个 .cas。
func TestExtractCasFromZipSingle(t *testing.T) {
	data := makeZip(t, map[string]string{
		"喜欢高兴爱 (2026)/": "",
		"喜欢高兴爱 (2026)/喜欢高兴爱.Love.My.Way.2026.2160p.WEB-DL.AAC.H264-HDSWEB.mkv.cas": sampleCAS,
	})
	items, err := extractCasFromArchive("喜欢高兴爱 (2026).zip", data)
	if err != nil {
		t.Fatalf("解压失败：%v", err)
	}
	if len(items) != 1 {
		t.Fatalf("应取出 1 个 .cas，实际 %d 个", len(items))
	}
	if items[0].Content != sampleCAS {
		t.Errorf("清单内容与原始不一致")
	}
	// 条目名必须被压成 base name：显示与后续判定都不该带目录。
	if strings.Contains(items[0].Name, "/") {
		t.Errorf("条目名应去掉目录，实际 %q", items[0].Name)
	}
	if !strings.HasSuffix(items[0].Name, ".cas") {
		t.Errorf("条目名应以 .cas 结尾，实际 %q", items[0].Name)
	}
	// 清单本身要能被真实解析器认出来（端到端的关键一环）。
	if _, err := base64.StdEncoding.DecodeString(items[0].Content); err != nil {
		t.Errorf("样本清单应是合法 base64：%v", err)
	}
}

// TestExtractCasFromZipMultipleDrives 一个压缩包里混装不同网盘的清单。
func TestExtractCasFromZipMultipleDrives(t *testing.T) {
	data := makeZip(t, map[string]string{
		"a/天翼影片.mkv.cas": sampleCAS,
		"b/夸克影片.mkv.cas": quarkCAS,
		"b/说明.txt":       "不是清单，应被忽略",
		"b/封面.jpg":       "\xff\xd8\xff",
	})
	items, err := extractCasFromArchive("混合.zip", data)
	if err != nil {
		t.Fatalf("解压失败：%v", err)
	}
	if len(items) != 2 {
		t.Fatalf("应取出 2 个 .cas，实际 %d 个：%+v", len(items), items)
	}
}

// TestExtractCasFromZipNoCas 压缩包里没有 .cas 时必须给出明确错误，
// 而不是静默返回空（用户需要知道为什么没转存）。
func TestExtractCasFromZipNoCas(t *testing.T) {
	data := makeZip(t, map[string]string{"readme.txt": "hello"})
	_, err := extractCasFromArchive("无清单.zip", data)
	if err == nil {
		t.Fatal("压缩包内无 .cas 时应报错")
	}
	if !strings.Contains(err.Error(), "没有找到 .cas") {
		t.Errorf("错误信息应说明未找到 .cas，实际：%v", err)
	}
}

// TestExtractCasFromZipRejectsTraversal 含上级目录的条目必须拒绝。
func TestExtractCasFromZipRejectsTraversal(t *testing.T) {
	data := makeZip(t, map[string]string{
		"../../etc/evil.mkv.cas": sampleCAS,
	})
	_, err := extractCasFromArchive("evil.zip", data)
	if err == nil {
		t.Fatal("含路径穿越的压缩包应被拒绝")
	}
	if !strings.Contains(err.Error(), "路径非法") {
		t.Errorf("错误应指明路径非法，实际：%v", err)
	}
}

// TestExtractCasFromZipRejectsAbsolutePath 绝对路径条目同样拒绝。
func TestExtractCasFromZipRejectsAbsolutePath(t *testing.T) {
	data := makeZip(t, map[string]string{"/tmp/evil.mkv.cas": sampleCAS})
	if _, err := extractCasFromArchive("evil.zip", data); err == nil {
		t.Fatal("绝对路径条目应被拒绝")
	}
}

// TestExtractCasFromZipRejectsTooManyEntries 条目数上限。
func TestExtractCasFromZipRejectsTooManyEntries(t *testing.T) {
	entries := map[string]string{}
	for i := 0; i < maxArchiveEntries+1; i++ {
		entries[withIndex("f", i)+".jpg"] = "x"
	}
	data := makeZip(t, entries)
	_, err := extractCasFromArchive("many.zip", data)
	if err == nil {
		t.Fatal("条目数超限应被拒绝")
	}
	if !strings.Contains(err.Error(), "条目过多") {
		t.Errorf("错误应说明条目过多，实际：%v", err)
	}
}

func withIndex(prefix string, i int) string {
	return prefix + string(rune('a'+i/1000%26)) + string(rune('a'+i/100%26)) + string(rune('a'+i/10%26)) + string(rune('a'+i%26))
}

// TestExtractCasFromZipRejectsOversizedEntry 单条目超过上限必须拒绝，
// 且要在读取前就根据声明大小拦下（避免先解压再判断）。
func TestExtractCasFromZipRejectsOversizedEntry(t *testing.T) {
	big := strings.Repeat("A", maxArchiveEntryBytes+1024)
	data := makeZip(t, map[string]string{"big.mkv.cas": big})
	_, err := extractCasFromArchive("big.zip", data)
	if err == nil {
		t.Fatal("超大条目应被拒绝")
	}
	if !strings.Contains(err.Error(), "过大") && !strings.Contains(err.Error(), "超过") {
		t.Errorf("错误应说明条目过大，实际：%v", err)
	}
}

// TestExtractCasFromZipSkipsNestedArchive 嵌套压缩包不递归解压（zip bomb 防护），
// 内层 .cas 不会被取出；外层若无其它 .cas 则应报「没有找到」。
func TestExtractCasFromZipSkipsNestedArchive(t *testing.T) {
	inner := makeZip(t, map[string]string{"inner.mkv.cas": sampleCAS})
	outerData := makeZip(t, map[string]string{
		"nested.zip": string(inner),
	})
	_, err := extractCasFromArchive("outer.zip", outerData)
	if err == nil {
		t.Fatal("只有嵌套压缩包时不应取出任何 .cas")
	}
}

// TestExtractCasFromZipBrokenData 损坏数据要报错而不是 panic。
func TestExtractCasFromZipBrokenData(t *testing.T) {
	_, err := extractCasFromArchive("broken.zip", []byte("这不是 zip 内容"))
	if err == nil {
		t.Fatal("损坏的 zip 应报错")
	}
}

// TestExtractCasFromArchiveEmptyData 空数据要报错。
func TestExtractCasFromArchiveEmptyData(t *testing.T) {
	if _, err := extractCasFromArchive("empty.zip", nil); err == nil {
		t.Fatal("空内容应报错")
	}
}

// TestExtractCasFromTarGz 覆盖 tar.gz 形态。
func TestExtractCasFromTarGz(t *testing.T) {
	data := makeTarGz(t, map[string]string{
		"剧集/天翼影片.mkv.cas": sampleCAS,
		"剧集/说明.txt":       "忽略",
	})
	items, err := extractCasFromArchive("pack.tar.gz", data)
	if err != nil {
		t.Fatalf("解压 tar.gz 失败：%v", err)
	}
	if len(items) != 1 {
		t.Fatalf("应取出 1 个 .cas，实际 %d 个", len(items))
	}
	if items[0].Content != sampleCAS {
		t.Error("tar.gz 中的清单内容不一致")
	}
}

// TestExtractCasFromTar 覆盖未压缩 tar。
func TestExtractCasFromTar(t *testing.T) {
	data := makeTar(t, map[string]string{"a.mkv.cas": sampleCAS})
	items, err := extractCasFromArchive("pack.tar", data)
	if err != nil {
		t.Fatalf("解压 tar 失败：%v", err)
	}
	if len(items) != 1 {
		t.Fatalf("应取出 1 个 .cas，实际 %d 个", len(items))
	}
}

// TestExtractCasFromGz 覆盖单个 .gz（内容即清单）。
func TestExtractCasFromGz(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write([]byte(sampleCAS)); err != nil {
		t.Fatalf("写 gzip 失败：%v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("关闭 gzip 失败：%v", err)
	}
	items, err := extractCasFromArchive("单文件.cas.gz", buf.Bytes())
	if err != nil {
		t.Fatalf("解压 gz 失败：%v", err)
	}
	if len(items) != 1 || items[0].Content != sampleCAS {
		t.Fatalf("gz 解压结果不符：%+v", items)
	}
}

// TestExtractCasFromArchiveRejectsUnsupported 未知后缀要报明确错误。
func TestExtractCasFromArchiveRejectsUnsupported(t *testing.T) {
	_, err := extractCasFromArchive("x.weird", []byte("data"))
	if err == nil {
		t.Fatal("不支持的后缀应报错")
	}
	if !strings.Contains(err.Error(), "不支持的压缩包格式") {
		t.Errorf("错误应说明格式不支持，实际：%v", err)
	}
}

// TestIsUnsupportedArchiveName 明确「看起来是压缩包但不支持」的格式，
// 这样用户能收到可执行的提示而不是毫无反应。
func TestIsUnsupportedArchiveName(t *testing.T) {
	for _, name := range []string{"a.7z", "a.RAR", "b.tar.bz2", "c.tar.xz", "d.zipx"} {
		if !isUnsupportedArchiveName(name) {
			t.Errorf("%q 应被判为不支持的压缩包格式", name)
		}
	}
	for _, name := range []string{"a.zip", "b.tar.gz", "c.mkv.cas", "d.txt"} {
		if isUnsupportedArchiveName(name) {
			t.Errorf("%q 不应被判为不支持的压缩包格式", name)
		}
	}
}

// --- 构造辅助 ---

func makeTar(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range entries {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("写 tar 头失败：%v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("写 tar 内容失败：%v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("关闭 tar 失败：%v", err)
	}
	return buf.Bytes()
}

func makeTarGz(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, content := range entries {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("写 tar 头失败：%v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("写 tar 内容失败：%v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("关闭 tar 失败：%v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("关闭 gzip 失败：%v", err)
	}
	return buf.Bytes()
}

// TestExtractRealUserSampleZip 用用户实际发来的压缩包做端到端验证：
// 解压 → 取出 .cas → 经真实解析器判定网盘。
// 这条链路就是「转发压缩包 → 自动转存到对应网盘目录」的完整前半程。
func TestExtractRealUserSampleZip(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "real_user_sample.zip"))
	if err != nil {
		t.Fatalf("读取真实样本失败：%v", err)
	}
	items, err := extractCasFromArchive("喜欢高兴爱 (2026).zip", data)
	if err != nil {
		t.Fatalf("解压真实样本失败：%v", err)
	}
	if len(items) != 1 {
		t.Fatalf("真实样本应取出 1 个 .cas，实际 %d 个", len(items))
	}

	manifest, err := cloud189.ParseManifestV2(items[0].Content)
	if err != nil {
		t.Fatalf("真实样本清单解析失败：%v", err)
	}
	if got := cas.ResolveDriveType(manifest); got != "cloud189" {
		t.Errorf("网盘判定 = %q，期望 cloud189（样本带 md5+sliceMd5）", got)
	}
	if manifest.FileSize <= 0 || manifest.FileName == "" {
		t.Errorf("清单字段不完整：%+v", manifest)
	}
	if !strings.HasSuffix(manifest.FileName, ".mkv") {
		t.Errorf("清单文件名异常：%q", manifest.FileName)
	}
}
