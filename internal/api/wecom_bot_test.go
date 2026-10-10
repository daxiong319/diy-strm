package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"litepan/internal/settings"
	"litepan/internal/wecom"
)

// newWeComHandler 装一个只带 settings + 企微机器人的 handler。
//
// 与 newBotHandler 同样不走 NewRouter（nil Uploads 会在 router.go:1019 panic）。
func newWeComHandler(t *testing.T, values map[string]string) (*Handler, *botConfigRepo) {
	t.Helper()
	repo := &botConfigRepo{values: values}
	settingsSvc, err := settings.New(context.Background(), repo)
	if err != nil {
		t.Fatalf("构造设置服务失败：%v", err)
	}
	bot := wecom.NewBot(wecom.BotOptions{
		Config: wecom.BotConfigFromSettings(settingsSvc),
		Logger: &botNopLogger{},
	})
	return &Handler{settings: settingsSvc, wecomBot: bot}, repo
}

// TestWeComConfigSaveSyncsAfterPersisting 钉住「先落库、再同步」。
//
// 顺序反过来的症状是「保存成功了但机器人还是按旧白名单拒」：
// 企微那边已经用新配置在发消息，本地这边还在按旧值判定准入。
func TestWeComConfigSaveSyncsAfterPersisting(t *testing.T) {
	h, repo := newWeComHandler(t, map[string]string{
		settings.KeyMOWecomBotToken:        "old-secret",
		settings.KeyMOWecomBotAESKey:       "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG",
		settings.KeyMOWecomBotAllowedChats: "-100500",
	})

	req := httptest.NewRequest(http.MethodPut, "/api/admin/telegram/wecom-bot",
		strings.NewReader(`{"enabled":true,"allowed_chats":"-100600","allowed_users":"1001","group_link":true,"corp_id":"corp-1","agent_id":"1000002"}`))
	rec := httptest.NewRecorder()
	h.wecomBotConfigUpdate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存应成功，实际 %d：%s", rec.Code, rec.Body.String())
	}

	if got := repo.values[settings.KeyMOWecomBotAllowedChats]; got != "-100600" {
		t.Errorf("群白名单没有落库：%q", got)
	}
	if got := repo.values[settings.KeyMOWecomBotCorpID]; got != "corp-1" {
		t.Errorf("corpid 没有落库：%q", got)
	}
	cfg := h.wecomBot.Config()
	if len(cfg.AllowedChats) != 1 || cfg.AllowedChats[0] != -100600 {
		t.Errorf("保存后 Bot 内存里的群白名单没更新：%v", cfg.AllowedChats)
	}
	if !cfg.GroupLinkEnabled {
		t.Error("保存后群链接开关没更新")
	}
}

// TestWeComConfigKeepsSecretsWhenBlank 钉住「Secret / AESKey 留空 = 不修改」。
//
// 这两个字段是凭证，前端读不回明文，框永远是空的。
// 把空串当清空的话，用户改一下白名单就把自己的机器人弄哑了 ——
// 而他从头到尾没碰过凭证那一栏。
func TestWeComConfigKeepsSecretsWhenBlank(t *testing.T) {
	h, repo := newWeComHandler(t, map[string]string{
		settings.KeyMOWecomBotToken:  "keep-secret",
		settings.KeyMOWecomBotAESKey: "keep-aes",
	})

	req := httptest.NewRequest(http.MethodPut, "/api/admin/telegram/wecom-bot",
		strings.NewReader(`{"enabled":true,"token":"","aes_key":"","allowed_users":"1001"}`))
	rec := httptest.NewRecorder()
	h.wecomBotConfigUpdate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存应成功，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if got := repo.values[settings.KeyMOWecomBotToken]; got != "keep-secret" {
		t.Errorf("留空的 token 被当成清空了：%q", got)
	}
	if got := repo.values[settings.KeyMOWecomBotAESKey]; got != "keep-aes" {
		t.Errorf("留空的 aes_key 被当成清空了：%q", got)
	}

	var out Resp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	data, _ := out.Data.(map[string]any)
	if cfg, _ := data["token_configured"].(bool); !cfg {
		t.Errorf("响应应当说 token 已配置，实际 %v", data["token_configured"])
	}
	if cfg, _ := data["aes_configured"].(bool); !cfg {
		t.Errorf("响应应当说 aes_key 已配置，实际 %v", data["aes_configured"])
	}
}

// TestWeComConfigOverwritesSecretsWhenProvided 是上一条的对照。
//
// 没有对照的话，「凭证永远不变」也能让上面那条测试通过。
func TestWeComConfigOverwritesSecretsWhenProvided(t *testing.T) {
	h, repo := newWeComHandler(t, map[string]string{
		settings.KeyMOWecomBotToken:  "old-secret",
		settings.KeyMOWecomBotAESKey: "old-aes",
	})
	req := httptest.NewRequest(http.MethodPut, "/api/admin/telegram/wecom-bot",
		strings.NewReader(`{"token":"brand-new","aes_key":"brand-new-aes"}`))
	rec := httptest.NewRecorder()
	h.wecomBotConfigUpdate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存应成功，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if got := repo.values[settings.KeyMOWecomBotToken]; got != "brand-new" {
		t.Errorf("给了新 token 却没有覆盖：%q", got)
	}
	if got := repo.values[settings.KeyMOWecomBotAESKey]; got != "brand-new-aes" {
		t.Errorf("给了新 aes_key 却没有覆盖：%q", got)
	}
}

// TestWeComConfigGetNeverLeaksSecrets 是凭证「可写不可读」的守卫。
func TestWeComConfigGetNeverLeaksSecrets(t *testing.T) {
	h, _ := newWeComHandler(t, map[string]string{
		settings.KeyMOWecomBotToken:        "super-secret-token",
		settings.KeyMOWecomBotAESKey:       "super-secret-aes",
		settings.KeyMOWecomBotAllowedChats: "-100500",
		settings.KeyMOWecomBotCorpID:       "corp-42",
	})
	rec := httptest.NewRecorder()
	h.wecomBotConfigGet(rec, httptest.NewRequest(http.MethodGet, "/api/admin/telegram/wecom-bot", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("读取应成功，实际 %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "super-secret-token") {
		t.Fatal("GET 响应里出现了 token 明文")
	}
	if strings.Contains(body, "super-secret-aes") {
		t.Fatal("GET 响应里出现了 aes_key 明文")
	}
	if !strings.Contains(body, `"token_configured":true`) {
		t.Error("响应里应能看到 token 已配置")
	}
	// 页面要靠 callback_path 告诉用户把哪个地址填进企微后台。
	if !strings.Contains(body, `"callback_path":"/api/wecom/callback"`) {
		t.Error("响应里缺少回调地址")
	}
	// 命令表必须一起下发，页面才能告诉用户「哪些命令真的能用」。
	if !strings.Contains(body, `"commands"`) {
		t.Error("响应里没有命令表")
	}
}
