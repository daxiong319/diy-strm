package casintake

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"litepan/internal/cas"
	"litepan/internal/domain"
)

// handleDocument 集成测试：httptest 假 Telegram（getFile → file 下载），
// 绑定 cas 钩子，断言 收到→下载→自动转存→失败通知 全链路。

const testCasContent = "{\n  \"version\": 2,\n  \"fileName\": \"movie.mkv\",\n  \"fileSize\": 1,\n  \"hashes\": {\"fileMd5\": \"bee46317da4862449b09e107d3a95c23\"},\n  \"sourceDrive\": \"123_open\"\n}"

type hookState struct {
	mu      sync.Mutex
	saved   []cas.AutoSaveSourceFile
	saveDir string
}

func TestHandleDocumentDownloadsAndAutoSaves(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/getFile"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "result": map[string]any{"file_path": "documents/test-e03.cas"},
			})
		case strings.HasPrefix(r.URL.Path, "/file/bot"):
			_, _ = w.Write([]byte(testCasContent))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	state := &hookState{}
	cas.BindAutoSaveSaver(func(ctx context.Context, accountID int64, dir string, file cas.AutoSaveSourceFile) (string, error) {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.saved = append(state.saved, file)
		state.saveDir = dir
		return "file-123", nil
	})
	cas.BindAutoSaveAccounts(func(ctx context.Context) ([]*domain.Account, error) {
		return []*domain.Account{{ID: 3, DriverType: "123_open", IsActive: true}}, nil
	})
	cas.BindAutoSaveNotifier(func(ctx context.Context, level, category, title, message string, accountID, refID int64) {})
	log := newTestLogger()
	handleDocument(context.Background(), ts.Client(), ts.URL, "test-token", 1,
		&tgDocument{FileID: "doc-1", FileName: "test-e03.cas", FileSize: int64(len(testCasContent))}, log)

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.saved) != 1 {
		t.Fatalf("应转存 1 个文件，实际 %d", len(state.saved))
	}
	if state.saved[0].FileName != "test-e03.cas" {
		t.Errorf("转存文件名应为 test-e03.cas，实际 %s", state.saved[0].FileName)
	}
	if !strings.Contains(state.saved[0].Content, "bee46317da4862449b09e107d3a95c23") {
		t.Error("转存内容应包含清单 md5")
	}
	if state.saveDir != "CAS" {
		t.Errorf("默认保存目录应为 CAS，实际 %s", state.saveDir)
	}
}

func TestHandleDocumentNotifyFailureOnBadManifest(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/getFile"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "result": map[string]any{"file_path": "documents/bad.cas"},
			})
		case strings.HasPrefix(r.URL.Path, "/file/bot"):
			_, _ = w.Write([]byte("不是清单内容"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	cas.BindAutoSaveSaver(func(ctx context.Context, accountID int64, dir string, file cas.AutoSaveSourceFile) (string, error) {
		t.Error("坏清单不应触发转存")
		return "", nil
	})
	cas.BindAutoSaveAccounts(func(ctx context.Context) ([]*domain.Account, error) {
		return []*domain.Account{{ID: 3, DriverType: "123_open", IsActive: true}}, nil
	})
	var notified int
	cas.BindAutoSaveNotifier(func(ctx context.Context, level, category, title, message string, accountID, refID int64) {
		notified++
		if !strings.Contains(message, "解析 .cas 失败") {
			t.Errorf("通知应说明解析失败，实际：%s", message)
		}
	})
	handleDocument(context.Background(), ts.Client(), ts.URL, "test-token", 1,
		&tgDocument{FileID: "doc-2", FileName: "bad.cas"}, newTestLogger())

	if notified == 0 {
		t.Fatal("解析失败应发出用户通知")
	}
}

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestHandleDocumentExtractsCasFromArchive 压缩包投递的端到端验证：
// 假 Telegram 返回一个 zip → handleDocument 解压 → 取出 .cas → 自动转存。
// 这是用户报告的「打包发压缩包没反应」的直接回归测试。
func TestHandleDocumentExtractsCasFromArchive(t *testing.T) {
	zipData := makeZip(t, map[string]string{
		"喜欢高兴爱 (2026)/": "",
		"喜欢高兴爱 (2026)/喜欢高兴爱.Love.My.Way.2026.2160p.WEB-DL.AAC.H264-HDSWEB.mkv.cas": testCasContent,
	})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/getFile"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "result": map[string]any{"file_path": "documents/pack.zip"},
			})
		case strings.HasPrefix(r.URL.Path, "/file/bot"):
			_, _ = w.Write(zipData)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	state := &hookState{}
	cas.BindAutoSaveSaver(func(ctx context.Context, accountID int64, dir string, file cas.AutoSaveSourceFile) (string, error) {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.saved = append(state.saved, file)
		state.saveDir = dir
		return "file-zip", nil
	})
	cas.BindAutoSaveAccounts(func(ctx context.Context) ([]*domain.Account, error) {
		return []*domain.Account{{ID: 3, DriverType: "123_open", IsActive: true}}, nil
	})
	cas.BindAutoSaveNotifier(func(ctx context.Context, level, category, title, message string, accountID, refID int64) {})

	handleDocument(context.Background(), ts.Client(), ts.URL, "test-token", 1,
		&tgDocument{FileID: "doc-zip", FileName: "喜欢高兴爱 (2026).zip", FileSize: int64(len(zipData))},
		newTestLogger())

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.saved) != 1 {
		t.Fatalf("压缩包内 1 个 .cas 应转存 1 个文件，实际 %d", len(state.saved))
	}
	// 保存名必须是清单名（.cas 结尾），而不是压缩包名。
	if !strings.HasSuffix(state.saved[0].FileName, ".cas") {
		t.Errorf("转存文件名应以 .cas 结尾，实际 %s", state.saved[0].FileName)
	}
	if !strings.Contains(state.saved[0].Content, "bee46317da4862449b09e107d3a95c23") {
		t.Error("转存内容应包含清单 md5")
	}
}

// TestHandleDocumentArchiveWithoutCasNotifies 压缩包里没有 .cas 时要通知用户，
// 而不是静默当成功。
func TestHandleDocumentArchiveWithoutCasNotifies(t *testing.T) {
	zipData := makeZip(t, map[string]string{"readme.txt": "没有清单"})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/getFile"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "result": map[string]any{"file_path": "documents/empty.zip"},
			})
		case strings.HasPrefix(r.URL.Path, "/file/bot"):
			_, _ = w.Write(zipData)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	cas.BindAutoSaveSaver(func(ctx context.Context, accountID int64, dir string, file cas.AutoSaveSourceFile) (string, error) {
		t.Error("没有 .cas 时不应触发转存")
		return "", nil
	})
	cas.BindAutoSaveAccounts(func(ctx context.Context) ([]*domain.Account, error) {
		return []*domain.Account{{ID: 3, DriverType: "123_open", IsActive: true}}, nil
	})
	var notified string
	cas.BindAutoSaveNotifier(func(ctx context.Context, level, category, title, message string, accountID, refID int64) {
		notified = message
	})

	handleDocument(context.Background(), ts.Client(), ts.URL, "test-token", 1,
		&tgDocument{FileID: "doc-empty", FileName: "empty.zip", FileSize: int64(len(zipData))},
		newTestLogger())

	if !strings.Contains(notified, "没有找到 .cas") {
		t.Errorf("应通知用户压缩包内无 .cas，实际通知：%q", notified)
	}
}

// TestHandleDocumentUnsupportedArchiveNotifies 不支持的压缩格式要给出明确提示。
func TestHandleDocumentUnsupportedArchiveNotifies(t *testing.T) {
	cas.BindAutoSaveSaver(func(ctx context.Context, accountID int64, dir string, file cas.AutoSaveSourceFile) (string, error) {
		t.Error("不支持的格式不应触发转存")
		return "", nil
	})
	cas.BindAutoSaveAccounts(func(ctx context.Context) ([]*domain.Account, error) {
		return nil, nil
	})
	var notified string
	cas.BindAutoSaveNotifier(func(ctx context.Context, level, category, title, message string, accountID, refID int64) {
		notified = message
	})

	// 不应发起任何网络请求：格式在下载前就被拒绝。
	handleDocument(context.Background(), http.DefaultClient, "http://127.0.0.1:1", "tok", 1,
		&tgDocument{FileID: "doc-7z", FileName: "影片.7z"}, newTestLogger())

	if !strings.Contains(notified, "不受支持") {
		t.Errorf("应通知用户格式不受支持，实际通知：%q", notified)
	}
}

// captureLogger 把日志写到内存，便于断言日志字段。
func captureLogger() (*slog.Logger, *strings.Builder) {
	buf := &strings.Builder{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})), buf
}

// 用户抱怨「日志只显示 自动转存成功 / 账号 2」，
// 看不出转存了什么文件、到哪个网盘、哪个目录。这条测试钉住日志必须三样齐全。
func TestAutoSaveLogCarriesFileDriveAndDir(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/getFile"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "result": map[string]any{"file_path": "documents/x.cas"},
			})
		case strings.HasPrefix(r.URL.Path, "/file/bot"):
			_, _ = w.Write([]byte(testCasContent))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	cas.BindAutoSaveSaver(func(context.Context, int64, string, cas.AutoSaveSourceFile) (string, error) {
		return "file-1", nil
	})
	cas.BindAutoSaveAccounts(func(context.Context) ([]*domain.Account, error) {
		return []*domain.Account{{ID: 2, DriverType: "123_open", IsActive: true}}, nil
	})
	cas.BindAutoSaveNotifier(func(context.Context, string, string, string, string, int64, int64) {})
	log, buf := captureLogger()

	handleDocument(context.Background(), ts.Client(), ts.URL, "test-token", 1,
		&tgDocument{FileID: "doc-1", FileName: "movie.cas", FileSize: int64(len(testCasContent))}, log)

	out := buf.String()
	for _, want := range []string{
		"CAS 接收：自动转存成功",
		"file_name=movie.cas", // 转存了什么文件
		"drive=\"123 网盘\"",    // 转到哪个网盘（中文展示名）
		"save_dir=/CAS",       // 转到哪个目录
		"account_id=2",        // 哪个账号
	} {
		if !strings.Contains(out, want) {
			t.Errorf("日志缺少 %q\n实际日志：%s", want, out)
		}
	}
}

// 失败路径同样要能看出文件/网盘/目录，否则用户不知道失败的是哪一个。
func TestAutoSaveFailureLogCarriesFileDriveAndDir(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/getFile"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "result": map[string]any{"file_path": "documents/x.cas"},
			})
		case strings.HasPrefix(r.URL.Path, "/file/bot"):
			_, _ = w.Write([]byte(testCasContent))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	cas.BindAutoSaveSaver(func(context.Context, int64, string, cas.AutoSaveSourceFile) (string, error) {
		return "", errors.New("认证服务暂时不可用")
	})
	cas.BindAutoSaveAccounts(func(context.Context) ([]*domain.Account, error) {
		return []*domain.Account{{ID: 2, DriverType: "123_open", IsActive: true}}, nil
	})
	log, buf := captureLogger()

	handleDocument(context.Background(), ts.Client(), ts.URL, "test-token", 1,
		&tgDocument{FileID: "doc-1", FileName: "movie.cas", FileSize: int64(len(testCasContent))}, log)

	out := buf.String()
	for _, want := range []string{
		"CAS 接收：自动转存失败",
		"file_name=movie.cas",
		"drive=\"123 网盘\"",
		"save_dir=/CAS",
		"认证服务暂时不可用",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("日志缺少 %q\n实际日志：%s", want, out)
		}
	}
}
