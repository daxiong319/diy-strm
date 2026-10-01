package cloud139

import "testing"

// 真实分享链接形如 https://www.139.com/w/i/{linkID}?pwd=xxxx
// 或 https://yun.139.com/shareweb/#/w/i/{linkID}
func TestParse139ShareURL(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		explicit   string
		wantID     string
		wantPasswd string
	}{
		{"标准分享页", "https://www.139.com/w/i/abcd1234", "", "abcd1234", ""},
		{"shareweb形式", "https://yun.139.com/shareweb/#/w/i/abcd1234", "", "abcd1234", ""},
		{"带查询提取码", "https://www.139.com/w/i/abcd1234?pwd=x8y9", "", "abcd1234", "x8y9"},
		{"带其它查询参数", "https://www.139.com/w/i/abcd1234?from=tg&pwd=x8y9", "", "abcd1234", "x8y9"},
		{"带文本提取码", "https://www.139.com/w/i/abcd1234 提取码:x8y9", "", "abcd1234", "x8y9"},
		{"中文冒号提取码", "https://www.139.com/w/i/abcd1234 提取码：x8y9", "", "abcd1234", "x8y9"},
		{"显式提取码优先", "https://www.139.com/w/i/abcd1234?pwd=1111", "2222", "abcd1234", "2222"},
		{"末端斜杠", "https://www.139.com/w/i/abcd1234/", "", "abcd1234", ""},
		{"空链接", "", "", "", ""},
		{"前后有文字", "分享给你 https://www.139.com/w/i/abcd1234 快去", "", "abcd1234", ""},
		{"中文标点结尾", "链接：https://www.139.com/w/i/abcd1234。", "", "abcd1234", ""},
		{"纯linkID", "abcd1234", "", "abcd1234", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, passwd := parse139ShareURL(tc.raw, tc.explicit)
			if id != tc.wantID {
				t.Errorf("linkID = %q, want %q", id, tc.wantID)
			}
			if passwd != tc.wantPasswd {
				t.Errorf("passwd = %q, want %q", passwd, tc.wantPasswd)
			}
		})
	}
}
