package playback

import (
	"testing"

	"litepan/internal/domain"
)

// TestPickActionForceRedirect 钉住 T17 的 ForceRedirect 语义。
//
// 顺序和两个例外是这里全部的难点：
//   - ForceProxy 必须仍然赢 —— 用户勾了「强制流代理」就说明他明确不要 302，
//     设置页里 302 开关不该反过来覆盖显式意图；
//   - 驱动自己声明 ForceProxy 时也不能 302（它的 URL 往往不是可直连的播放地址）；
//   - 网盘没给 URL（只有 LocalPath）时 302 出去会指向空地址，必须退回流代理。
func TestPickActionForceRedirect(t *testing.T) {
	cases := []struct {
		name   string
		mode   domain.DownloadMode
		link   domain.DownloadInfo
		intent Intent
		want   Action
	}{
		{
			name:   "显式选择 302 且网盘给了直链 → 302",
			mode:   domain.DownloadProxy,
			link:   domain.DownloadInfo{URL: "https://cdn.example/x.mkv"},
			intent: Intent{ForceRedirect: true},
			want:   ActionRedirect,
		},
		{
			name:   "驱动判成 redirect 时 302（原有行为不变）",
			mode:   domain.DownloadRedirect,
			link:   domain.DownloadInfo{URL: "https://cdn.example/x.mkv"},
			intent: Intent{},
			want:   ActionRedirect,
		},
		{
			name:   "没开 302 就沿用驱动判定（流代理）",
			mode:   domain.DownloadProxy,
			link:   domain.DownloadInfo{URL: "https://cdn.example/x.mkv"},
			intent: Intent{},
			want:   ActionStream,
		},
		{
			name:   "ForceProxy 优先于 ForceRedirect",
			mode:   domain.DownloadRedirect,
			link:   domain.DownloadInfo{URL: "https://cdn.example/x.mkv"},
			intent: Intent{ForceRedirect: true, ForceProxy: true},
			want:   ActionStream,
		},
		{
			name:   "驱动声明 ForceProxy 时不得 302",
			mode:   domain.DownloadRedirect,
			link:   domain.DownloadInfo{URL: "https://cdn.example/x.mkv", ForceProxy: true},
			intent: Intent{ForceRedirect: true},
			want:   ActionStream,
		},
		{
			name:   "没有直链（只有本地路径）时退回流代理",
			mode:   domain.DownloadProxy,
			link:   domain.DownloadInfo{LocalPath: "/data/x.mkv"},
			intent: Intent{ForceRedirect: true},
			want:   ActionStream,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PickAction(tc.mode, tc.link, tc.intent); got != tc.want {
				t.Fatalf("PickAction = %v，期望 %v（mode=%v link=%+v intent=%+v）",
					got, tc.want, tc.mode, tc.link, tc.intent)
			}
		})
	}
}
