package cloud189

import "testing"

// TestExtractQRBaseParams 从登录页 HTML 提取 lt/reqId/paramId/captchaToken
func TestExtractQRBaseParams(t *testing.T) {
	html := `<html><body>
<input type="hidden" id="captchaToken" value='abc123' />
<script>var lt = "lt_value"; var paramId = "pid_1"; var reqId = "rid_2";</script>
</body></html>`
	// 对齐 litepan 正则：'captchaToken' value='...' 与 lt = "..."
	// 注意：真实页面是 JS 对象 'captchaToken' value='xxx'，这里构造包含该模式
	html = `var x = 'captchaToken' value='abc123'; lt = "lt_value"; paramId = "pid_1"; reqId = "rid_2";`
	params, err := extractQRBaseParams(html)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if params["captcha_token"] != "abc123" {
		t.Fatalf("captcha_token 错误：%v", params)
	}
	if params["lt"] != "lt_value" || params["param_id"] != "pid_1" || params["req_id"] != "rid_2" {
		t.Fatalf("登录参数错误：%v", params)
	}
}

// TestQrStatusCode 扫码状态码提取（多字段兼容）
func TestQrStatusCode(t *testing.T) {
	if code, ok := qrStatusCode(map[string]any{"status": float64(0)}); !ok || code != 0 {
		t.Fatalf("status 字段提取失败：%d %v", code, ok)
	}
	if code, ok := qrStatusCode(map[string]any{"result": float64(-11001)}); !ok || code != -11001 {
		t.Fatalf("result 字段提取失败：%d %v", code, ok)
	}
	if _, ok := qrStatusCode(map[string]any{"foo": "bar"}); ok {
		t.Fatal("无状态码字段应返回 false")
	}
}

// TestQrRedirectURL redirectURL 多字段兼容 + 嵌套
func TestQrRedirectURL(t *testing.T) {
	if u := qrRedirectURL(map[string]any{"redirectUrl": "http://x/y"}); u != "http://x/y" {
		t.Fatalf("redirectUrl 提取失败：%s", u)
	}
	if u := qrRedirectURL(map[string]any{"data": map[string]any{"redirect_url": "http://nested"}}); u != "http://nested" {
		t.Fatalf("嵌套提取失败：%s", u)
	}
	if u := qrRedirectURL(map[string]any{"foo": "bar"}); u != "" {
		t.Fatalf("无 redirect 应返回空：%s", u)
	}
}

// TestAnyInt 宽容整数提取
func TestAnyInt(t *testing.T) {
	if anyInt(float64(42)) != 42 || anyInt(int(-3)) != -3 || anyInt("7") != 7 || anyInt("abc") != 0 {
		t.Fatal("anyInt 错误")
	}
}
