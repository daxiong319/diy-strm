package backuprestore

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// T14：备份要含 config + STRM 目录，恢复目标可以是本地或网盘。

// newSTRMTestEnv 在既有测试环境上加一个 STRM 目录。
func newSTRMTestEnv(t *testing.T) (*testEnv, string) {
	t.Helper()
	env := newTestEnv(t)
	strmDir := filepath.Join(env.root, "strm")
	if err := os.MkdirAll(strmDir, 0o700); err != nil {
		t.Fatal(err)
	}
	service, err := New(Options{
		DataDir:       env.dataDir,
		DBPath:        env.dbPath,
		Version:       "v-test",
		DB:            env.db,
		Configs:       env.store.Configs,
		Secret:        []byte("backup-test-secret-key-32-bytes!!"),
		StrmDir:       strmDir,
		UploadToLocal: &fakeUploader{},
	})
	if err != nil {
		t.Fatal(err)
	}
	env.service = service
	return env, strmDir
}

func writeSTRM(t *testing.T, dir, rel, body string) {
	t.Helper()
	target := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// readSTRM 读回磁盘上的 STRM 文件（相对路径 → 内容）。
func readSTRM(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil //nolint:nilerr // 单条读不到就跳过
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return nil //nolint:nilerr // 同上
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil //nolint:nilerr // 同上
		}
		out[filepath.ToSlash(rel)] = string(body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// fakeUploader 是一份内存网盘。
type fakeUploader struct {
	files    map[string]string // "<parentID>/<name>" → 内容
	dirs     map[string]bool   // 目录 ID
	dirNames map[string]string // 目录 ID → 名称
	nextID   int
	// failName 非空时上传同名文件会失败，用来验证「单文件失败不中断整体」。
	failName string
	// skipCreateDir 为 true 时 EnsureSTRMDirectory 不记录已存在的目录，
	// 用来验证幂等是靠适配器复用 ID 实现的。
	skipCreateDir bool
}

func newFakeUploader() *fakeUploader {
	return &fakeUploader{files: map[string]string{}, dirs: map[string]bool{"root": true}, dirNames: map[string]string{"root": ""}}
}

func (u *fakeUploader) UploadSTRMFile(_ context.Context, accountID int64, req STRMUploadRequest) (*STRMUploadResult, error) {
	if accountID != 7 {
		return nil, context.Canceled
	}
	if u.failName != "" && strings.Contains(req.FileName, u.failName) {
		return nil, context.Canceled
	}
	key := req.ParentID + "/" + req.FileName
	u.files[key] = "uploaded:" + filepath.Base(req.LocalPath)
	return &STRMUploadResult{FileID: "file-" + key, ParentID: req.ParentID, FileName: req.FileName}, nil
}

func (u *fakeUploader) EnsureSTRMDirectory(_ context.Context, accountID int64, parentID, name string) (string, error) {
	if accountID != 7 {
		return "", context.Canceled
	}
	if u.skipCreateDir {
		u.nextID++
	}
	id := "dir-" + parentID + "-" + name
	u.dirs[id] = true
	u.dirNames[id] = name
	return id, nil
}

func (u *fakeUploader) paths() []string {
	out := make([]string, 0, len(u.files))
	for key := range u.files {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func TestFullBackupIncludesSTRMDirectoryAndRestoresItLocally(t *testing.T) {
	env, strmDir := newSTRMTestEnv(t)
	t.Cleanup(func() {
		if env.db != nil {
			_ = env.db.Close()
		}
	})
	writeSTRM(t, strmDir, "剧集/某剧/Season 01/S01E01.strm", "http://a/1")
	writeSTRM(t, strmDir, "剧集/某剧/Season 01/S01E02.strm", "http://a/2")
	writeSTRM(t, strmDir, "电影/某片.strm", "http://a/3")

	record, err := env.service.Create(env.ctx, CreateRequest{Note: "full", Password: testPassword, IncludeAccounts: true})
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(record.Components, "strm") {
		t.Fatalf("components = %v，缺少 strm", record.Components)
	}

	// 恢复前先把 STRM 目录搅乱：删一个、改一个、多留一个。
	if err := os.Remove(filepath.Join(strmDir, "剧集", "某剧", "Season 01", "S01E02.strm")); err != nil {
		t.Fatal(err)
	}
	writeSTRM(t, strmDir, "剧集/某剧/Season 01/S01E03.strm", "旧的残留")

	summary, err := env.service.PrepareRestore(env.ctx, record.ID, RestoreRequest{Password: testPassword})
	if err != nil {
		t.Fatal(err)
	}
	if summary.STRMCount != 3 || summary.STRMTarget != "local" {
		t.Fatalf("summary = %+v", summary)
	}
	got := readSTRM(t, strmDir)
	want := map[string]string{
		"剧集/某剧/Season 01/S01E01.strm": "http://a/1",
		"剧集/某剧/Season 01/S01E02.strm": "http://a/2",
		"电影/某片.strm":                  "http://a/3",
	}
	if len(got) != len(want) {
		t.Fatalf("恢复后文件数 = %d，期望 %d：%v", len(got), len(want), got)
	}
	for name, body := range want {
		if got[name] != body {
			t.Fatalf("%s = %q，期望 %q（整体替换还是合并？这是关键区别）", name, got[name], body)
		}
	}
	if _, ok := got["剧集/某剧/Season 01/S01E03.strm"]; ok {
		t.Fatalf("备份里没有的文件留在了目录里：%v", got)
	}
}

func TestSettingsBackupDoesNotIncludeSTRM(t *testing.T) {
	env, strmDir := newSTRMTestEnv(t)
	t.Cleanup(func() {
		if env.db != nil {
			_ = env.db.Close()
		}
	})
	writeSTRM(t, strmDir, "a.strm", "http://a/1")
	record, err := env.service.Create(env.ctx, CreateRequest{Note: "settings", Password: testPassword})
	if err != nil {
		t.Fatal(err)
	}
	if containsString(record.Components, "strm") {
		t.Fatalf("设置级备份不该带 strm：%v", record.Components)
	}
}

func TestRestoreWithoutSTRMLeavesExistingDirectoryAlone(t *testing.T) {
	env, strmDir := newSTRMTestEnv(t)
	t.Cleanup(func() {
		if env.db != nil {
			_ = env.db.Close()
		}
	})
	writeSTRM(t, strmDir, "keep.strm", "http://a/1")
	record, err := env.service.Create(env.ctx, CreateRequest{Note: "settings", Password: testPassword})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := env.service.PrepareRestore(env.ctx, record.ID, RestoreRequest{Password: testPassword})
	if err != nil {
		t.Fatal(err)
	}
	if summary.STRMCount != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	if body := readSTRM(t, strmDir)["keep.strm"]; body != "http://a/1" {
		t.Fatalf("keep.strm = %q，恢复一份不含 STRM 的备份不该动它", body)
	}
}

func TestRestoreSTRMToCloudKeepsDirectoryStructure(t *testing.T) {
	env, strmDir := newSTRMTestEnv(t)
	t.Cleanup(func() {
		if env.db != nil {
			_ = env.db.Close()
		}
	})
	up := newFakeUploader()
	env.service.uploader = up
	writeSTRM(t, strmDir, "剧集/某剧/Season 01/S01E01.strm", "http://a/1")
	writeSTRM(t, strmDir, "剧集/某剧/Season 01/S01E02.strm", "http://a/2")
	writeSTRM(t, strmDir, "电影/某片.strm", "http://a/3")

	record, err := env.service.Create(env.ctx, CreateRequest{Note: "full", Password: testPassword, IncludeAccounts: true})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := env.service.PrepareRestore(env.ctx, record.ID, RestoreRequest{
		Password:   testPassword,
		STRMTarget: &RestoreTarget{Local: false, AccountID: 7, ParentID: "cloud-root"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.STRMTarget != "cloud" || summary.STRMCount != 3 || len(summary.STRMFailures) != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	paths := up.paths()
	// 目录 ID 由适配器生成，路径里带目录 ID；这里只断言三个文件都上传了，
	// 且父目录随层级递进（不同父目录 ⇒ 结构被保留，而不是全塞进根目录）。
	if len(paths) != 3 {
		t.Fatalf("上传文件数 = %d：%v", len(paths), paths)
	}
	parents := map[string]bool{}
	for _, p := range paths {
		parent := p[:strings.LastIndex(p, "/")]
		parents[parent] = true
	}
	// 3 个文件分布在 2 个叶子目录里（剧集/某剧/Season 01 有 2 个），
	// 所以父目录数是 2；关键是这个数不是 1 —— 层级要是塌了就全都进了根目录。
	if len(parents) != 2 {
		t.Fatalf("文件落在了 %d 个父目录，期望 2（目录层级丢了）：%v", len(parents), paths)
	}
	if !up.dirs["dir-cloud-root-剧集"] || !up.dirs["dir-dir-cloud-root-剧集-某剧"] {
		t.Fatalf("中间目录没建：%v", up.dirNames)
	}
	// 本地暂存应该被清掉，避免几百 MB 的重复副本留在磁盘上。
	if entries, err := os.ReadDir(filepath.Join(env.service.restoreDir, "staging")); err == nil {
		for _, entry := range entries {
			staged := filepath.Join(env.service.restoreDir, "staging", entry.Name(), "strm-staging")
			if _, statErr := os.Stat(staged); statErr == nil {
				t.Fatalf("网盘恢复后本地暂存还在：%s", staged)
			}
		}
	}
}

func TestRestoreSTRMToCloudReportsPartialFailures(t *testing.T) {
	env, strmDir := newSTRMTestEnv(t)
	t.Cleanup(func() {
		if env.db != nil {
			_ = env.db.Close()
		}
	})
	up := newFakeUploader()
	up.failName = "S01E02"
	env.service.uploader = up
	writeSTRM(t, strmDir, "a/S01E01.strm", "http://a/1")
	writeSTRM(t, strmDir, "a/S01E02.strm", "http://a/2")
	writeSTRM(t, strmDir, "b/S01E01.strm", "http://a/3")

	record, err := env.service.Create(env.ctx, CreateRequest{Note: "full", Password: testPassword, IncludeAccounts: true})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := env.service.PrepareRestore(env.ctx, record.ID, RestoreRequest{
		Password:   testPassword,
		STRMTarget: &RestoreTarget{AccountID: 7, ParentID: "cloud-root"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.STRMFailures) != 1 || !strings.Contains(summary.STRMFailures[0], "S01E02") {
		t.Fatalf("failures = %v", summary.STRMFailures)
	}
	if len(up.paths()) != 2 {
		t.Fatalf("其余文件不该被牵连：%v", up.paths())
	}
}

func TestCloudRestoreWithoutUploaderIsRejectedLoudly(t *testing.T) {
	env, strmDir := newSTRMTestEnv(t)
	t.Cleanup(func() {
		if env.db != nil {
			_ = env.db.Close()
		}
	})
	env.service.uploader = nil
	writeSTRM(t, strmDir, "a.strm", "http://a/1")
	record, err := env.service.Create(env.ctx, CreateRequest{Note: "full", Password: testPassword, IncludeAccounts: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.service.PrepareRestore(env.ctx, record.ID, RestoreRequest{
		Password:   testPassword,
		STRMTarget: &RestoreTarget{AccountID: 7, ParentID: "cloud-root"},
	}); err == nil {
		t.Fatal("没配上传能力时应当报错，而不是静默什么都不做")
	}
}

func TestIncompleteCloudTargetFallsBackToLocal(t *testing.T) {
	env, strmDir := newSTRMTestEnv(t)
	t.Cleanup(func() {
		if env.db != nil {
			_ = env.db.Close()
		}
	})
	writeSTRM(t, strmDir, "a.strm", "http://a/1")
	record, err := env.service.Create(env.ctx, CreateRequest{Note: "full", Password: testPassword, IncludeAccounts: true})
	if err != nil {
		t.Fatal(err)
	}
	// 选了网盘但没给目标目录 —— 宁可落本地，也不要「什么都不恢复」。
	summary, err := env.service.PrepareRestore(env.ctx, record.ID, RestoreRequest{
		Password:   testPassword,
		STRMTarget: &RestoreTarget{AccountID: 7},
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.STRMTarget != "local" || summary.STRMCount != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if body := readSTRM(t, strmDir)["a.strm"]; body != "http://a/1" {
		t.Fatalf("a.strm = %q", body)
	}
}

func TestBackupArchiveRejectsSTRMPathEscape(t *testing.T) {
	// 恶意备份里塞一个逃出 strm/ 前缀的条目，恢复必须拒绝而不是写出去。
	zipBytes := buildEvilSTRMZip(t)
	if _, err := strmFilesFromArchive(zipBytes); err == nil {
		t.Fatal("带 ../ 的条目竟然被接受了")
	}
	zipBytes = buildNonSTRMZip(t)
	if _, err := strmFilesFromArchive(zipBytes); err == nil {
		t.Fatal("非 .strm 条目竟然被接受了")
	}
}

func TestSTRMSourceCountIgnoresNonSTRMFiles(t *testing.T) {
	dir := t.TempDir()
	writeSTRM(t, dir, "a.strm", "x")
	writeSTRM(t, dir, "sub/b.strm", "x")
	writeSTRM(t, dir, "sub/cover.jpg", "x")
	writeSTRM(t, dir, "sub/notes.txt", "x")
	if got := strmSourceCount(dir); got != 2 {
		t.Fatalf("strmSourceCount = %d，期望 2", got)
	}
}

func TestSTRMArchiveRoundTripKeepsRelativePaths(t *testing.T) {
	dir := t.TempDir()
	writeSTRM(t, dir, "剧集/某剧/Season 01/S01E01.strm", "http://a/1")
	data, count, ok, err := buildSTRMArchive(dir)
	if err != nil || !ok || count != 1 {
		t.Fatalf("build = %v %d %v %v", data, count, ok, err)
	}
	files, err := strmFilesFromArchive(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files["剧集/某剧/Season 01/S01E01.strm"] == nil {
		t.Fatalf("files = %v", files)
	}
	if string(files["剧集/某剧/Season 01/S01E01.strm"]) != "http://a/1" {
		t.Fatalf("内容不对：%q", files["剧集/某剧/Season 01/S01E01.strm"])
	}
}

func TestMissingSTRMDirIsNotAnError(t *testing.T) {
	if _, _, ok, err := buildSTRMArchive(filepath.Join(t.TempDir(), "nope")); ok || err != nil {
		t.Fatalf("ok = %v err = %v", ok, err)
	}
	if _, _, ok, err := buildSTRMArchive(""); ok || err != nil {
		t.Fatalf("ok = %v err = %v", ok, err)
	}
}

func TestRestoreSTRMFilesRollsBackOnFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "strm")
	writeSTRM(t, dir, "old.strm", "old")
	// 一个注定写不进去的文件名（路径段过长）会让整次写入失败。
	err := restoreSTRMFiles(dir, map[string][]byte{
		"good.strm":                        []byte("good"),
		strings.Repeat("x", 300) + ".strm": []byte("bad"),
	})
	if err == nil {
		t.Fatal("非法文件名应当失败")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "old.strm")); statErr != nil {
		t.Fatalf("失败后旧目录没被挪回来：%v", statErr)
	}
}

// buildEvilSTRMZip 造一个条目名带 `../` 的 STRM 包。
func buildEvilSTRMZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	entry, err := zw.Create("strm/../escape.strm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildNonSTRMZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	entry, err := zw.Create("strm/movie.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
