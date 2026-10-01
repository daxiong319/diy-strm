package cloud189

import (
	"encoding/json"
	"testing"
)

// TestParseCloud189FileDataExists 锁定 fileDataExists 的严格三态语义。
// 这是防止「协议异常被误判为未命中、进而触发真上传」的回归测试。
func TestParseCloud189FileDataExists(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want fileDataExistsState
	}{
		{"明确有数据", "1", cloud189DataExists},
		{"明确无数据", "0", cloud189DataAbsent},
		{"带空白的有数据", "  1  ", cloud189DataExists},
		{"带空白的无数据", " 0 ", cloud189DataAbsent},

		// 以下全部属于协议异常，必须与「明确无数据」区分开
		{"字段缺失（空串）", "", cloud189DataUnknown},
		{"只有空白", "   ", cloud189DataUnknown},
		{"null 归一化后的空串", "", cloud189DataUnknown},
		{"非法值二", "2", cloud189DataUnknown},
		{"非法值负数", "-1", cloud189DataUnknown},
		{"非法值布尔文本", "true", cloud189DataUnknown},
		{"乱码", "abc", cloud189DataUnknown},
		{"类似但不同", "01", cloud189DataUnknown},
		{"小数", "1.0", cloud189DataUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCloud189FileDataExists(tc.raw); got != tc.want {
				t.Fatalf("parseCloud189FileDataExists(%q) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

// TestParseCloud189InitFileDataExists 覆盖上传初始化路径的 any 归一化。
func TestParseCloud189InitFileDataExists(t *testing.T) {
	cases := []struct {
		name string
		raw  any
		want fileDataExistsState
	}{
		{"字符串有数据", "1", cloud189DataExists},
		{"字符串无数据", "0", cloud189DataAbsent},
		{"json 数字有数据", json.Number("1"), cloud189DataExists},
		{"json 数字无数据", json.Number("0"), cloud189DataAbsent},
		{"float 有数据", float64(1), cloud189DataExists},
		{"float 无数据", float64(0), cloud189DataAbsent},

		{"nil", nil, cloud189DataUnknown},
		{"非法 float", float64(2), cloud189DataUnknown},
		{"bool", true, cloud189DataUnknown},
		{"非法 json 数字", json.Number("7"), cloud189DataUnknown},
		{"结构体", struct{}{}, cloud189DataUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCloud189InitFileDataExists(tc.raw); got != tc.want {
				t.Fatalf("parseCloud189InitFileDataExists(%#v) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

// TestRapidCreateRespFileDataExistsMissing 确认 JSON 字段缺失时落在「协议异常」而非「未命中」。
// 真实场景：接口改版或网关截断响应，导致 fileDataExists 不再返回。
func TestRapidCreateRespFileDataExistsMissing(t *testing.T) {
	cases := []struct {
		name string
		body string
		want fileDataExistsState
	}{
		{"字段完全缺失", `{"uploadFileId":"up-1","fileCommitUrl":"http://x/y"}`, cloud189DataUnknown},
		{"显式 null", `{"uploadFileId":"up-1","fileDataExists":null}`, cloud189DataUnknown},
		{"数字 0", `{"uploadFileId":"up-1","fileDataExists":0}`, cloud189DataAbsent},
		{"数字 1", `{"uploadFileId":"up-1","fileDataExists":1}`, cloud189DataExists},
		{"字符串 0", `{"uploadFileId":"up-1","fileDataExists":"0"}`, cloud189DataAbsent},
		{"字符串 1", `{"uploadFileId":"up-1","fileDataExists":"1"}`, cloud189DataExists},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var resp rapidCreateResp
			if err := json.Unmarshal([]byte(tc.body), &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got := parseCloud189FileDataExists(resp.FileDataExists.String())
			if got != tc.want {
				t.Fatalf("fileDataExists=%q -> %d, want %d", resp.FileDataExists.String(), got, tc.want)
			}
			// 关键的负向断言：字段缺失绝不能被判为「明确无数据」。
			if tc.name == "字段完全缺失" && got == cloud189DataAbsent {
				t.Fatal("字段缺失被误判为「明确无数据」，会触发非预期真上传")
			}
		})
	}
}
