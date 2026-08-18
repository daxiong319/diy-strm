package controllers

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"diy-strm/internal/helpers"
	"diy-strm/internal/logstream"
	"diy-strm/internal/realtime"

	"github.com/gin-gonic/gin"
)

func TestLogStreamEmitsAppendedLogEntry(t *testing.T) {
	oldLifecycle := realtime.GlobalLifecycle
	oldLogManager := logstream.GlobalManager
	oldConfigDir := helpers.ConfigDir
	realtime.GlobalLifecycle = realtime.NewLifecycle()
	logstream.GlobalManager = logstream.NewManager()
	helpers.ConfigDir = t.TempDir()
	t.Cleanup(func() {
		realtime.GlobalLifecycle = oldLifecycle
		logstream.GlobalManager = oldLogManager
		helpers.ConfigDir = oldConfigDir
	})

	logsDir := filepath.Join(helpers.ConfigDir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("创建测试日志目录失败: %v", err)
	}
	fullLogPath := filepath.Join(logsDir, "app.log")
	if err := os.WriteFile(fullLogPath, []byte("2026/07/18 10:00:00.000000 [INFO] existing\n"), 0o644); err != nil {
		t.Fatalf("写入初始日志失败: %v", err)
	}

	router := gin.New()
	router.GET("/logs/stream", LogStream)
	server := httptest.NewServer(router)
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/logs/stream?path=app.log")
	if err != nil {
		t.Fatalf("建立日志 SSE 请求失败: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("日志 SSE status = %d，期望 %d", response.StatusCode, http.StatusOK)
	}

	reader := bufio.NewReader(response.Body)
	connected := readSSEFrame(t, reader)
	if !strings.Contains(connected, ": connected") {
		t.Fatalf("首帧应为 connected 注释，frame = %q", connected)
	}

	if file, err := os.OpenFile(fullLogPath, os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
		t.Fatalf("打开测试日志失败: %v", err)
	} else {
		if _, err := file.WriteString("2026/07/18 10:00:01.000000 [INFO] appended\n"); err != nil {
			_ = file.Close()
			t.Fatalf("追加测试日志失败: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("关闭测试日志失败: %v", err)
		}
	}

	frame := readSSEFrame(t, reader)
	if !strings.Contains(frame, "event:log_append") || !strings.Contains(frame, `"message":"appended"`) {
		t.Fatalf("未收到追加日志 SSE 事件，frame = %q", frame)
	}
}

func TestLogStreamEmitsResyncAfterLogTruncation(t *testing.T) {
	oldLifecycle := realtime.GlobalLifecycle
	oldLogManager := logstream.GlobalManager
	oldConfigDir := helpers.ConfigDir
	realtime.GlobalLifecycle = realtime.NewLifecycle()
	logstream.GlobalManager = logstream.NewManager()
	helpers.ConfigDir = t.TempDir()
	t.Cleanup(func() {
		realtime.GlobalLifecycle = oldLifecycle
		logstream.GlobalManager = oldLogManager
		helpers.ConfigDir = oldConfigDir
	})

	logsDir := filepath.Join(helpers.ConfigDir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("创建测试日志目录失败: %v", err)
	}
	fullLogPath := filepath.Join(logsDir, "app.log")
	if err := os.WriteFile(fullLogPath, []byte("2026/07/18 10:00:00.000000 [INFO] existing\n"), 0o644); err != nil {
		t.Fatalf("写入初始日志失败: %v", err)
	}

	router := gin.New()
	router.GET("/logs/stream", LogStream)
	server := httptest.NewServer(router)
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/logs/stream?path=app.log")
	if err != nil {
		t.Fatalf("建立日志 SSE 请求失败: %v", err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	_ = readSSEFrame(t, reader)

	if err := os.WriteFile(fullLogPath, nil, 0o644); err != nil {
		t.Fatalf("截断测试日志失败: %v", err)
	}
	frame := readSSEFrame(t, reader)
	if !strings.Contains(frame, "event:resync_required") || !strings.Contains(frame, "log_file_truncated") {
		t.Fatalf("截断日志后未收到 resync_required，frame = %q", frame)
	}
}

func TestListLogFiles(t *testing.T) {
	oldLogManager := logstream.GlobalManager
	oldConfigDir := helpers.ConfigDir
	logstream.GlobalManager = logstream.NewManager()
	helpers.ConfigDir = t.TempDir()
	t.Cleanup(func() {
		logstream.GlobalManager = oldLogManager
		helpers.ConfigDir = oldConfigDir
	})

	logsDir := filepath.Join(helpers.ConfigDir, "logs")
	for _, dir := range []string{logsDir, filepath.Join(logsDir, "sync"), filepath.Join(logsDir, "libs")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("创建测试日志目录失败: %v", err)
		}
	}
	files := map[string]string{
		"app.log":          "2026/07/18 10:00:00.000000 [INFO] app\n",
		"115.log":          "2026/07/18 10:00:00.000000 [INFO] 115\n",
		"console_run1.log": "2026/07/18 10:00:00.000000 [INFO] debug leftover\n",
		filepath.Join("sync", "sync_1.log"): "2026/07/18 10:00:00.000000 [INFO] sync1\n",
		filepath.Join("libs", "sync_2.log"): "2026/07/18 10:00:00.000000 [INFO] sync2\n",
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(logsDir, rel), []byte(content), 0o644); err != nil {
			t.Fatalf("写入测试日志 %s 失败: %v", rel, err)
		}
	}

	router := gin.New()
	router.GET("/logs/files", ListLogFiles)
	server := httptest.NewServer(router)
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/logs/files")
	if err != nil {
		t.Fatalf("请求日志文件清单失败: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("日志文件清单 status = %d，期望 %d", response.StatusCode, http.StatusOK)
	}
	var body struct {
		Files []LogFileInfo `json:"files"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("解析日志文件清单失败: %v", err)
	}

	got := make([]string, 0, len(body.Files))
	wantOrder := []string{"115.log", "app.log", "libs/sync_2.log", "sync/sync_1.log"}
	for _, f := range body.Files {
		got = append(got, f.Path)
	}
	if len(got) != len(wantOrder) {
		t.Fatalf("清单位置数量 = %d (%v)，期望 %d (%v)", len(got), got, len(wantOrder), wantOrder)
	}
	for i, want := range wantOrder {
		if got[i] != want {
			t.Fatalf("清单第 %d 项 = %q，期望 %q（完整清单 %v）", i, got[i], want, got)
		}
	}
	for _, f := range body.Files {
		if f.Path == "console_run1.log" {
			t.Fatalf("清单不应包含调试日志 console_run1.log")
		}
		if f.Size == 0 {
			t.Fatalf("条目 %s 不应为空文件元信息", f.Path)
		}
	}
}

func TestClearLogFile(t *testing.T) {
	oldLogManager := logstream.GlobalManager
	oldConfigDir := helpers.ConfigDir
	logstream.GlobalManager = logstream.NewManager()
	helpers.ConfigDir = t.TempDir()
	t.Cleanup(func() {
		logstream.GlobalManager = oldLogManager
		helpers.ConfigDir = oldConfigDir
	})

	logsDir := filepath.Join(helpers.ConfigDir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("创建测试日志目录失败: %v", err)
	}
	fullLogPath := filepath.Join(logsDir, "app.log")
	if err := os.WriteFile(fullLogPath, []byte("2026/07/18 10:00:00.000000 [INFO] a\n2026/07/18 10:00:01.000000 [ERROR] b\n"), 0o644); err != nil {
		t.Fatalf("写入测试日志失败: %v", err)
	}

	router := gin.New()
	router.POST("/logs/clear", ClearLogFile)
	server := httptest.NewServer(router)
	defer server.Close()

	response, err := server.Client().Post(server.URL+"/logs/clear?path=app.log", "application/json", nil)
	if err != nil {
		t.Fatalf("请求清空日志失败: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("清空日志 status = %d，期望 %d", response.StatusCode, http.StatusOK)
	}
	var body struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("解析清空日志响应失败: %v", err)
	}
	if !body.Success {
		t.Fatalf("清空日志应返回 success=true")
	}
	stat, err := os.Stat(fullLogPath)
	if err != nil {
		t.Fatalf("清空后日志文件应存在: %v", err)
	}
	if stat.Size() != 0 {
		t.Fatalf("清空后日志文件大小 = %d，期望 0", stat.Size())
	}

	// 非法路径应被拒绝
	badResp, err := server.Client().Post(server.URL+"/logs/clear?path=../app.log", "application/json", nil)
	if err != nil {
		t.Fatalf("非法路径请求失败: %v", err)
	}
	defer badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法路径 status = %d，期望 %d", badResp.StatusCode, http.StatusBadRequest)
	}

	// 不存在的文件应返回 404
	missingResp, err := server.Client().Post(server.URL+"/logs/clear?path=missing.log", "application/json", nil)
	if err != nil {
		t.Fatalf("不存在文件请求失败: %v", err)
	}
	defer missingResp.Body.Close()
	if missingResp.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在文件 status = %d，期望 %d", missingResp.StatusCode, http.StatusNotFound)
	}
}

func readSSEFrame(t *testing.T, reader *bufio.Reader) string {
	t.Helper()

	type result struct {
		frame string
		err   error
	}
	resultCh := make(chan result, 1)
	go func() {
		var frame strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				resultCh <- result{err: err}
				return
			}
			frame.WriteString(line)
			if line == "\n" {
				resultCh <- result{frame: frame.String()}
				return
			}
		}
	}()

	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("读取 SSE 帧失败: %v", result.err)
		}
		return result.frame
	case <-time.After(3 * time.Second):
		t.Fatal("读取 SSE 帧超时")
		return ""
	}
}
