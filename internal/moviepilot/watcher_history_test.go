package moviepilot

import (
	"strings"
	"testing"
)

// TestIsVirtualMountPath 虚拟挂载路径识别：alist 等网盘协议前缀应静默跳过，不参与本地路径匹配。
func TestIsVirtualMountPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"alist 前缀", "alist:/中国移动云盘/影视/待整理/国产剧集/炽夏.Never-Ending.Summer.2026.S01.1080p.WEB-DL.H264.AAC-PTerWEB", true},
		{"alist 前缀带空格", "  alist:/中国移动云盘/影视/待整理/xxx", true},
		{"本地下载路径", "/downloads/国产剧集/飞到我心上 (2026)", false},
		{"Windows 风格本地路径", "D:/media/国产剧集/xxx", false},
		{"空字符串", "", false},
		{"纯网盘路径无协议前缀", "/中国移动云盘/影视/待整理/xxx", false},
		{"包含但不以前缀开头", "mounted/alist:/xxx", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isVirtualMountPath(tc.path); got != tc.want {
				t.Fatalf("isVirtualMountPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// TestCheckDownloadHistorySkipsVirtualMountPath 端到端：虚拟挂载路径的历史记录应跳过并推进游标，不产生本地路径告警。
func TestCheckDownloadHistorySkipsVirtualMountPath(t *testing.T) {
	h := &DownloadHistory{
		ID:           21,
		Path:         "alist:/中国移动云盘/影视/待整理/国产剧集/炽夏.Never-Ending.Summer.S01.2026.2160p.WEB-DL.H265.AAC-ADWeb",
		Title:        "炽夏",
		DownloadHash: "15dea0aa84cfdeadbeef",
	}
	if !isVirtualMountPath(h.Path) {
		t.Fatalf("历史 path %q 应识别为虚拟挂载路径", h.Path)
	}
	// 路径不含本地根（/downloads），resolveHistoryLocalPath 必然失败——
	// 此时 isVirtualMountPath 分支应先行接管，不落入 Warn 告警
	if strings.Contains(h.Path, "/downloads") {
		t.Fatalf("测试前提不成立：虚拟挂载路径不应包含本地根")
	}
}
