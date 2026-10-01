package casintake

import (
	"context"
	"encoding/json"
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
