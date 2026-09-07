package controllers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"diy-strm/internal/db"
	"diy-strm/internal/models"
)

// 回归：Emby 302 独立反代播放端口（tgto123 形态）——
// ① proxy_port 随配置保存/未传时保留原值；② 保存后触发热重载钩子。
func TestUpdateEmbyConfigProxyPort(t *testing.T) {
	r := setupEmbyConfigControllerTest(t)

	var reloadCalls int32
	old := Emby302ProxyReloader
	Emby302ProxyReloader = func() { atomic.AddInt32(&reloadCalls, 1) }
	t.Cleanup(func() { Emby302ProxyReloader = old })

	if err := db.Db.Create(&models.EmbyConfig{
		EmbyUrl:    "http://emby.local:8096",
		EmbyApiKey: "api-key",
		ProxyPort:  0,
	}).Error; err != nil {
		t.Fatalf("创建 EmbyConfig 失败: %v", err)
	}

	doPut := func(payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/emby/config", bytes.NewBufferString(payload))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("HTTP = %d, body=%s", w.Code, w.Body.String())
		}
		return w
	}

	// 1. 传 proxy_port=8095 → 保存
	doPut(`{"emby_url":"http://emby.local:8096","emby_api_key":"api-key","sync_enabled":1,"sync_cron":"0 * * * *","sync_all_libraries":1,"proxy_port":8095}`)
	var cfg models.EmbyConfig
	if err := db.Db.First(&cfg).Error; err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	if cfg.ProxyPort != 8095 {
		t.Fatalf("proxy_port 应保存为 8095，实际 %d", cfg.ProxyPort)
	}

	// 2. 不传 proxy_port → 保留原值（8095）
	doPut(`{"emby_url":"http://emby.local:8096","emby_api_key":"api-key","sync_enabled":1,"sync_cron":"0 * * * *","sync_all_libraries":1}`)
	if err := db.Db.First(&cfg).Error; err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	if cfg.ProxyPort != 8095 {
		t.Fatalf("未传 proxy_port 应保留 8095，实际 %d", cfg.ProxyPort)
	}

	// 3. 传 0 → 关闭独立端口
	doPut(`{"emby_url":"http://emby.local:8096","emby_api_key":"api-key","sync_enabled":1,"sync_cron":"0 * * * *","sync_all_libraries":1,"proxy_port":0}`)
	if err := db.Db.First(&cfg).Error; err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	if cfg.ProxyPort != 0 {
		t.Fatalf("proxy_port=0 应保存为 0（关闭），实际 %d", cfg.ProxyPort)
	}

	// 4. 非法端口被拒（400）
	req := httptest.NewRequest(http.MethodPut, "/emby/config", bytes.NewBufferString(`{"emby_url":"http://emby.local:8096","emby_api_key":"api-key","sync_enabled":1,"sync_cron":"0 * * * *","sync_all_libraries":1,"proxy_port":80}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || !bytes.Contains(w.Body.Bytes(), []byte("proxy_port")) {
		t.Fatalf("端口 80 应被校验拒绝(400+proxy_port 提示)，实际 %d：%s", w.Code, w.Body.String())
	}

	if atomic.LoadInt32(&reloadCalls) < 3 {
		t.Fatalf("每次成功保存都应触发热重载钩子（至少 3 次），实际 %d", reloadCalls)
	}
}

