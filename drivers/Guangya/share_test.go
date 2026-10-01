package guangya

import "testing"

func TestParseGuangyaShareURL(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		explicit  string
		wantID    string
		wantCode  string
	}{
		{"标准分享页", "https://www.guangyapan.com/s/AbCdEf123", "", "AbCdEf123", ""},
		{"带html后缀", "https://www.guangyapan.com/s/AbCdEf123.html", "", "AbCdEf123", ""},
		{"带查询提取码", "https://www.guangyapan.com/s/AbCdEf123?pwd=1234", "", "AbCdEf123", "1234"},
		{"带文本提取码", "https://www.guangyapan.com/s/AbCdEf123 提取码:1234", "", "AbCdEf123", "1234"},
		{"中文冒号提取码", "https://www.guangyapan.com/s/AbCdEf123 提取码：abcd", "", "AbCdEf123", "abcd"},
		{"显式提取码优先", "https://www.guangyapan.com/s/AbCdEf123?pwd=1111", "2222", "AbCdEf123", "2222"},
		{"末端斜杠", "https://www.guangyapan.com/s/AbCdEf123/", "", "AbCdEf123", ""},
		{"空链接", "", "", "", ""},
		{"前后有文字", "分享给你 https://www.guangyapan.com/s/ZzYyXx 快去", "", "ZzYyXx", ""},
		{"中文标点结尾", "链接：https://www.guangyapan.com/s/ZzYyXx。", "", "ZzYyXx", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, code := parseGuangyaShareURL(tc.raw, tc.explicit)
			if id != tc.wantID {
				t.Errorf("shareID = %q, want %q", id, tc.wantID)
			}
			if code != tc.wantCode {
				t.Errorf("code = %q, want %q", code, tc.wantCode)
			}
		})
	}
}
