package hdhive

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestHKDFSHA256RFC5869 用 RFC 5869 附录 A.1 官方测试向量校验手写 HKDF 实现
func TestHKDFSHA256RFC5869(t *testing.T) {
	ikm, _ := hex.DecodeString("0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b")
	salt, _ := hex.DecodeString("000102030405060708090a0b0c")
	info, _ := hex.DecodeString("f0f1f2f3f4f5f6f7f8f9")
	want, _ := hex.DecodeString("3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf34007208d5b887185865")
	got := hkdfSHA256(ikm, salt, info, 42)
	if !bytesEqual(got, want) {
		t.Fatalf("HKDF-SHA256 结果不匹配\n  got:  %x\n  want: %x", got, want)
	}
}

// TestSymediaProofFormat 验证 proof 串格式（HMAC-SHA256(secret, prefix+role+"\n"+nonce)）
func TestSymediaProofFormat(t *testing.T) {
	c := NewSymediaClient("u1", "k1")
	c.secret = []byte("test-secret")
	proof := c.proof("nonce123")
	if proof == "" {
		t.Fatal("proof 为空")
	}
	if len(proof) != 64 {
		t.Fatalf("proof 长度 %d，期望 64（hex SHA-256）", len(proof))
	}
	// 与手工计算对照：HMAC-SHA256("test-secret", "hdhive-openproxy-proof\nclient\nnonce123")
	mac := hmac.New(sha256.New, []byte("test-secret"))
	mac.Write([]byte("hdhive-openproxy-proof\nclient\nnonce123"))
	want := hex.EncodeToString(mac.Sum(nil))
	if proof != want {
		t.Fatalf("proof 不匹配\n  got:  %s\n  want: %s", proof, want)
	}
}

// TestSymediaHandshakeAndSignedRequest 端到端：mock 服务器按真实协议重建会话密钥，
// 校验签名路径必须含 query（/api/v1/oauth/start?callback=...），并验证序列号递增
func TestSymediaHandshakeAndSignedRequest(t *testing.T) {
	var mu sync.Mutex
	seqs := []string{}
	handshakes := 0
	var clientNonce string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/session" {
			handshakes++
			var req struct {
				ClientNonce string `json:"client_nonce"`
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &req)
			mu.Lock()
			clientNonce = req.ClientNonce
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			mac := hmac.New(sha256.New, []byte("test-secret"))
			mac.Write([]byte("hdhive-openproxy-proof\nserver\nsrv-nonce"))
			serverProof := hex.EncodeToString(mac.Sum(nil))
			fmt.Fprintf(w, `{"success":true,"data":{"session_id":"sess-abc","server_nonce":"srv-nonce","server_proof":"%s","expires_in":21600}}`, serverProof)
			return
		}
		if r.URL.Path == "/api/v1/oauth/start" {
			// 重建会话密钥（与客户端同一派生逻辑），校验签名与「含 query 的完整路径」一致
			mu.Lock()
			cn := clientNonce
			mu.Unlock()
			salt := []byte("hdhive-openproxy-session:" + cn + ":srv-nonce")
			key := hkdfSHA256([]byte("test-secret"), salt, []byte("hdhive-openproxy-session-key"), 32)
			pathWithQuery := r.URL.Path
			if r.URL.RawQuery != "" {
				pathWithQuery += "?" + r.URL.RawQuery
			}
			msg := strings.Join([]string{
				"POST", pathWithQuery,
				r.Header.Get("X-Proxy-Session"),
				r.Header.Get("X-Proxy-Sequence"),
				r.Header.Get("X-Proxy-Body-SHA256"),
				r.Header.Get("X-Proxy-User-Key"),
			}, "\n")
			expect := hmac.New(sha256.New, key)
			expect.Write([]byte(msg))
			wantSig := hex.EncodeToString(expect.Sum(nil))
			if got := r.Header.Get("X-Proxy-Signature"); got != wantSig {
				t.Errorf("签名校验失败：\n  got=%s\n want=%s\n（签名路径必须含 query）", got, wantSig)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"success":false,"message":"密钥错误或签名无效"}`)
				return
			}
			mu.Lock()
			seqs = append(seqs, r.Header.Get("X-Proxy-Sequence"))
			mu.Unlock()
			if r.Header.Get("X-Proxy-Session") != "sess-abc" {
				t.Errorf("X-Proxy-Session = %q，期望 sess-abc", r.Header.Get("X-Proxy-Session"))
			}
			if r.Header.Get("X-Proxy-User-Key") != "k1" {
				t.Errorf("X-Proxy-User-Key = %q，期望 k1", r.Header.Get("X-Proxy-User-Key"))
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"success":true,"data":{"authorize_url":"https://hdhive.com/authorize?x=1"}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewSymediaClient("u1", "k1")
	c.BaseURL = srv.URL
	c.secret = []byte("test-secret")

	authURL, err := c.StartOAuth(context.Background(), "http://127.0.0.1:8094/hive-symedia/callback")
	if err != nil {
		t.Fatalf("StartOAuth 失败：%v", err)
	}
	if authURL == "" {
		t.Fatal("StartOAuth 返回空 authorize_url")
	}
	if handshakes != 1 {
		t.Fatalf("握手次数 %d，期望 1（会话应复用）", handshakes)
	}

	// 再请求一次：序列号递增，不重复握手
	if _, err := c.StartOAuth(context.Background(), "http://127.0.0.1:8094/hive-symedia/callback"); err != nil {
		t.Fatalf("第二次 StartOAuth 失败：%v", err)
	}
	if handshakes != 1 {
		t.Fatalf("第二次请求后握手次数 %d，期望仍为 1", handshakes)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seqs) != 2 {
		t.Fatalf("业务请求次数 %d，期望 2", len(seqs))
	}
	if seqs[0] != "1" || seqs[1] != "2" {
		t.Fatalf("序列号递增异常：%v，期望 [1 2]", seqs)
	}
}

// TestSymediaSessionResetOn403 403「密钥错误」时应重置会话并重握手重试
func TestSymediaSessionResetOn403(t *testing.T) {
	var mu sync.Mutex
	handshakes := 0
	first403 := true

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/session" {
			mu.Lock()
			handshakes++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			mac := hmac.New(sha256.New, []byte("test-secret"))
			mac.Write([]byte("hdhive-openproxy-proof\nserver\nsrv-nonce"))
			serverProof := hex.EncodeToString(mac.Sum(nil))
			fmt.Fprintf(w, `{"success":true,"data":{"session_id":"sess-new","server_nonce":"srv-nonce","server_proof":"%s","expires_in":21600}}`, serverProof)
			return
		}
		mu.Lock()
		if first403 {
			first403 = false
			mu.Unlock()
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"success":false,"message":"密钥错误或签名无效"}`)
			return
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"success":true,"data":[]}`)
	}))
	defer srv.Close()

	c := NewSymediaClient("u1", "k1")
	c.BaseURL = srv.URL
	c.secret = []byte("test-secret")

	resp, err := c.GetResources(context.Background(), "movie", "123")
	if err != nil {
		t.Fatalf("GetResources 失败：%v", err)
	}
	if !resp.Success {
		t.Fatalf("403 后应重握手重试成功，实际失败：%s", resp.Message)
	}
	mu.Lock()
	defer mu.Unlock()
	if handshakes != 2 {
		t.Fatalf("握手次数 %d，期望 2（初次 + 403 后重置）", handshakes)
	}
}

// TestParseSymediaResponse 宽容解析：success 信封与平铺对象
func TestParseSymediaResponse(t *testing.T) {
	// 平铺对象（如 users/{userid}/status）
	flat := parseSymediaResponse([]byte(`{"authorized":true,"userid":"u1","username":"tester"}`), 200)
	if !flat.Success {
		t.Fatal("平铺对象 HTTP 200 应视为成功")
	}
	if !strings.Contains(string(flat.Data), "tester") {
		t.Fatalf("平铺对象应整体作为 data：%s", string(flat.Data))
	}
	// success 信封
	env := parseSymediaResponse([]byte(`{"success":false,"code":"E1","message":"失败","data":null}`), 200)
	if env.Success {
		t.Fatal("success=false 应解析为失败")
	}
	if env.Code != "E1" || env.Message != "失败" {
		t.Fatalf("code/message 解析错误：%+v", env)
	}
	// data 信封
	data := parseSymediaResponse([]byte(`{"success":true,"data":[{"slug":"s1"}]}`), 200)
	if !data.Success || !strings.Contains(string(data.Data), "s1") {
		t.Fatalf("data 信封解析错误：%+v", data)
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
