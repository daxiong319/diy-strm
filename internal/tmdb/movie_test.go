package tmdb

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestClient 创建指向 mock 服务的 TMDB 客户端。
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	// 单例模式：直接复用并重置 baseURL
	GlobalTmdbClient = nil
	return NewClient("", "", server.URL, "zh-CN", "")
}

func TestGetPopularMovies(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/movie/popular" {
			t.Errorf("请求路径不正确：%s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.URL.Query().Get("language"); got != "zh-CN" {
			t.Errorf("language 参数不正确：%s", got)
		}
		if got := r.URL.Query().Get("page"); got != "2" {
			t.Errorf("page 参数不正确：%s", got)
		}
		payload := SearchMovieResponse{
			Page:         2,
			TotalResults: 2,
			TotalPages:   1,
			Results: []SearchMovie{
				{ID: 100, Title: "测试电影一", BackdropPath: "/bg1.jpg", PosterPath: "/p1.jpg", ReleaseDate: "2026-01-01", VoteAverage: 8.5},
				{ID: 200, Title: "测试电影二", BackdropPath: "", PosterPath: "/p2.jpg", ReleaseDate: "2025-06-15", VoteAverage: 7.2},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	})

	resp, err := client.GetPopularMovies("zh-CN", 2)
	if err != nil {
		t.Fatalf("GetPopularMovies 返回错误：%v", err)
	}
	if resp == nil || len(resp.Results) != 2 {
		t.Fatalf("期望 2 部电影，实际 %v", resp)
	}
	first := resp.Results[0]
	if first.ID != 100 || first.Title != "测试电影一" || first.BackdropPath != "/bg1.jpg" {
		t.Errorf("第一部电影解析不正确：%+v", first)
	}
}

func TestGetPopularMoviesServerError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, "boom")
	})

	resp, err := client.GetPopularMovies("zh-CN", 1)
	if err == nil {
		t.Fatalf("期望返回错误，实际成功：%v", resp)
	}
}

func TestGetPopularMoviesDefaultPage(t *testing.T) {
	var gotPage string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPage = r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(SearchMovieResponse{Results: []SearchMovie{}})
	})

	// page <= 0 时不携带 page 参数
	if _, err := client.GetPopularMovies("zh-CN", 0); err != nil {
		t.Fatalf("GetPopularMovies 返回错误：%v", err)
	}
	if gotPage != "" {
		t.Errorf("page<=0 时不应携带 page 参数，实际 %q", gotPage)
	}
}
