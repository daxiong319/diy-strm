package mediaupgrade

import (
	"testing"

	"litepan/internal/moviepilot"
)

func TestSlotKeySeparatesEveryDimension(t *testing.T) {
	base := Slot{
		Resolution: "2160p", Codec: "h265", Group: "ptp",
		Audio: "6ch", Subtitle: "nosub", Container: "mkv",
	}
	baseKey := base.Key()

	// 六个维度逐个改一个，槽位键都必须跟着变。
	// 只要有一个维度没进键，就会出现「两个不该同槽的文件被配成新旧版本」。
	for name, mutate := range map[string]func(s *Slot){
		"分辨率": func(s *Slot) { s.Resolution = "1080p" },
		"编码":  func(s *Slot) { s.Codec = "h264" },
		"制作组": func(s *Slot) { s.Group = "other" },
		"音轨":  func(s *Slot) { s.Audio = "2ch" },
		"字幕":  func(s *Slot) { s.Subtitle = "sub" },
		"容器":  func(s *Slot) { s.Container = "mp4" },
	} {
		got := base
		mutate(&got)
		if got.Key() == baseKey {
			t.Errorf("改了%s槽位键却没变：%s", name, got.Key())
		}
	}
}

func TestSlotOfFromRealFileNames(t *testing.T) {
	cases := []struct {
		name  string
		file  string
		want  Slot
		label string
	}{
		{
			name: "常规 4K",
			file: "剧名.S01E01.2160p.WEB-DL.H265.mkv",
			want: Slot{Resolution: "2160p", Codec: "h265", Group: "?", Audio: "?", Subtitle: "nosub", Container: "mkv"},
		},
		{
			// 音轨只认引擎认得的音频标签（atmos/ddp/…），「6CH」这种写法解析不出来；
			// 制作组只认结尾的 -XXX / [XXX]。这里跟着引擎的实际口径写，不另立一套。
			name: "带制作组与音轨标签",
			file: "剧名.S01E01.2160p.HDR.Atmos-GRP.mkv",
			want: Slot{Resolution: "2160p", Codec: "?", Group: "grp", Audio: "8ch", Subtitle: "nosub", Container: "mkv"},
		},
		{
			name: "中文名带字幕标记",
			file: "剧名.S01E01.2160p.H265.简繁内挂.mkv",
			want: Slot{Resolution: "2160p", Codec: "h265", Group: "?", Audio: "?", Subtitle: "sub", Container: "mkv"},
		},
		{
			name: "解析不出质量",
			file: "剧名.S01E01.mkv",
			want: Slot{Resolution: "?", Codec: "?", Group: "?", Audio: "?", Subtitle: "nosub", Container: "mkv"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SlotOf(c.file, moviepilot.ParseQualityFromName(c.file))
			if got != c.want {
				t.Errorf("槽位是 %+v，期望 %+v", got, c.want)
			}
			if got.Label() == "" {
				t.Errorf("槽位展示名为空")
			}
		})
	}
}

func TestSlotOfNilQualityStillHasContainer(t *testing.T) {
	got := SlotOf("剧名.S01E01.mp4", nil)
	if got.Container != "mp4" {
		t.Errorf("nil 质量时容器应从文件名取到 mp4，实际 %q", got.Container)
	}
	if got.Resolution != slotUnknown || got.Codec != slotUnknown {
		t.Errorf("nil 质量时解析不出的维度应占位为 %q，实际 %+v", slotUnknown, got)
	}
}

// TestUnknownDimensionsShareOneSlot 有意为之：解析不出的维度落到同一个占位槽位，
// 于是它们会进入比较流程，再被「无可比维度」守卫拦下。
// 反过来（解析不出就当不同槽位）会让「没信息」变成无限跳过，把真问题掩盖掉。
func TestUnknownDimensionsShareOneSlot(t *testing.T) {
	a := SlotOf("剧名.S01E01.mkv", nil)
	b := SlotOf("剧名.S01E01[重制].mkv", nil)
	if a.Key() != b.Key() {
		t.Errorf("两个都没解析出质量的文件应落进同一槽位，实际 %q vs %q", a.Key(), b.Key())
	}
}

// TestSubtitleDetectionIsDeliberatelyNarrow 钉住「不认 sub / gb」。
//
// 这两个词在本仓的语料里都不是字幕标记：sub 会命中 subscribed，
// gb 会命中体积串（如 8GB）。宁可把带外置字幕的文件判成「无字幕」
// （两个都保留），也不要反过来。
func TestSubtitleDetectionIsDeliberatelyNarrow(t *testing.T) {
	cases := []struct {
		file string
		want string
	}{
		{"剧名.S01E01.2160p.H265.mkv", "nosub"},
		{"剧名.S01E01.2160p.H265.subscribed.mkv", "nosub"},
		{"剧名.S01E01.2160p.H265.8GB.mkv", "nosub"},
		{"剧名.S01E01.2160p.H265.简繁.mkv", "sub"},
		{"剧名.S01E01.2160p.H265.中字.mkv", "sub"},
		{"剧名.S01E01.2160p.H265.chs.mkv", "sub"},
		{"剧名.S01E01.2160p.H265.zh-CN.mkv", "sub"},
		{"剧名.S01E01.2160p.H265.CHT.mkv", "sub"},
		{"剧名.S01E01.2160p.H265.mandarin.mkv", "sub"},
		{"剧名.S01E01.2160p-H265-Big5.mkv", "sub"},
	}
	for _, c := range cases {
		if got := hasSubtitleToken(c.file); got != c.want {
			t.Errorf("%s 的字幕判定是 %q，期望 %q", c.file, got, c.want)
		}
	}
}

// TestSlotLabelSkipsUnknowns 展示名不该把一堆问号甩给用户。
func TestSlotLabelSkipsUnknowns(t *testing.T) {
	got := SlotOf("剧名.S01E01.mkv", nil).Label()
	if got != "nosub mkv" {
		t.Errorf("只有一个维度可展示时是 %q，期望 %q（sub/nosub 是事实判断，该展示）", got, "nosub mkv")
	}
	empty := Slot{}.Label()
	if empty != "未知槽位" {
		t.Errorf("空槽位的展示名是 %q", empty)
	}
}
