package strm

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalRelPathKeepsExistingNamingWhenISOCompatibilityDisabled(t *testing.T) {
	got := LocalRelPath("影音库", []string{"电影"}, "电影.iso", false)
	want := "影音库/电影/电影.strm"
	if got != want {
		t.Fatalf("LocalRelPath() = %q, want %q", got, want)
	}
}

func TestNormalizeGroupDir(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"电影":        "电影",
		"/电影":       "电影",
		"电影/":       "电影",
		"/电影/港台":    "电影/港台",
		"电影//港台":    "电影/港台",
		"电影/../港台":  "电影/港台",
		"电影\\港台":    "电影/港台",
		" 电影 / 港台 ": "电影/港台",
	}
	for in, want := range cases {
		if got := NormalizeGroupDir(in); got != want {
			t.Errorf("NormalizeGroupDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTaskRelDirAndLocalRelPathWithGroup(t *testing.T) {
	if got := TaskRelDir("电影/港台", "电影1"); got != "电影/港台/电影1" {
		t.Fatalf("TaskRelDir = %q", got)
	}
	if got := TaskRelDir("", "电影1"); got != "电影1" {
		t.Fatalf("TaskRelDir 空分组 = %q", got)
	}
	rel := LocalRelPath("电影/港台/电影1", []string{"Season 1"}, "1.mp4", false)
	if rel != filepath.Join("电影", "港台", "电影1", "Season 1", "1.strm") {
		t.Fatalf("LocalRelPath 多级 = %q", rel)
	}
}

func TestMetadataRelPathAndLocalTaskDirKeepGroupSegments(t *testing.T) {
	if got := metadataRelPath("电影/光鸭测试", []string{"Season 1"}, "poster.jpg"); got != filepath.Join("电影", "光鸭测试", "Season 1", "poster.jpg") {
		t.Fatalf("metadataRelPath 多级 = %q", got)
	}
	if got := localTaskDir("/tmp/root", "电影/光鸭测试", []string{"Season 1"}); got != filepath.Join("/tmp/root", "电影", "光鸭测试", "Season 1") {
		t.Fatalf("localTaskDir 多级 = %q", got)
	}
	if got := TaskOutputDir("/tmp/root", "电影/光鸭测试"); got != filepath.Join("/tmp/root", "电影", "光鸭测试") {
		t.Fatalf("TaskOutputDir 多级 = %q", got)
	}
}

func TestLocalRelPathUsesISOSuffixWhenCompatibilityEnabled(t *testing.T) {
	got := LocalRelPath("影音库", []string{"电影"}, "电影.iso", true)
	want := "影音库/电影/电影.iso.strm"
	if got != want {
		t.Fatalf("LocalRelPath() = %q, want %q", got, want)
	}
}

func TestAlignMetadataItemsForISO(t *testing.T) {
	items := []metadataItem{
		newMetadataItem("nfo", "电影.nfo", "影音库", []string{"电影"}),
		newMetadataItem("sub", "电影.zh-CN.srt", "影音库", []string{"电影"}),
		newMetadataItem("poster", "电影-poster.jpg", "影音库", []string{"电影"}),
		newMetadataItem("thumb", "电影-thumb.jpg", "影音库", []string{"电影"}),
		newMetadataItem("folder-poster", "poster.jpg", "影音库", []string{"电影"}),
		newMetadataItem("other", "电影2.nfo", "影音库", []string{"电影"}),
		newMetadataItem("direct", "电影.iso.nfo", "影音库", []string{"电影"}),
		newMetadataItem("direct-poster", "电影.iso-poster.jpg", "影音库", []string{"电影"}),
	}
	media := []mediaCandidate{{fileID: "iso", fileName: "电影.iso", relDirs: []string{"电影"}}}

	got := alignMetadataItems("影音库", media, items, true)
	want := []string{
		"影音库/电影/电影.iso.nfo",
		"影音库/电影/电影.iso.zh-CN.srt",
		"影音库/电影/电影.iso-poster.jpg",
		"影音库/电影/电影.iso-thumb.jpg",
		"影音库/电影/poster.jpg",
		"影音库/电影/电影2.nfo",
		"影音库/电影/电影.iso.nfo",
		"影音库/电影/电影.iso-poster.jpg",
	}
	for i, item := range got {
		if item.relPath != want[i] {
			t.Fatalf("item %d relPath = %q, want %q", i, item.relPath, want[i])
		}
	}
	if got[0].legacyRelPath != "影音库/电影/电影.nfo" {
		t.Fatalf("legacyRelPath = %q, want original metadata path", got[0].legacyRelPath)
	}
	if got[6].legacyRelPath != "" || got[7].legacyRelPath != "" {
		t.Fatalf("direct ISO metadata should not be rewritten")
	}
}

func TestAlignMetadataItemsKeepsCompleteDottedISOStem(t *testing.T) {
	items := []metadataItem{
		newMetadataItem("nfo", "电影.2024.nfo", "影音库", []string{"电影"}),
		newMetadataItem("other", "电影.2025.nfo", "影音库", []string{"电影"}),
	}
	media := []mediaCandidate{{fileID: "iso", fileName: "电影.2024.iso", relDirs: []string{"电影"}}}

	got := alignMetadataItems("影音库", media, items, true)
	if got[0].relPath != "影音库/电影/电影.2024.iso.nfo" {
		t.Fatalf("dotted ISO metadata = %q", got[0].relPath)
	}
	if got[1].relPath != "影音库/电影/电影.2025.nfo" {
		t.Fatalf("different metadata = %q", got[1].relPath)
	}
}

func TestMigrateLegacyISOStrmFileMovesOnlyMatchingFile(t *testing.T) {
	root := t.TempDir()
	legacyRelPath := LegacyLocalRelPath("影音库", []string{"电影"}, "电影.iso")
	legacyPath := filepath.Join(root, legacyRelPath)
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "https://pan.example.com/api/strm/play/1/" + EncodeFileKey("iso-file") + "/t/token/n/%E7%94%B5%E5%BD%B1.iso\n"
	if err := os.WriteFile(legacyPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	migrated, err := MigrateLegacyISOStrmFile(root, "影音库", []string{"电影"}, "电影.iso", "iso-file", true)
	if err != nil {
		t.Fatal(err)
	}
	if !migrated {
		t.Fatal("expected legacy ISO STRM to migrate")
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy file still exists: %v", err)
	}
	currentPath := filepath.Join(root, LocalRelPath("影音库", []string{"电影"}, "电影.iso", true))
	got, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("migrated content = %q, want %q", got, content)
	}
}

func TestMigrateLegacyISOStrmFileKeepsDifferentSourceFile(t *testing.T) {
	root := t.TempDir()
	legacyPath := filepath.Join(root, LegacyLocalRelPath("影音库", nil, "电影.iso"))
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte("https://example.com/"+EncodeFileKey("other-file")), 0o644); err != nil {
		t.Fatal(err)
	}

	migrated, err := MigrateLegacyISOStrmFile(root, "影音库", nil, "电影.iso", "iso-file", true)
	if err != nil {
		t.Fatal(err)
	}
	if migrated {
		t.Fatal("different source STRM must not migrate")
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("legacy file should remain: %v", err)
	}
}

func TestMetadataSyncerMigratesAlignedISOFileWithoutDownload(t *testing.T) {
	root := t.TempDir()
	legacyRelPath := "影音库/电影/电影.nfo"
	legacyPath := filepath.Join(root, legacyRelPath)
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte("metadata"), 0o644); err != nil {
		t.Fatal(err)
	}

	item := metadataItem{relPath: "影音库/电影/电影.iso.nfo", legacyRelPath: legacyRelPath}
	migrated, err := migrateLegacyMetadata(root, item)
	if err != nil {
		t.Fatal(err)
	}
	if !migrated {
		t.Fatal("expected local metadata migration")
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy metadata still exists: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, item.relPath)); err != nil || string(got) != "metadata" {
		t.Fatalf("migrated metadata = %q, err=%v", got, err)
	}
}

func TestDescribeMediaInfoParsesEpisodeAndTitle(t *testing.T) {
	source := "/CloudNAS/影视/已整理/国产剧集/兰香如故 (2026) {tmdb=282326}/Season 01/兰香如故.2026.S01E31.第31集.2160p.WEB-DL.H.265.10-bit.60fps-UBWEB.mkv"
	info := DescribeMediaInfo(source, "兰香如故.2026.S01E31.第31集.2160p.WEB-DL.H.265.10-bit.60fps-UBWEB.mkv",
		[]string{"国产剧集", "兰香如故 (2026) {tmdb=282326}", "Season 01"})

	if !info.HasSeason || info.Season != 1 {
		t.Fatalf("Season = %d (has=%v), want 1", info.Season, info.HasSeason)
	}
	if !info.HasEpisode || info.Episode != 31 {
		t.Fatalf("Episode = %d (has=%v), want 31", info.Episode, info.HasEpisode)
	}
	if info.Title != "兰香如故 (2026) {tmdb=282326}" {
		t.Fatalf("Title = %q, want 剧名目录", info.Title)
	}
	if info.SourcePath != source {
		t.Fatalf("SourcePath = %q, want %q", info.SourcePath, source)
	}
}

func TestDescribeMediaInfoMovieHasNoEpisode(t *testing.T) {
	info := DescribeMediaInfo("/媒体/电影/流浪地球2.2023.2160p.mkv", "流浪地球2.2023.2160p.mkv", []string{"电影"})
	if info.HasEpisode || info.HasSeason {
		t.Fatalf("电影不应解析出季集: %+v", info)
	}
	if info.Title != "电影" {
		t.Fatalf("Title = %q, want 电影", info.Title)
	}
}

func TestDescribeMediaInfoFallsBackToFileNameWhenSourceMissing(t *testing.T) {
	// 当前目录生成等场景拿不到源文件完整路径：此时 source 为空、标题退回文件名去扩展名。
	info := DescribeMediaInfo("", "第03集.mkv", []string{"电视剧", "某剧", "Season 2"})
	if info.SourcePath != "" {
		t.Fatalf("SourcePath = %q, want empty", info.SourcePath)
	}
	if !info.HasEpisode || info.Episode != 3 {
		t.Fatalf("Episode = %d (has=%v), want 3", info.Episode, info.HasEpisode)
	}
	if info.Title != "某剧" {
		t.Fatalf("Title = %q, want 某剧", info.Title)
	}
}

func TestBuildStrmLogArgsIncludesAllFields(t *testing.T) {
	item := mediaCandidate{
		fileID:    "video-1",
		fileName:  "兰香如故.2026.S01E31.第31集.2160p.WEB-DL.H.265.10-bit.60fps-UBWEB.mkv",
		relDirs:   []string{"国产剧集", "兰香如故 (2026) {tmdb=282326}", "Season 01"},
		sourceDir: "/CloudNAS/CloudDrive/NAS-WebDAV/中国移动云盘/影视/已整理/国产剧集/兰香如故 (2026) {tmdb=282326}/Season 01",
	}
	display, args := buildStrmLogArgs(item,
		"/media/移动STRM/国产剧集/兰香如故 (2026) {tmdb=282326}/Season 01/兰香如故.2026.S01E31.第31集.2160p.WEB-DL.H.265.10-bit.60fps-UBWEB.strm",
		"国产剧集/兰香如故 (2026) {tmdb=282326}/Season 01/兰香如故.2026.S01E31.第31集.2160p.WEB-DL.H.265.10-bit.60fps-UBWEB.strm",
		"国产剧集")

	wantSource := "/CloudNAS/CloudDrive/NAS-WebDAV/中国移动云盘/影视/已整理/国产剧集/兰香如故 (2026) {tmdb=282326}/Season 01/兰香如故.2026.S01E31.第31集.2160p.WEB-DL.H.265.10-bit.60fps-UBWEB.mkv"
	wantStrm := "/media/移动STRM/国产剧集/兰香如故 (2026) {tmdb=282326}/Season 01/兰香如故.2026.S01E31.第31集.2160p.WEB-DL.H.265.10-bit.60fps-UBWEB.strm"
	if want := wantSource + " => " + wantStrm; display != want {
		t.Fatalf("display = %q, want %q", display, want)
	}

	fields := map[string]any{}
	for i := 0; i+1 < len(args); i += 2 {
		key, _ := args[i].(string)
		fields[key] = args[i+1]
	}
	for key, want := range map[string]any{
		"source":   wantSource,
		"strm":     wantStrm,
		"title":    "兰香如故 (2026) {tmdb=282326}",
		"file":     item.fileName,
		"season":   1,
		"episode":  31,
		"rel_path": "国产剧集/兰香如故 (2026) {tmdb=282326}/Season 01/兰香如故.2026.S01E31.第31集.2160p.WEB-DL.H.265.10-bit.60fps-UBWEB.strm",
		"root":     "国产剧集",
	} {
		if got, ok := fields[key]; !ok || got != want {
			t.Fatalf("field %q = %v (present=%v), want %v", key, got, ok, want)
		}
	}

	t.Logf("渲染后的日志: level=INFO msg=\"STRM 生成成功: %s\" %s", display, formatLogArgsForTest(args))
}

func formatLogArgsForTest(args []any) string {
	var b strings.Builder
	for i := 0; i+1 < len(args); i += 2 {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%v=%v", args[i], args[i+1])
	}
	return b.String()
}

// 当前目录生成（API 手动触发）时 sourceDir 即完整远端目录，日志须据此拼出源文件路径。
func TestLogStrmFileFromCurrentDirectoryUsesFullRemoteDir(t *testing.T) {
	item := mediaCandidate{
		fileID:    "ep31",
		fileName:  "兰香如故.2026.S01E31.第31集.2160p.WEB-DL.H.265.mkv",
		relDirs:   []string{"兰香如故 (2026)", "Season 01"},
		sourceDir: "/CloudNAS/影视/已整理/国产剧集/兰香如故 (2026)/Season 01",
	}
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logStrmFile(logger, "STRM 生成成功", item,
		"/media/移动STRM/任务/兰香如故 (2026)/Season 01/兰香如故.2026.S01E31.第31集.2160p.WEB-DL.H.265.strm",
		"任务/兰香如故 (2026)/Season 01/兰香如故.2026.S01E31.第31集.2160p.WEB-DL.H.265.strm",
		"任务")

	out := buf.String()
	t.Logf("当前目录生成日志:\n%s", out)
	for _, want := range []string{
		"/CloudNAS/影视/已整理/国产剧集/兰香如故 (2026)/Season 01/兰香如故.2026.S01E31.第31集.2160p.WEB-DL.H.265.mkv",
		"=> ",
		"season=1", "episode=31", "title=",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("日志缺少 %q，实际:\n%s", want, out)
		}
	}
}
