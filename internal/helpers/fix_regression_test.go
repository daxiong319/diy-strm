package helpers

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 测试基建：静默 logger（DownloadFile 失败路径会调 AppLogger）
func setupDownloadTestLogger(t *testing.T) {
	t.Helper()
	old := AppLogger
	AppLogger = &QLogger{Logger: log.New(&bytes.Buffer{}, "", 0)}
	t.Cleanup(func() { AppLogger = old })
}

// 回归（三批修复）：DownloadFile 流式化——大文件不再 io.ReadAll 全量入内存，
// 通过临时文件 .downloading + rename 落盘；成功后临时文件不存在、内容完整。
func TestDownloadFileStreamsLargeBodyAndCleansTemp(t *testing.T) {
	setupDownloadTestLogger(t)

	payload := bytes.Repeat([]byte("A0123456789"), 64*1024) // 640KB，覆盖多轮 io.Copy
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	target := filepath.Join(t.TempDir(), "video.mkv")
	if err := DownloadFile(srv.URL+"/f.mkv", target, "test-ua"); err != nil {
		t.Fatalf("DownloadFile 失败：%v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取下载文件失败：%v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("下载内容不完整：期望 %d 字节，实际 %d 字节", len(payload), len(got))
	}
	if _, err := os.Stat(target + ".downloading"); !os.IsNotExist(err) {
		t.Fatalf("成功后不应残留 .downloading 临时文件：%s", target+".downloading")
	}
}

// 回归：服务端提前断流（Content-Length 大于实际写出）→ 返回错误且不落半截成品文件，
// .downloading 临时文件同步清理（原 io.ReadAll 实现失败时不落任何文件，流式版必须保持该语义）。
func TestDownloadFileTruncatedBodyLeavesNoPartialFile(t *testing.T) {
	setupDownloadTestLogger(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100000")
		_, _ = w.Write(bytes.Repeat([]byte("X"), 100)) // 只写 100 字节就返回 → 客户端 unexpected EOF
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "broken.mkv")
	err := DownloadFile(srv.URL+"/f.mkv", target, "test-ua")
	if err == nil {
		t.Fatal("断流下载应返回错误")
	}
	if !strings.Contains(err.Error(), "下载") {
		t.Fatalf("错误应来自流式拷贝阶段，实际：%v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("断流不应落半截成品文件：%s", target)
	}
	if _, statErr := os.Stat(target + ".downloading"); !os.IsNotExist(statErr) {
		t.Fatalf("断流后 .downloading 临时文件应被清理：%s", target+".downloading")
	}
}

// 回归：HTTP 错误状态码路径保持原语义（错误返回 + 不产生任何文件）。
func TestDownloadFileHTTPErrorNoFile(t *testing.T) {
	setupDownloadTestLogger(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	target := filepath.Join(t.TempDir(), "denied.mkv")
	if err := DownloadFile(srv.URL+"/f.mkv", target, "test-ua"); err == nil {
		t.Fatal("403 下载应返回错误")
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("失败下载不应产生文件：%s", target)
	}
}
