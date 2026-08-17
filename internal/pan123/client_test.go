package pan123

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestClient 创建指向 mock 服务器的客户端
func newTestClient(t *testing.T, loginHandler, mainHandler http.HandlerFunc) (*Client, *httptest.Server, *httptest.Server) {
	t.Helper()
	loginServer := httptest.NewServer(loginHandler)
	mainServer := httptest.NewServer(mainHandler)
	client := NewClient(1, "test@example.com", "password123")
	client.SetBaseURL(loginServer.URL, mainServer.URL)
	t.Cleanup(func() {
		loginServer.Close()
		mainServer.Close()
		client.Close()
	})
	return client, loginServer, mainServer
}

// writeJSON 写 JSON 响应
func writeJSON(w http.ResponseWriter, code int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(data)
}

func TestSignPathFormat(t *testing.T) {
	k, v := signPath("/b/api/file/list/new", "web", "3")
	// key 应为 10 位以内的数字字符串（crc32）
	if k == "" {
		t.Error("sign key 不应为空")
	}
	for _, c := range k {
		if c < '0' || c > '9' {
			t.Errorf("sign key 应为数字：%s", k)
			break
		}
	}
	// value 格式：timestamp-random-dataSign
	parts := strings.Split(v, "-")
	if len(parts) != 3 {
		t.Fatalf("sign value 格式错误：%s，应为 timestamp-random-dataSign", v)
	}
	for _, p := range parts {
		if p == "" {
			t.Fatalf("sign value 包含空部分：%s", v)
		}
	}
}

func TestGetApiAddsSignQuery(t *testing.T) {
	raw := "https://yun.123pan.com/b/api/file/list/new"
	signed := GetApi(raw)
	if !strings.HasPrefix(signed, raw+"?") {
		t.Errorf("签名 URL 格式错误：%s", signed)
	}
	// 签名 key 为数字
	parts := strings.SplitN(signed, "?", 2)
	query := parts[1]
	kv := strings.SplitN(query, "=", 2)
	if len(kv) != 2 {
		t.Fatalf("签名参数格式错误：%s", query)
	}
	if kv[0] == "" || kv[1] == "" {
		t.Errorf("签名参数不应为空：%s", query)
	}
}

func TestLoginEmail(t *testing.T) {
	var gotBody map[string]interface{}
	client, _, _ := newTestClient(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/user/sign_in" {
				t.Errorf("登录路径错误：%s", r.URL.Path)
			}
			if r.Header.Get("platform") != "web" || r.Header.Get("app-version") != "3" {
				t.Errorf("登录请求头缺少 platform/app-version")
			}
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			writeJSON(w, 200, map[string]interface{}{
				"code":    200,
				"message": "success",
				"data":    map[string]interface{}{"token": "mock-token-abc"},
			})
		},
		func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("不应访问主 API：%s", r.URL.Path)
		},
	)

	if err := client.Login(context.Background()); err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	if client.GetAccessToken() != "mock-token-abc" {
		t.Errorf("令牌未保存：%s", client.GetAccessToken())
	}
	if gotBody["mail"] != "test@example.com" {
		t.Errorf("邮箱登录体错误：%v", gotBody)
	}
	if gotBody["type"] != float64(2) {
		t.Errorf("邮箱登录 type 应为 2：%v", gotBody)
	}
}

func TestLoginPhone(t *testing.T) {
	var gotBody map[string]interface{}
	client, _, _ := newTestClient(t,
		func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			writeJSON(w, 200, map[string]interface{}{
				"code":    200,
				"message": "success",
				"data":    map[string]interface{}{"token": "mock-token-phone"},
			})
		},
		nil,
	)
	client.username = "13800138000"

	if err := client.Login(context.Background()); err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	if gotBody["passport"] != "13800138000" {
		t.Errorf("手机号登录体错误：%v", gotBody)
	}
	if gotBody["remember"] != true {
		t.Errorf("手机号登录 remember 应为 true：%v", gotBody)
	}
}

func TestLoginFailed(t *testing.T) {
	client, _, _ := newTestClient(t,
		func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]interface{}{
				"code":    400,
				"message": "用户名或密码错误",
			})
		},
		nil,
	)

	err := client.Login(context.Background())
	if err == nil {
		t.Fatal("登录应失败")
	}
	if !strings.Contains(err.Error(), "用户名或密码错误") {
		t.Errorf("错误信息不正确：%v", err)
	}
}

func TestRequestAutoReloginOn401(t *testing.T) {
	loginCount := 0
	mainCount := 0
	client, _, _ := newTestClient(t,
		func(w http.ResponseWriter, r *http.Request) {
			loginCount++
			writeJSON(w, 200, map[string]interface{}{
				"code":    200,
				"message": "success",
				"data":    map[string]interface{}{"token": "token-after-relogin"},
			})
		},
		func(w http.ResponseWriter, r *http.Request) {
			mainCount++
			if mainCount == 1 {
				// 第一次返回 401，触发重新登录
				writeJSON(w, 200, map[string]interface{}{"code": 401, "message": "token expired"})
				return
			}
			// 第二次应带新令牌并成功
			auth := r.Header.Get("authorization")
			if auth != "Bearer token-after-relogin" {
				t.Errorf("重试请求未使用新令牌：%s", auth)
			}
			writeJSON(w, 200, map[string]interface{}{"code": 0, "message": "success", "data": map[string]interface{}{}})
		},
	)
	client.SetAccessToken("expired-token")

	if _, err := client.Request(context.Background(), client.api("/user/info"), http.MethodGet, nil); err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	if loginCount != 1 {
		t.Errorf("应自动登录 1 次，实际 %d 次", loginCount)
	}
	if mainCount != 2 {
		t.Errorf("主 API 应请求 2 次，实际 %d 次", mainCount)
	}
}

func TestRequestBusinessError(t *testing.T) {
	client, _, _ := newTestClient(t, nil,
		func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]interface{}{"code": 400, "message": "参数错误"})
		},
	)
	client.SetAccessToken("token")

	_, err := client.Request(context.Background(), client.api("/user/info"), http.MethodGet, nil)
	if err == nil || !strings.Contains(err.Error(), "参数错误") {
		t.Errorf("业务错误处理不正确：%v", err)
	}
}

func TestListFiles(t *testing.T) {
	client, _, _ := newTestClient(t, nil,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/file/list/new" {
				t.Errorf("路径错误：%s", r.URL.Path)
			}
			if r.Header.Get("authorization") != "Bearer test-token" {
				t.Errorf("鉴权头错误：%s", r.Header.Get("authorization"))
			}
			// 验证签名参数存在
			q := r.URL.Query()
			hasSign := false
			for _, v := range q {
				if len(v) > 0 && strings.Contains(v[0], "-") && strings.Count(v[0], "-") == 2 {
					hasSign = true
				}
			}
			if !hasSign {
				t.Errorf("请求缺少签名参数：%s", r.URL.RawQuery)
			}
			if q.Get("parentFileId") != "123" || q.Get("Page") != "1" {
				t.Errorf("查询参数错误：%s", r.URL.RawQuery)
			}
			writeJSON(w, 200, map[string]interface{}{
				"code":    0,
				"message": "success",
				"data": map[string]interface{}{
					"Next":  "-1",
					"Total": 2,
					"InfoList": []map[string]interface{}{
						{"FileName": "movie.mkv", "Size": 1024, "FileId": 1001, "Type": 0, "Etag": "abc"},
						{"FileName": "电影", "Size": 0, "FileId": 1002, "Type": 1},
					},
				},
			})
		},
	)
	client.SetAccessToken("test-token")

	files, err := client.ListFiles(context.Background(), "123", 1)
	if err != nil {
		t.Fatalf("获取文件列表失败：%v", err)
	}
	if len(files.Data.InfoList) != 2 {
		t.Fatalf("文件数量错误：%d", len(files.Data.InfoList))
	}
	if !files.Data.InfoList[0].IsDir() && files.Data.InfoList[0].FileName != "movie.mkv" {
		t.Errorf("文件解析错误：%+v", files.Data.InfoList[0])
	}
	if !files.Data.InfoList[1].IsDir() {
		t.Errorf("目录解析错误：%+v", files.Data.InfoList[1])
	}
}

func TestGetFilesPagination(t *testing.T) {
	pageCount := 0
	client, _, _ := newTestClient(t, nil,
		func(w http.ResponseWriter, r *http.Request) {
			pageCount++
			page := r.URL.Query().Get("Page")
			switch page {
			case "1":
				writeJSON(w, 200, map[string]interface{}{
					"code": 0, "message": "success",
					"data": map[string]interface{}{
						"Next": "2", "Total": 2,
						"InfoList": []map[string]interface{}{
							{"FileName": "a.mkv", "Size": 1, "FileId": 1, "Type": 0},
						},
					},
				})
			case "2":
				writeJSON(w, 200, map[string]interface{}{
					"code": 0, "message": "success",
					"data": map[string]interface{}{
						"Next": "-1", "Total": 2,
						"InfoList": []map[string]interface{}{
							{"FileName": "b.mkv", "Size": 2, "FileId": 2, "Type": 0},
						},
					},
				})
			default:
				t.Errorf("超出预期页码：%s", page)
			}
		},
	)
	client.SetAccessToken("token")

	files, err := client.GetFiles(context.Background(), "0")
	if err != nil {
		t.Fatalf("获取文件失败：%v", err)
	}
	if len(files) != 2 {
		t.Errorf("分页获取文件数量错误：%d", len(files))
	}
	if pageCount != 2 {
		t.Errorf("应请求 2 页，实际 %d 页", pageCount)
	}
}

func TestResolveDownloadURLWithParams(t *testing.T) {
	// 模拟 CDN 302 重定向
	cdnServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.String() != "/file.mp4?sign=xyz" {
			t.Errorf("CDN 请求 URL 错误：%s", r.URL.String())
		}
		if r.Header.Get("referer") != "https://yun.123pan.com/" {
			t.Errorf("CDN 请求缺少 Referer")
		}
		w.Header().Set("Location", "https://real-cdn.example.com/final.mp4")
		w.WriteHeader(http.StatusFound)
	}))
	defer cdnServer.Close()

	client := NewClient(1, "test@example.com", "pwd")
	client.SetBaseURL("http://localhost:1", "http://localhost:1")
	defer client.Close()

	// params 解码后的 URL 指向 mock CDN
	realURL := cdnServer.URL + "/file.mp4?sign=xyz"
	encoded := base64.StdEncoding.EncodeToString([]byte(realURL))
	rawURL := "https://yun.123pan.com/download?params=" + encoded

	got, err := client.ResolveDownloadURL(context.Background(), rawURL)
	if err != nil {
		t.Fatalf("解析下载链接失败：%v", err)
	}
	if got != "https://real-cdn.example.com/final.mp4" {
		t.Errorf("302 重定向解析错误：%s", got)
	}
}

func TestResolveDownloadURLDirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]interface{}{
			"code": 0,
			"data": map[string]interface{}{"redirect_url": "https://final.example.com/video.mp4"},
		})
	}))
	defer server.Close()

	client := NewClient(1, "test@example.com", "pwd")
	client.SetBaseURL("http://localhost:1", "http://localhost:1")
	defer client.Close()

	got, err := client.ResolveDownloadURL(context.Background(), server.URL+"/download")
	if err != nil {
		t.Fatalf("解析下载链接失败：%v", err)
	}
	if got != "https://final.example.com/video.mp4" {
		t.Errorf("JSON 重定向解析错误：%s", got)
	}
}

func TestResolveDownloadURLLargeStreamDoesNotReadAll(t *testing.T) {
	// 模拟 CDN 直接以 200 返回超大视频流（非 JSON）：
	// 实现应只读取限量字节判断 JSON，直接返回原 URL，而不是把整个响应体读入内存
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Type", "application/octet-stream")
		chunk := make([]byte, 64*1024) // 64KB 垃圾数据
		// 写 20MB 模拟大视频流
		for i := 0; i < 320; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	client := NewClient(1, "test@example.com", "pwd")
	client.SetBaseURL("http://localhost:1", "http://localhost:1")
	defer client.Close()

	got, err := client.ResolveDownloadURL(context.Background(), server.URL+"/big.mkv")
	if err != nil {
		t.Fatalf("解析下载链接失败：%v", err)
	}
	if got != server.URL+"/big.mkv" {
		t.Errorf("非 JSON 200 响应应返回原 URL：%s", got)
	}
}

func TestMD5File(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	content := []byte("hello world")
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatalf("写测试文件失败：%v", err)
	}

	etag, err := MD5File(filePath)
	if err != nil {
		t.Fatalf("计算 MD5 失败：%v", err)
	}
	// "hello world" 的 MD5
	if etag != "5eb63bbbe01eeed093cb22bb8f5acdc3" {
		t.Errorf("MD5 错误：%s", etag)
	}
}

func TestIsEmailFormat(t *testing.T) {
	if !IsEmailFormat("user@example.com") {
		t.Error("邮箱应被识别")
	}
	if IsEmailFormat("13800138000") {
		t.Error("手机号不应被识别为邮箱")
	}
}

func TestGetFileById(t *testing.T) {
	client, _, _ := newTestClient(t, nil,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/file/list/new" {
				t.Errorf("路径错误：%s", r.URL.Path)
			}
			parentID := r.URL.Query().Get("parentFileId")
			if parentID == "1001" {
				// 父目录中查找，命中目标文件
				writeJSON(w, 200, map[string]interface{}{
					"code": 0, "message": "success",
					"data": map[string]interface{}{
						"Next": "-1", "Total": 1,
						"InfoList": []map[string]interface{}{
							{"FileName": "movie.mkv", "Size": 1024, "FileId": 2001, "Type": 0, "Etag": "etag-abc", "S3KeyFlag": "flag-1"},
						},
					},
				})
				return
			}
			// 根目录（回退路径），不包含目标文件
			writeJSON(w, 200, map[string]interface{}{
				"code": 0, "message": "success",
				"data": map[string]interface{}{"Next": "-1", "Total": 0, "InfoList": []map[string]interface{}{}},
			})
		},
	)
	client.SetAccessToken("token")

	file, err := client.GetFileById(context.Background(), "2001", "1001")
	if err != nil {
		t.Fatalf("按 ID 查找文件失败：%v", err)
	}
	if file.FileName != "movie.mkv" || file.Etag != "etag-abc" || file.S3KeyFlag != "flag-1" {
		t.Errorf("文件信息不完整：%+v", file)
	}
}

func TestGetFileByIdNotFound(t *testing.T) {
	client, _, _ := newTestClient(t, nil,
		func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]interface{}{
				"code": 0, "message": "success",
				"data": map[string]interface{}{"Next": "-1", "Total": 0, "InfoList": []map[string]interface{}{}},
			})
		},
	)
	client.SetAccessToken("token")

	if _, err := client.GetFileById(context.Background(), "9999", ""); err == nil {
		t.Fatal("不存在的文件应返回错误")
	}
}

func TestGetPathIdByPathRoot(t *testing.T) {
	client, _, _ := newTestClient(t, nil, nil)
	id, err := client.GetPathIdByPath(context.Background(), "")
	if err != nil {
		t.Fatalf("根路径查询失败：%v", err)
	}
	if id != "0" {
		t.Errorf("根路径 ID 应为 0：%s", id)
	}
}

func TestLoginTriggersAuthChanged(t *testing.T) {
	client, _, _ := newTestClient(t,
		func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]interface{}{
				"code":    200,
				"message": "success",
				"data":    map[string]interface{}{"token": "fresh-token"},
			})
		},
		nil,
	)
	var notified []string
	client.SetAuthChanged(func(token string) {
		notified = append(notified, token)
	})

	if err := client.Login(context.Background()); err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	if len(notified) != 1 || notified[0] != "fresh-token" {
		t.Errorf("登录后应回调一次新令牌：%v", notified)
	}

	// 令牌未变化时再次登录不应重复回调
	if err := client.Login(context.Background()); err != nil {
		t.Fatalf("重复登录失败：%v", err)
	}
	if len(notified) != 1 {
		t.Errorf("令牌未变化时不应重复回调：%v", notified)
	}
}

func TestRequestReloginNotifiesAuthChanged(t *testing.T) {
	loginCount := 0
	client, _, _ := newTestClient(t,
		func(w http.ResponseWriter, r *http.Request) {
			loginCount++
			writeJSON(w, 200, map[string]interface{}{
				"code":    200,
				"message": "success",
				"data":    map[string]interface{}{"token": "relogin-token"},
			})
		},
		func(w http.ResponseWriter, r *http.Request) {
			if loginCount == 0 {
				writeJSON(w, 200, map[string]interface{}{"code": 401, "message": "token expired"})
				return
			}
			writeJSON(w, 200, map[string]interface{}{"code": 0, "message": "success", "data": map[string]interface{}{}})
		},
	)
	client.SetAccessToken("expired")
	var notified []string
	client.SetAuthChanged(func(token string) {
		notified = append(notified, token)
	})

	if _, err := client.Request(context.Background(), client.api("/user/info"), http.MethodGet, nil); err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	if len(notified) != 1 || notified[0] != "relogin-token" {
		t.Errorf("401 重登录后应回调新令牌：%v", notified)
	}
}

func TestGetDownloadInfoAlwaysCallsAPI(t *testing.T) {
	client, _, _ := newTestClient(t, nil,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/file/download_info" {
				t.Errorf("路径错误：%s", r.URL.Path)
			}
			// 列表自带的 DownloadUrl 是缩略图链接，必须忽略并调用 download_info 接口
			body, _ := io.ReadAll(r.Body)
			var req map[string]interface{}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("请求体解析失败：%v", err)
			}
			if int(req["fileId"].(float64)) != 1001 {
				t.Errorf("请求体 fileId 错误：%v", req["fileId"])
			}
			writeJSON(w, 200, map[string]interface{}{
				"code": 0, "message": "success",
				"data": map[string]interface{}{"DownloadUrl": "https://api.example.com/dl?sign=1"},
			})
		},
	)
	client.SetAccessToken("token")

	info, err := client.GetDownloadInfo(context.Background(), File{
		FileId:      1001,
		FileName:    "movie.mkv",
		Etag:        "abc",
		S3KeyFlag:   "flag",
		DownloadUrl: "https://download-cdn.example.com/xxx?w=24&h=24&trade_key=123pan-thumbnail",
	})
	if err != nil {
		t.Fatalf("获取下载信息失败：%v", err)
	}
	if info.Data.DownloadUrl != "https://api.example.com/dl?sign=1" {
		t.Errorf("应忽略列表缩略图 DownloadUrl 并返回接口直链：%s", info.Data.DownloadUrl)
	}
}

func TestGetDownloadInfoFallbackToAPI(t *testing.T) {
	client, _, _ := newTestClient(t, nil,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/file/download_info" {
				t.Errorf("路径错误：%s", r.URL.Path)
			}
			writeJSON(w, 200, map[string]interface{}{
				"code": 0, "message": "success",
				"data": map[string]interface{}{"DownloadUrl": "https://api.example.com/dl?sign=1"},
			})
		},
	)
	client.SetAccessToken("token")

	info, err := client.GetDownloadInfo(context.Background(), File{
		FileId:   1001,
		FileName: "movie.mkv",
		Etag:     "abc",
		S3KeyFlag: "flag",
		Size:     1024,
		Type:     0,
	})
	if err != nil {
		t.Fatalf("获取下载信息失败：%v", err)
	}
	if info.Data.DownloadUrl != "https://api.example.com/dl?sign=1" {
		t.Errorf("DownloadUrl 为空时应回退 download_info 接口：%s", info.Data.DownloadUrl)
	}
}
