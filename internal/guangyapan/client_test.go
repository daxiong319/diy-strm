package guangyapan

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newMockServer 创建光鸭云盘 mock 服务
// handleAPI 处理 /api 前缀请求，handleAccount 处理 /account 前缀请求
func newMockServer(t *testing.T, handleAPI, handleAccount http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api"):
			if handleAPI != nil {
				handleAPI(w, r)
				return
			}
		case strings.HasPrefix(r.URL.Path, "/account"):
			if handleAccount != nil {
				handleAccount(w, r)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	client := NewClient(1, "test-access-token", "test-refresh-token")
	client.SetBaseURL(server.URL+"/account", server.URL+"/api")
	return server, client
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// TestGetFilesPagination 测试列表自动分页
func TestGetFilesPagination(t *testing.T) {
	var listCalls int
	// 第一页返回满页（PageSize 个），第二页返回剩余 1 个
	page1Files := make([]File, 0, PageSize)
	for i := 0; i < PageSize; i++ {
		page1Files = append(page1Files, File{
			FileID:   fmt.Sprintf("%d", i+1),
			ParentID: "123",
			FileName: fmt.Sprintf("文件%d.mp4", i+1),
			FileSize: int64(i + 1),
			ResType:  0,
			UTime:    1700000000,
		})
	}
	server, client := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api"+APIFileList {
			t.Errorf("意外路径：%s", r.URL.Path)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-access-token" {
			t.Errorf("Authorization 头错误：%s", got)
		}
		if got := r.Header.Get("Did"); got == "" {
			t.Error("缺少 Did 请求头")
		}
		if got := r.Header.Get("Dt"); got != "4" {
			t.Errorf("Dt 头错误：%s", got)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("解析请求体失败：%v", err)
			return
		}
		page, _ := body["page"].(float64)
		if body["parentId"] != "123" {
			t.Errorf("parentId 错误：%v", body["parentId"])
		}
		listCalls++
		if page == 0 {
			writeJSON(w, Files{Code: 0, Msg: "success", Data: struct {
				Total int    `json:"total"`
				List  []File `json:"list"`
			}{Total: PageSize + 1, List: page1Files}})
			return
		}
		writeJSON(w, Files{Code: 0, Msg: "success", Data: struct {
			Total int    `json:"total"`
			List  []File `json:"list"`
		}{Total: PageSize + 1, List: []File{
			{FileID: "101", ParentID: "123", FileName: "目录B", FileSize: 0, ResType: 2, UTime: 1700000001},
		}}})
	}, nil)
	defer server.Close()
	defer client.Close()

	files, err := client.GetFiles(context.Background(), "123")
	if err != nil {
		t.Fatalf("GetFiles 失败：%v", err)
	}
	if listCalls != 2 {
		t.Errorf("应请求 2 页，实际 %d 页", listCalls)
	}
	if len(files) != PageSize+1 {
		t.Fatalf("应返回 %d 个文件，实际 %d", PageSize+1, len(files))
	}
	if !files[PageSize].IsDir() {
		t.Error("目录 B 的 ResType=2 应判定为目录")
	}
	if files[0].GetName() != "文件1.mp4" {
		t.Errorf("文件名错误：%s", files[0].GetName())
	}
	if files[PageSize].GetSize() != 0 {
		t.Errorf("文件大小错误：%d", files[PageSize].GetSize())
	}
}

// TestGetDownloadURL 测试获取下载直链
func TestGetDownloadURL(t *testing.T) {
	server, client := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api"+APIDownloadURL {
			t.Errorf("意外路径：%s", r.URL.Path)
			return
		}
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if body["fileId"] != "42" {
			t.Errorf("fileId 错误：%v", body["fileId"])
		}
		writeJSON(w, map[string]interface{}{
			"code": 0, "msg": "success",
			"data": map[string]interface{}{
				"signedURL":   "https://cdn.example.com/signed/42",
				"downloadUrl": "https://cdn.example.com/direct/42",
			},
		})
	}, nil)
	defer server.Close()
	defer client.Close()

	url, err := client.GetDownloadURL(context.Background(), "42")
	if err != nil {
		t.Fatalf("GetDownloadURL 失败：%v", err)
	}
	if url != "https://cdn.example.com/signed/42" {
		t.Errorf("应优先返回 signedURL，实际：%s", url)
	}
}

// TestGetDownloadURLFallback 测试 signedURL 为空时回退 downloadUrl
func TestGetDownloadURLFallback(t *testing.T) {
	server, client := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{
			"code": 0, "msg": "success",
			"data": map[string]interface{}{
				"signedURL":   "",
				"downloadUrl": "https://cdn.example.com/direct/42",
			},
		})
	}, nil)
	defer server.Close()
	defer client.Close()

	url, err := client.GetDownloadURL(context.Background(), "42")
	if err != nil {
		t.Fatalf("GetDownloadURL 失败：%v", err)
	}
	if url != "https://cdn.example.com/direct/42" {
		t.Errorf("应回退 downloadUrl，实际：%s", url)
	}
}

// TestRefreshToken 测试刷新令牌
func TestRefreshToken(t *testing.T) {
	server, client := newMockServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/account/v1/auth/token" {
			t.Errorf("意外路径：%s", r.URL.Path)
			return
		}
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if body["grant_type"] != "refresh_token" {
			t.Errorf("grant_type 错误：%v", body["grant_type"])
		}
		if body["refresh_token"] != "test-refresh-token" {
			t.Errorf("refresh_token 错误：%v", body["refresh_token"])
		}
		if body["client_id"] != DefaultClient {
			t.Errorf("client_id 错误：%v", body["client_id"])
		}
		if r.Header.Get("X-Device-Id") == "" {
			t.Error("缺少 X-Device-Id 请求头")
		}
		writeJSON(w, TokenResp{
			AccessToken:  "new-access-token",
			RefreshToken: "new-refresh-token",
			TokenType:    "bearer",
		})
	})
	defer server.Close()
	defer client.Close()

	if err := client.RefreshToken(context.Background()); err != nil {
		t.Fatalf("RefreshToken 失败：%v", err)
	}
	if got := client.GetAccessToken(); got != "new-access-token" {
		t.Errorf("访问令牌未更新：%s", got)
	}
	if got := client.GetRefreshToken(); got != "new-refresh-token" {
		t.Errorf("刷新令牌未更新：%s", got)
	}
}

// TestRequestAutoRefresh 测试 401 自动刷新并重试
func TestRequestAutoRefresh(t *testing.T) {
	apiCalls := 0
	server, client := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		apiCalls++
		if apiCalls == 1 {
			// 第一次 401
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// 第二次带新令牌成功
		if got := r.Header.Get("Authorization"); got != "Bearer new-access-token" {
			t.Errorf("重试时未使用新令牌：%s", got)
		}
		writeJSON(w, Files{Code: 0, Msg: "success"})
	}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/account/v1/auth/token" {
			t.Errorf("意外路径：%s", r.URL.Path)
			return
		}
		writeJSON(w, TokenResp{AccessToken: "new-access-token"})
	})
	defer server.Close()
	defer client.Close()

	var out Files
	if err := client.Request(context.Background(), APIFileList, map[string]interface{}{"parentId": ""}, &out); err != nil {
		t.Fatalf("Request 失败：%v", err)
	}
	if apiCalls != 2 {
		t.Errorf("应请求 2 次，实际 %d 次", apiCalls)
	}
	if got := client.GetAccessToken(); got != "new-access-token" {
		t.Errorf("访问令牌未刷新：%s", got)
	}
}

// TestRequestRefreshFail 测试无刷新令牌时 401 报错
func TestRequestRefreshFail(t *testing.T) {
	server, client := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}, nil)
	defer server.Close()
	defer client.Close()
	client.SetTokens("expired", "")

	var out Files
	err := client.Request(context.Background(), APIFileList, map[string]interface{}{}, &out)
	if err == nil {
		t.Fatal("无刷新令牌时应报错")
	}
	if !strings.Contains(err.Error(), "refresh_token 为空") {
		t.Errorf("错误信息不符合预期：%v", err)
	}
}

// TestGetPathIdByPath 测试路径解析
func TestGetPathIdByPath(t *testing.T) {
	server, client := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		parentID, _ := body["parentId"].(string)
		var list []File
		switch parentID {
		case "":
			list = []File{
				{FileID: "100", FileName: "电影", ResType: 2},
				{FileID: "101", FileName: "电视剧", ResType: 2},
			}
		case "100":
			list = []File{
				{FileID: "200", FileName: "科幻", ResType: 2},
				{FileID: "201", FileName: "动作", ResType: 2},
			}
		case "200":
			list = []File{{FileID: "300", FileName: "流浪地球.mkv", ResType: 0}}
		}
		writeJSON(w, Files{Code: 0, Msg: "success", Data: struct {
			Total int    `json:"total"`
			List  []File `json:"list"`
		}{Total: len(list), List: list}})
	}, nil)
	defer server.Close()
	defer client.Close()

	id, err := client.GetPathIdByPath(context.Background(), "/电影/科幻")
	if err != nil {
		t.Fatalf("GetPathIdByPath 失败：%v", err)
	}
	if id != "200" {
		t.Errorf("目录 ID 错误：%s", id)
	}

	if _, err := client.GetPathIdByPath(context.Background(), "/电影/不存在的目录"); err == nil {
		t.Error("不存在的路径应报错")
	}

	rootID, err := client.GetPathIdByPath(context.Background(), "/")
	if err != nil || rootID != "" {
		t.Errorf("根目录 ID 应为空，实际 %q，err=%v", rootID, err)
	}
}

// TestGetUserInfo 测试用户信息
func TestGetUserInfo(t *testing.T) {
	server, client := newMockServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/account/v1/user/me" {
			t.Errorf("意外路径：%s", r.URL.Path)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-access-token" {
			t.Errorf("Authorization 头错误：%s", got)
		}
		writeJSON(w, UserMeResp{Sub: "user-123"})
	})
	defer server.Close()
	defer client.Close()

	me, err := client.GetUserInfo(context.Background())
	if err != nil {
		t.Fatalf("GetUserInfo 失败：%v", err)
	}
	if me.Sub != "user-123" {
		t.Errorf("用户标识错误：%s", me.Sub)
	}
}

// TestDeleteFile 测试删除文件（异步任务）
func TestDeleteFile(t *testing.T) {
	server, client := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api" + APIDeleteFile:
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			ids, _ := body["fileIds"].([]interface{})
			if len(ids) != 2 {
				t.Errorf("fileIds 数量错误：%v", ids)
			}
			writeJSON(w, map[string]interface{}{
				"code": 0, "msg": "success",
				"data": map[string]interface{}{"taskId": "task-1"},
			})
		case "/api" + APIGetTaskStatus:
			writeJSON(w, map[string]interface{}{
				"code": 0, "msg": "success",
				"data": map[string]interface{}{"status": 2},
			})
		default:
			t.Errorf("意外路径：%s", r.URL.Path)
		}
	}, nil)
	defer server.Close()
	defer client.Close()

	if err := client.Delete(context.Background(), []string{"1", "2"}); err != nil {
		t.Fatalf("Delete 失败：%v", err)
	}
}
