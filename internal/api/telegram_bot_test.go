package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"litepan/internal/settings"
	"litepan/internal/tgbot"
)

// botConfigRepo 是 domain.ConfigRepository 的最小实现。
type botConfigRepo struct {
	mu     sync.Mutex
	values map[string]string
}

func (r *botConfigRepo) Get(_ context.Context, key string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.values[key]
	return v, ok, nil
}

func (r *botConfigRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key] = value
	return nil
}

func (r *botConfigRepo) All(context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(r.values))
	for k, v := range r.values {
		out[k] = v
	}
	return out, nil
}

// newBotHandler 装一个只带 settings + Bot 的 handler。
//
// 不走 NewRouter：那个构造器会因为 nil Uploads 直接 panic（internal/api/router.go:1019），
// 而这里要测的是端点行为，不是整棵路由树。
func newBotHandler(t *testing.T, values map[string]string) (*Handler, *botConfigRepo) {
	t.Helper()
	repo := &botConfigRepo{values: values}
	settingsSvc, err := settings.New(context.Background(), repo)
	if err != nil {
		t.Fatalf("构造设置服务失败：%v", err)
	}
	svc := tgbot.New(tgbot.Options{
		Config: tgbot.ConfigFromSettings(settingsSvc),
		Log:    &botNopLogger{},
	})
	return &Handler{settings: settingsSvc, tgbot: svc}, repo
}

type botNopLogger struct{}

func (botNopLogger) Debugf(string, ...any) {}
func (botNopLogger) Warnf(string, ...any)  {}

// TestBotConfigSaveSyncsWebhookAfterPersisting 钉住「先落库、再同步」这个顺序。
//
// 顺序反过来的症状很难查：先同步后落库的话，Telegram 侧已经按新配置登记，
// 本地还没生效 —— 中间进来的消息全被拒，而页面上已经显示「保存成功」。
// 所以这里断言的是**保存返回成功后** Bot 内存里的配置已经是新值，
// 而不是只断言 HTTP 200。
func TestBotConfigSaveSyncsWebhookAfterPersisting(t *testing.T) {
	h, repo := newBotHandler(t, map[string]string{
		settings.KeyMOTelegramBotToken:        "old-token",
		settings.KeyMOTelegramBotAllowedUsers: "1001",
	})

	body := strings.NewReader(`{"enabled":true,"allowed_users":"2002","group_link":true}`)
	req := httptest.NewRequest(http.MethodPut, "/api/admin/telegram/bot", body)
	rec := httptest.NewRecorder()
	h.telegramBotConfigUpdate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存应成功，实际 %d：%s", rec.Code, rec.Body.String())
	}

	if got := repo.values[settings.KeyMOTelegramBotAllowedUsers]; got != "2002" {
		t.Errorf("白名单没有落库：%q", got)
	}
	if got := repo.values[settings.KeyMOTelegramBotEnabled]; got != "true" {
		t.Errorf("开关没有落库：%q", got)
	}
	// 落库之后必须已经同步进 Bot —— 否则 Telegram 已经收新消息、本地还在按旧白名单拒。
	if got := h.tgbot.Config().AllowedUsers; len(got) != 1 || got[0] != 2002 {
		t.Errorf("保存后 Bot 内存里的白名单没更新：%v", got)
	}
	if !h.tgbot.Config().GroupLinkEnabled {
		t.Error("保存后 Bot 内存里的群链接开关没更新")
	}
}

// TestBotConfigSaveKeepsTokenWhenBlank 钉住「token 留空 = 不修改」。
//
// 这条是从前端行为反推出来的：token 是凭证，读不回明文，
// 页面上那个框永远是空的。如果空串被当成「清空」，用户改一下白名单就会把自己的
// Bot 弄哑 —— 而他完全没碰过 token 那一栏。
func TestBotConfigSaveKeepsTokenWhenBlank(t *testing.T) {
	h, repo := newBotHandler(t, map[string]string{
		settings.KeyMOTelegramBotToken:        "keep-me",
		settings.KeyMOTelegramBotAllowedUsers: "1001",
	})

	req := httptest.NewRequest(http.MethodPut, "/api/admin/telegram/bot",
		strings.NewReader(`{"enabled":true,"allowed_users":"2002","token":""}`))
	rec := httptest.NewRecorder()
	h.telegramBotConfigUpdate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存应成功，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if got := repo.values[settings.KeyMOTelegramBotToken]; got != "keep-me" {
		t.Errorf("留空的 token 被当成清空了：%q", got)
	}

	var out Resp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	data, _ := out.Data.(map[string]any)
	if cfg, _ := data["token_configured"].(bool); !cfg {
		t.Errorf("响应应当说 token 已配置，实际 %v", data["token_configured"])
	}
}

// TestBotConfigOverwritesTokenWhenProvided 是上一条的对照：给了新值就必须写进去。
//
// 没有对照的话，「token 永远不变」也能让上面那条测试通过。
func TestBotConfigOverwritesTokenWhenProvided(t *testing.T) {
	h, repo := newBotHandler(t, map[string]string{
		settings.KeyMOTelegramBotToken: "old-token",
	})
	req := httptest.NewRequest(http.MethodPut, "/api/admin/telegram/bot",
		strings.NewReader(`{"token":"brand-new"}`))
	rec := httptest.NewRecorder()
	h.telegramBotConfigUpdate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存应成功，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if got := repo.values[settings.KeyMOTelegramBotToken]; got != "brand-new" {
		t.Errorf("给了新 token 却没有覆盖：%q", got)
	}
}

// TestBotConfigGetNeverLeaksToken 是凭证「可写不可读」的守卫。
func TestBotConfigGetNeverLeaksToken(t *testing.T) {
	h, _ := newBotHandler(t, map[string]string{
		settings.KeyMOTelegramBotToken:        "super-secret-token",
		settings.KeyMOTelegramBotAllowedUsers: "1001,-100500",
	})
	rec := httptest.NewRecorder()
	h.telegramBotConfigGet(rec, httptest.NewRequest(http.MethodGet, "/api/admin/telegram/bot", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("读取应成功，实际 %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "super-secret-token") {
		t.Fatal("GET 响应里出现了 token 明文")
	}
	if !strings.Contains(rec.Body.String(), "\"token_configured\":true") {
		t.Error("响应里应能看到 token 已配置")
	}
	// 命令表必须一起下发，页面才能告诉用户「哪些命令真的能用」。
	if !strings.Contains(rec.Body.String(), "\"commands\"") {
		t.Error("响应里没有命令表")
	}
}
