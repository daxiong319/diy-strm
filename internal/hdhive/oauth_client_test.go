package hdhive

import (
	"testing"
)

// 与 tgto123 容器内 Python 参考输出交叉验证
func TestCanonicalAndSignatureMatchPython(t *testing.T) {
	c := NewOAuthClient("install-test-abc123")

	cases := []struct {
		name   string
		method string
		path   string
		body   []byte
		wantCN string
		wantSG string
	}{
		{
			name:   "ping",
			method: "GET",
			path:   "/api/ping",
			body:   nil,
			wantCN: "GET\n/api/ping\n\ninstall-test-abc123\n1234567890\nnonce-test-hex-001\ne3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			wantSG: "b4cc1d68d4279444077f61e630d43180f7f9f76d60ba87dbab72d1677dd3b698",
		},
		{
			name:   "checkin",
			method: "POST",
			path:   "/api/checkin",
			body:   []byte(`{"is_gambler":true}`),
			wantCN: "POST\n/api/checkin\n\ninstall-test-abc123\n1234567890\nnonce-test-hex-001\n6b06da823108d15a7734cee73226891b0e85b6ed18f18150027649b8e0d4fe72",
			wantSG: "f8f00ae25d8442ea06c1cb6eecc6a5d81e53cda91bb7a20977e90320ab3ad06f",
		},
		{
			name:   "resources with query sorting",
			method: "GET",
			path:   "/api/resources/movie/12345?x=1&a=2",
			body:   nil,
			wantCN: "GET\n/api/resources/movie/12345\na=2&x=1\ninstall-test-abc123\n1234567890\nnonce-test-hex-001\ne3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			wantSG: "5546dd1ec7c07f2d4c50d3b6ff19eb872eb5fcceed391b7313c131a2f8f75452",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotCN := canonicalRequest(tc.method, tc.path, c.InstallID, "1234567890", "nonce-test-hex-001", tc.body)
			if gotCN != tc.wantCN {
				t.Fatalf("canonical request 不匹配\n  got:  %q\n  want: %q", gotCN, tc.wantCN)
			}
			gotSG := c.signRequest(gotCN)
			if gotSG != tc.wantSG {
				t.Fatalf("签名不匹配\n  got:  %s\n  want: %s", gotSG, tc.wantSG)
			}
		})
	}
}

func TestSignatureStableUnderDifferentSecrets(t *testing.T) {
	// 默认密钥与自定义密钥应产生不同签名
	c1 := NewOAuthClient("install-test-abc123")
	c2 := NewOAuthClient("install-test-abc123")
	c2.secret = []byte("other-secret")
	canon := canonicalRequest("GET", "/api/ping", "install-test-abc123", "1234567890", "nonce", nil)
	if c1.signRequest(canon) == c2.signRequest(canon) {
		t.Fatal("不同密钥应产生不同签名")
	}
}

func TestCanonicalQueryStringSkipsSignature(t *testing.T) {
	got := canonicalQueryString(map[string][]string{
		"sig":      {"abc"},
		"signature": {"def"},
		"b":        {"2"},
		"a":        {"1"},
	})
	if got != "a=1&b=2" {
		t.Fatalf("canonicalQueryString = %q, want %q", got, "a=1&b=2")
	}
}

func TestNewInstallIDFormat(t *testing.T) {
	id := NewInstallID()
	if len(id) < 40 {
		t.Fatalf("install_id 长度异常：%d", len(id))
	}
	if InstallHash(id) == "" || len(InstallHash(id)) != 64 {
		t.Fatal("install hash 应为 64 位十六进制")
	}
}

func TestBuildAuthURLStructure(t *testing.T) {
	c := NewOAuthClient("install-test-abc123")
	u := c.BuildAuthURL()
	if u[:len(c.BaseURL)] != c.BaseURL {
		t.Fatalf("授权 URL 应以 base URL 开头：%s", u)
	}
	for _, part := range []string{"/auth/start", "install_id=", "ts=", "nonce=", "sig="} {
		if !containsStr(u, part) {
			t.Fatalf("授权 URL 缺少 %s：%s", part, u)
		}
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}