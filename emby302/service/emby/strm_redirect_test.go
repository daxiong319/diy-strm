package emby

import "testing"

func TestParseStrmPointer(t *testing.T) {
	cases := []struct {
		in      string
		kind    string
		payload string
		ok      bool
	}{
		{"play115://abc123", "play115", "abc123", true},
		{"play115:abc123", "play115", "abc123", true},
		{"/play115/abc123", "play115", "abc123", true},
		{"play115://abc123?path=%2Fvideo", "play115", "abc123?path=%2Fvideo", true},
		{"playgy://12345", "playgy", "12345", true},
		{"playgy:12345", "playgy", "12345", true},
		{"/playgy/12345", "playgy", "12345", true},
		{"play123://12345", "play123", "12345", true},
		{"play123:12345", "play123", "12345", true},
		{"play115share://share123", "play115share", "share123", true},
		{"http://127.0.0.1:12333/115/url/video.mkv?pickcode=abc", "", "", false},
		{"https://diy.strm.cn/pan123/url/video.mp4?pickcode=9", "", "", false},
		{"nfs:///mnt/strm/1.strm", "", "", false},
		{"/data/strm/1234.mkv", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		kind, payload, ok := parseStrmPointer(c.in)
		if ok != c.ok || kind != c.kind || payload != c.payload {
			t.Errorf("parseStrmPointer(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.in, kind, payload, ok, c.kind, c.payload, c.ok)
		}
	}
}
