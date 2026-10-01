package pan123open

import "testing"

func TestParse123ShareURL(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		explicit string
		wantKey  string
		wantPwd  string
	}{
		{"标准分享页", "https://www.123pan.com/s/AbCdEf", "", "AbCdEf", ""},
		{"带html后缀", "https://www.123pan.com/s/AbCdEf.html", "", "AbCdEf", ""},
		{"带查询提取码", "https://www.123pan.com/s/AbCdEf?pwd=1234", "", "AbCdEf", "1234"},
		{"带文本提取码", "https://www.123pan.com/s/AbCdEf 提取码:1234", "", "AbCdEf", "1234"},
		{"中文冒号提取码", "https://www.123pan.com/s/AbCdEf 提取码：abcd", "", "AbCdEf", "abcd"},
		{"显式提取码优先", "https://www.123pan.com/s/AbCdEf?pwd=1111", "2222", "AbCdEf", "2222"},
		{"无协议", "www.123pan.com/s/AbCdEf", "", "AbCdEf", ""},
		{"空链接", "", "", "", ""},
		{"前后有文字", "分享给你 https://www.123pan.com/s/ZzYyXx 快去", "", "ZzYyXx", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, pwd := parse123ShareURL(tc.raw, tc.explicit)
			if key != tc.wantKey {
				t.Errorf("shareKey = %q, want %q", key, tc.wantKey)
			}
			if pwd != tc.wantPwd {
				t.Errorf("sharePwd = %q, want %q", pwd, tc.wantPwd)
			}
		})
	}
}
