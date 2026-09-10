package controllers

import (
	"strings"
	"testing"

	embyclientrestgo "diy-strm/internal/embyclient-rest-go"
)

func TestParseNewItemCountFromTitle(t *testing.T) {
	cases := []struct {
		title string
		want  int
	}{
		{"13d504c54e81 将 3 项目添加到 师兄太稳健", 3},
		{"13d504c54e81 将 15 项目添加到 囧徒之预演告别", 15},
		{"无数量的标题", 0},
		{"", 0},
	}
	for _, tc := range cases {
		if got := parseNewItemCountFromTitle(tc.title); got != tc.want {
			t.Fatalf("parseNewItemCountFromTitle(%q) = %d，期望 %d", tc.title, got, tc.want)
		}
	}
}

func TestSummarizeReleaseGroups(t *testing.T) {
	got := summarizeReleaseGroups([]string{
		"/media/x/囧徒之预演告别.2026.S01E01.2160p.WEB-DL.H.265-Ocat.strm",
		"/media/x/囧徒之预演告别.2026.S01E02.2160p.WEB-DL.H.265-Ocat.strm",
		"/media/x/囧徒之预演告别.2026.S01E03.2160p.WEB-DL.H.265-AilMWeb.strm",
	})
	if !strings.Contains(got, "Ocat×2") || !strings.Contains(got, "AilMWeb") {
		t.Fatalf("发布组摘要 = %q，期望包含 Ocat×2 与 AilMWeb", got)
	}
	// 纯数字/含点的 token 应被过滤
	if got := summarizeReleaseGroups([]string{"/media/x/show.2026.S01E01.2160p.strm"}); got != "" {
		t.Fatalf("无发布组时应为空，实际 %q", got)
	}
}

func TestFormatBytesHuman(t *testing.T) {
	cases := []struct {
		size int64
		want string
	}{
		{512, "512 B"},
		{2048, "2.00 KB"},
		{5 * 1024 * 1024, "5.00 MB"},
		{3 * 1024 * 1024 * 1024, "3.00 GB"},
	}
	for _, tc := range cases {
		if got := formatBytesHuman(tc.size); got != tc.want {
			t.Fatalf("formatBytesHuman(%d) = %q，期望 %q", tc.size, got, tc.want)
		}
	}
}

func TestBuildMediaNotificationContent(t *testing.T) {
	detail := &embyclientrestgo.BaseItemDtoV2{
		Name:            "师兄太稳健",
		ProductionYear:  2026,
		CommunityRating: 7.2,
		Genres:          []string{"Fantasy", "Comedy"},
		ProviderIds:     map[string]string{"Tmdb": "272938"},
		Overview:        "测试简介",
	}
	content := buildMediaNotificationContent(detail, "📺 入库季集：S01E16-E18（新增 3 集）\n")
	for _, want := range []string{
		"师兄太稳健 (2026)",
		"🆔 TMDB：272938",
		"⭐ 评分：7.2",
		"🎭 类型：Fantasy, Comedy",
		"📺 入库季集：S01E16-E18（新增 3 集）",
		"⏰ 入库时间：",
		"📝 简介",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("通知正文缺少 %q，实际：\n%s", want, content)
		}
	}
}
