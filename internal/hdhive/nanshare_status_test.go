package hdhive

import (
	"testing"
)

func TestParseNanShareStatus_Revoked(t *testing.T) {
	body := []byte(`{"ok":true,"account":{"oauth_status":"revoked","reauth_required":false}}`)
	p, err := ParseNanShareStatus(&OAuthAPIResponse{Data: body})
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if p.Authorized == nil {
		t.Fatal("Authorized should not be nil")
	}
	if *p.Authorized {
		t.Fatal("revoked should map to authorized=false")
	}
	if p.ReauthRequired {
		t.Fatal("reauth_required=false expected")
	}
}

func TestParseNanShareStatus_Authorized(t *testing.T) {
	body := []byte(`{"ok":true,"account":{"oauth_status":"authorized","reauth_required":true}}`)
	p, err := ParseNanShareStatus(&OAuthAPIResponse{Data: body})
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if p.Authorized == nil || !*p.Authorized {
		t.Fatal("authorized should map to authorized=true")
	}
	if !p.ReauthRequired {
		t.Fatal("reauth_required=true expected")
	}
}

func TestParseNanShareStatus_Pending(t *testing.T) {
	body := []byte(`{"ok":true,"account":{"oauth_status":"pending","reauth_required":false}}`)
	p, err := ParseNanShareStatus(&OAuthAPIResponse{Data: body})
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if p.Authorized == nil || *p.Authorized {
		t.Fatal("pending should map to authorized=false")
	}
}

func TestParseNanShareStatus_TopLevelAuthorizedPreferred(t *testing.T) {
	// 兼容旧格式：顶层 authorized 优先
	body := []byte(`{"authorized":true,"account":{"oauth_status":"revoked"}}`)
	p, err := ParseNanShareStatus(&OAuthAPIResponse{Data: body})
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if p.Authorized == nil || !*p.Authorized {
		t.Fatal("top-level authorized=true should win")
	}
}

func TestParseNanShareStatus_NoData(t *testing.T) {
	if _, err := ParseNanShareStatus(&OAuthAPIResponse{}); err == nil {
		t.Fatal("expected error for empty data")
	}
}
