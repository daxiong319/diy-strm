package tgchannel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 真实频道页面回归：regeng123 的分享链接写在正文行内 <a href>（而非键盘按钮），
// 曾因 parseMessageDiv 对正文 div 提前 return 导致该频道永远提取不到链接
// （症状：回溯翻页正常、关键词命中、但「链接 0 个，跳过 0 次」，资源帖被静默丢弃）。
// fixture 为服务器实抓的 t.me/s/regeng123 页面（testdata 不参与编译）。

func truncateStr(s string, n int) string {
	b := []byte(s)
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return s
}

func parseFixturePage(t *testing.T, name string) []ChannelPost {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读取 fixture 失败：%v", err)
	}
	posts, err := parseChannelHTML(string(data))
	if err != nil {
		t.Fatalf("解析 fixture 失败：%v", err)
	}
	return posts
}

func TestParseRealRegengPage1(t *testing.T) {
	posts := parseFixturePage(t, "regeng123_page1.html")
	if len(posts) != 20 {
		t.Fatalf("应解析出 20 帖，实际 %d", len(posts))
	}
	// 帖 17288：链接写在正文 <a href="https://www.123865.com/s/Oqtgvd-39Mdh?pwd=YSRG">
	var p17288 *ChannelPost
	for i := range posts {
		if posts[i].PostID == "17288" {
			p17288 = &posts[i]
		}
	}
	if p17288 == nil {
		t.Fatal("缺少帖 17288")
	}
	if len(p17288.Links) == 0 {
		t.Fatalf("帖 17288 应提取到 123 链接（正文行内链接），文本片段：%s", truncateStr(p17288.Text, 120))
	}
	want := "https://www.123pan.com/s/Oqtgvd-39Mdh"
	found := false
	for _, l := range p17288.Links {
		if l.Type == "123" && strings.HasPrefix(l.URL, want) {
			if l.Pwd != "YSRG" {
				t.Fatalf("提取码应为 YSRG，实际 %q", l.Pwd)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("帖 17288 未提取到 Oqtgvd-39Mdh 链接，实际：%+v", p17288.Links)
	}
}

func TestParseRealRegengPage3TargetPost(t *testing.T) {
	posts := parseFixturePage(t, "regeng123_page3.html")
	// 找到含「飞到我心上」的资源帖：必须能提取到 123 链接（用户订阅场景的直接回归）
	hits := 0
	for _, p := range posts {
		if !strings.Contains(p.Text, "飞到我心上") {
			continue
		}
		hits++
		has123 := false
		for _, l := range p.Links {
			if l.Type == "123" {
				has123 = true
			}
		}
		if !has123 {
			t.Fatalf("帖 %s 命中关键词但未提取到 123 链接（正文行内链接丢失回归），文本片段：%s", p.PostID, truncateStr(p.Text, 150))
		}
	}
	if hits == 0 {
		t.Fatal("fixture 中应存在「飞到我心上」帖")
	}
}
