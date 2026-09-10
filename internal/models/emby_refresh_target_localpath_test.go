package models

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"diy-strm/internal/db"
)

// 回归：新剧首次生成 STRM 时条目/兄弟集解析均失败，此前会退化为
// 「同步目录关联的全部媒体库」（一个同步目录可能关联十几个库）全量刷新。
// 现在按变更文件本地路径前缀匹配库 Locations，精准锁定单个库。
func TestResolveEmbyRefreshTargetByLocalPathHitsSingleLibrary(t *testing.T) {
	setupEmbyRefreshTestDB(t)

	// 未关联任何库的同步目录，模拟临时同步场景
	existing := createEmbyRefreshSyncFile(t, 999, "媒体库/已整理/国产剧集/交锋 (2026) {tmdb=294486}/Season 01", "交锋.2026.S01E01.第1集.2160p.WEB-DL.60fps.H.265.AAC-Ocat.mkv", "jk1")
	// 本地落盘路径位于 国产剧集 库 Locations 之下
	existing.LocalFilePath = "/media/媒体库/已整理/国产剧集/交锋 (2026) {tmdb=294486}/Season 01/交锋.2026.S01E01.第1集.2160p.WEB-DL.60fps.H.265.AAC-Ocat.strm"
	if err := db.Db.Save(existing).Error; err != nil {
		t.Fatalf("更新 sync file 失败: %v", err)
	}

	var virtualFoldersRequests sync.Map
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/emby/Library/VirtualFolders":
			virtualFoldersRequests.Store("n", true)
			fmt.Fprint(w, `[{"ItemId":"914","Name":"国产剧集","Locations":["/media/媒体库/已整理/国产剧集","/media/影视/已整理/国产剧集"]},
				{"ItemId":"798","Name":"华语电影","Locations":["/media/媒体库/已整理/华语电影"]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	GlobalEmbyConfig.EmbyUrl = server.URL

	target, err := ResolveEmbyRefreshTarget(existing)
	if err != nil {
		t.Fatalf("解析刷新目标失败: %v", err)
	}
	if target.TargetType != EmbyRefreshTargetTypeLibrary {
		t.Fatalf("target type = %s，期望 Library", target.TargetType)
	}
	if target.FallbackLibraryId != "914" || target.FallbackLibraryName != "国产剧集" {
		t.Fatalf("fallback library = %s/%s，期望 914/国产剧集", target.FallbackLibraryId, target.FallbackLibraryName)
	}

	// 路径不落任何库时回退既有逻辑（本例同步目录无关联 → 无 fallback）
	existing.LocalFilePath = "/media/别的根/未分类/x.strm"
	if err := db.Db.Save(existing).Error; err != nil {
		t.Fatalf("更新 sync file 失败: %v", err)
	}
	target2, err := ResolveEmbyRefreshTarget(existing)
	if err != nil {
		t.Fatalf("解析刷新目标失败: %v", err)
	}
	if target2.FallbackLibraryId != "" {
		t.Fatalf("不相关路径不应命中库，实际 fallback=%s", target2.FallbackLibraryId)
	}
}
