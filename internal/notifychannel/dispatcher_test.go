package notifychannel

import "testing"

func TestMatchesEventFilter(t *testing.T) {
	cases := []struct {
		name     string
		events   string
		category string
		want     bool
	}{
		{"空白名单放行", "", "auth", true},
		{"空白字符放行", "   ", "auth", true},
		{"单命中", "auth", "auth", true},
		{"多值命中", "system,auth,strm", "auth", true},
		{"带空格命中", "system, auth , strm", "auth", true},
		{"大小写不敏感", "AUTH", "auth", true},
		{"未命中", "system,strm", "auth", false},
		{"空前缀不误伤", "auth", "authx", false},
	}
	for _, c := range cases {
		if got := matchesEventFilter(c.events, c.category); got != c.want {
			t.Errorf("%s: matchesEventFilter(%q,%q)=%v want %v", c.name, c.events, c.category, got, c.want)
		}
	}
}

func TestDecodeConfig(t *testing.T) {
	if got := decodeConfig(""); len(got) != 0 {
		t.Errorf("空串应返回空 map，got %v", got)
	}
	if got := decodeConfig("not-json"); len(got) != 0 {
		t.Errorf("非法 JSON 应返回空 map，got %v", got)
	}
	got := decodeConfig(`{"bot_token":"x","chat_id":"1"}`)
	if got["bot_token"] != "x" || got["chat_id"] != "1" {
		t.Errorf("解析结果不符: %v", got)
	}
}

func TestToneFromLevel(t *testing.T) {
	cases := map[string]string{
		"error": "error", "danger": "error",
		"warn": "warn", "warning": "warn",
		"success": "success",
		"info":    "info", "": "info", "weird": "info",
	}
	for in, want := range cases {
		if got := toneFromLevel(in); got != want {
			t.Errorf("toneFromLevel(%q)=%s want %s", in, got, want)
		}
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	cfg := map[string]string{"a": "1", "b": "two"}
	if got := decodeConfig(encodeConfig(cfg)); got["a"] != "1" || got["b"] != "two" {
		t.Errorf("往返失败: %v", got)
	}
}
