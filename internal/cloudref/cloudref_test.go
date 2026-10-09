package cloudref

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// 用例里的库根一律走 /media 前缀，是为了让「前缀相同但不是子目录」这个
// 最容易写错的边界一眼可见：/media/影视 与 /media/影视2 只差最后一个字符。
func TestLibraryRootMatchesItselfAndSubdirectories(t *testing.T) {
	refs := DirRefs{LibraryRoots: []string{"/media/影视"}}

	cases := []struct {
		name string
		dir  string
		want bool
	}{
		{"根本身", "/media/影视", true},
		{"子目录", "/media/影视/2026", true},
		{"深层子目录", "/media/影视/2026/某剧/某集.mkv", true},
		{"同名兄弟目录", "/media/影视2", false},
		{"父目录", "/media", false},
		{"完全无关", "/downloads/影视", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveDirReferences(context.Background(), tc.dir, refs)
			if got.InLibrary != tc.want {
				t.Errorf("InLibrary(%q)=%v，期望 %v（提示：%s）", tc.dir, got.InLibrary, tc.want, got.Hint)
			}
		})
	}
}

// TestSiblingPrefixIsNotCovered 是本包最关键的一条：字符串前缀比较会把
// /media/影视2 判成「在 /media/影视 内」。它的症状是洗版把备份目录当成
// 媒体库的一部分 —— 界面写着「在媒体库内」，Emby 里却什么都没有，
// 而用户在删文件之前不会有任何察觉。
//
// 这里把目录名取成互为前缀的形态（影视 / 影视2 / 影视备份），
// 换成任何别的写法这条断言都会失去意义。
func TestSiblingPrefixIsNotCovered(t *testing.T) {
	refs := DirRefs{LibraryRoots: []string{"/media/影视"}}
	for _, dir := range []string{"/media/影视2", "/media/影视备份", "/media/影视-copy", "/media/影视 4K"} {
		got := ResolveDirReferences(context.Background(), dir, refs)
		if got.InLibrary {
			t.Errorf("%q 被判成在 /media/影视 内 —— 前缀比较把兄弟目录吃进来了", dir)
		}
		if got.Hint != hintNotInLibrary {
			t.Errorf("%q 的提示=%q，期望 %q", dir, got.Hint, hintNotInLibrary)
		}
	}
}

// TestRootSlashCoversEverything 根目录就是「全部」：
// 用户把库根配成 / 时，任何路径都该判定为在库内。
func TestRootSlashCoversEverything(t *testing.T) {
	refs := DirRefs{LibraryRoots: []string{"/"}}
	for _, dir := range []string{"/media/影视", "/downloads", "/"} {
		if !ResolveDirReferences(context.Background(), dir, refs).InLibrary {
			t.Errorf("库根为 / 时 %q 应判定在库内", dir)
		}
	}
	// 但空路径依然答不了这个问题：见 ResolveDirReferences 的注释。
	if got := ResolveDirReferences(context.Background(), "", refs); got.InLibrary {
		t.Errorf("空路径不该被判定在库内，实际 %+v", got)
	}
}

func TestNormalizePathEquivalentForms(t *testing.T) {
	refs := DirRefs{LibraryRoots: []string{"/media/影视"}}

	// 这些写法在人手上是同一个目录。用户在输入框里敲一个尾斜杠、
	// 或者从别处粘来一段带首尾空格的路径，都是常态；判定结果必须一致，
	// 否则同一个目录在「媒体库根」框里显示在库内、在「移动目标」框里
	// 显示不在，用户会以为系统出 bug。
	for _, dir := range []string{
		"/media/影视",
		"/media/影视/",
		"  /media/影视  ",
		"/media/影视////",
		"media/影视",         // 漏敲首斜杠
		"/media/./影视",      // 带当前目录
		"/media/影视/子目录/..", // 归一后仍是 /media/影视
		"/media//影视",       // 双斜杠
	} {
		got := ResolveDirReferences(context.Background(), dir, refs)
		if !got.InLibrary {
			t.Errorf("Normalize(%q) 后应判定在 /media/影视 内，实际提示 %q", dir, got.Hint)
		}
	}
}

func TestDotDotCannotEscapeRoot(t *testing.T) {
	refs := DirRefs{LibraryRoots: []string{"/media/影视"}}

	// `/media/影视/../影视2` 归一后是 `/media/影视2`，不在库内。
	// 如果哪天有人把归一换成朴素的前缀比较，这条就会变成「在库内」，
	// 而用户只是多打了一段 ../ —— 这就是目录逃逸。
	for _, dir := range []string{
		"/media/影视/../影视2",
		"/media/影视/../../media/影视2",
		"/media/影视/./../影视2",
	} {
		if got := ResolveDirReferences(context.Background(), dir, refs); got.InLibrary {
			t.Errorf("%q 用 .. 绕过了库根边界，实际提示 %q", dir, got.Hint)
		}
	}

	// 往上逃到根也逃不出 /media/影视：Clean 只会向上折，不会折穿根。
	if got := ResolveDirReferences(context.Background(), "/media/影视/../..", refs); got.InLibrary {
		t.Errorf("/media/影视/../.. 归一为 /，不应判定在 /media/影视 内")
	}
	// 反向：.. 能让一个看着在外面的目录落进库内，这是**应该**判定的，
	// 因为比较的是归一后的路径 —— 用户看到的也是归一后的那个目录。
	if !ResolveDirReferences(context.Background(), "/downloads/../media/影视", refs).InLibrary {
		t.Errorf("归一后确实落在库内的目录被判成不在库内")
	}
}

func TestNormalizePathRejectsUnusableInput(t *testing.T) {
	for _, raw := range []string{
		"",
		"   ",
		"\t\n ",
		"/media/影视\n/media/电影", // 粘贴多行
		"/media/影\x00视",        // NUL
		"/media/影视\x7f",
	} {
		if got := NormalizePath(raw); got != "" {
			t.Errorf("NormalizePath(%q)=%q，期望空串（不可用路径）", raw, got)
		}
	}
	// 不可用路径一条提示都不给。
	refs := DirRefs{LibraryRoots: []string{"/media/影视"}, MonitorSources: []string{"/watch"}}
	got := ResolveDirReferences(context.Background(), "  \n ", refs)
	if len(got.Hints) != 0 || got.Hint != "" || got.InLibrary || got.IsMonitorSource {
		t.Errorf("不可用路径应返回零结果，实际 %+v", got)
	}
}

func TestHintTextsAndTones(t *testing.T) {
	refs := DirRefs{
		LibraryRoots:   []string{"/media/影视"},
		MonitorSources: []string{"/watch/cas"},
	}

	// 两条都不满足 —— 两条都是 warn，主提示取第一条。
	got := ResolveDirReferences(context.Background(), "/downloads/tv", refs)
	if len(got.Hints) != 2 {
		t.Fatalf("两条配置都配了时应给两条提示，实际 %d 条：%+v", len(got.Hints), got.Hints)
	}
	if got.Hints[0].Text != hintNotInLibrary || got.Hints[0].Tone != ToneWarn {
		t.Errorf("媒体库提示=%+v，期望 %q/warn", got.Hints[0], hintNotInLibrary)
	}
	if got.Hints[1].Text != hintNotMonitor || got.Hints[1].Tone != ToneWarn {
		t.Errorf("源目录提示=%+v，期望 %q/warn", got.Hints[1], hintNotMonitor)
	}
	if got.Hint != hintNotInLibrary || got.Tone != ToneWarn {
		t.Errorf("主提示=%q/%q，期望 %q/warn", got.Hint, got.Tone, hintNotInLibrary)
	}
	if got.InLibrary || got.IsMonitorSource {
		t.Errorf("/downloads/tv 不应命中任何一条，实际 %+v", got)
	}

	// 都在：源目录这条是 ok，媒体库这条是 warn，所以主提示是媒体库那条。
	// （下面 TestMainHintPrefersTheOneThatSilentlyFails 专门覆盖反过来那种。）
	got = ResolveDirReferences(context.Background(), "/watch/cas/某剧", refs)
	if !got.IsMonitorSource || got.InLibrary {
		t.Errorf("/watch/cas/某剧 应命中源目录，实际 %+v", got)
	}
	if got.Hint != hintNotInLibrary || got.Tone != ToneWarn {
		t.Errorf("主提示=%q/%q，期望 warn 的 %q", got.Hint, got.Tone, hintNotInLibrary)
	}
	if got.Hints[1].Text != hintIsMonitor || got.Hints[1].Tone != ToneOK {
		t.Errorf("第二条提示=%+v，期望 %q/ok", got.Hints[1], hintIsMonitor)
	}

	// 两条都 ok：都在库里、也是源目录。
	both := ResolveDirReferences(context.Background(), "/watch/cas", DirRefs{
		LibraryRoots:   []string{"/watch"},
		MonitorSources: []string{"/watch/cas"},
	})
	if both.Tone != ToneOK || both.Hint != "这个目录在媒体库「媒体库」内" {
		t.Errorf("主提示=%q/%q，期望 in-library/ok", both.Hint, both.Tone)
	}
}

// TestMainHintPrefersTheOneThatSilentlyFails 「在媒体库内」常是 ok，
// 而真正会静默失败的是「不是源目录」。如果主提示只是取第一条，
// 用户在目标目录框里看到的就是一句「在媒体库内」，源目录那条 warn
// 排在后面等于没提示 —— 而那恰恰是这个功能要解决的主要坑。
func TestMainHintPrefersTheOneThatSilentlyFails(t *testing.T) {
	refs := DirRefs{
		LibraryRoots:   []string{"/media/影视"},
		MonitorSources: []string{"/watch/cas"},
	}
	got := ResolveDirReferences(context.Background(), "/media/影视/某剧", refs)
	if got.Tone != ToneWarn || got.Hint != hintNotMonitor {
		t.Errorf("主提示=%q/%q，期望 warn 的 %q（媒体库那条是 ok，不该当主提示）", got.Hint, got.Tone, hintNotMonitor)
	}
	// 但 ok 那条仍然要给出，界面上可以显示成中性状态。
	if len(got.Hints) != 2 || got.Hints[0].Tone != ToneOK {
		t.Errorf("两条提示都应保留，实际 %+v", got.Hints)
	}
}

// TestNoHintWhenNothingConfigured 是「不硬造概念」的落点。
//
// 没配媒体库根的时候，我们根本不知道用户有没有媒体库，报一句
// 「不在任何媒体库内」是编造的：用户会以为自己漏配了什么，
// 去设置里找一个不存在的开关。
//
// 「等效于没配」包含三种情况：切片为空、每项都是空白、每项都只有分隔符。
// 媒体库根存的是文本框内容，用户在框里按一下空格就属于后两种 ——
// 那时候报「不在任何媒体库内」是凭空捏造，而用户明明配过。
func TestNoHintWhenNothingConfigured(t *testing.T) {
	// ⚠️ 期望值必须是**字面量**，不能拿 cleanRoots 算 —— 那是被测函数自己。
	// 原先这里写的是 len(cleanRoots(...)) > 0，于是 cleanRoots 被改成
	// 不过滤空项时整条用例照样绿（变异实测），而它守的正是
	// 「配置里只有一个空格不许被当成配了媒体库」这条底线。
	for name, tc := range map[string]struct {
		refs       DirRefs
		hasLibrary bool
		hasMonitor bool
	}{
		"全空":        {DirRefs{}, false, false},
		"只有媒体库":     {DirRefs{LibraryRoots: []string{"/media/影视"}}, true, false},
		"只有源目录":     {DirRefs{MonitorSources: []string{"/watch/cas"}}, false, true},
		"媒体库根是空串":   {DirRefs{LibraryRoots: []string{""}}, false, false},
		"库根全是空白":    {DirRefs{LibraryRoots: []string{"  ", "\t"}}, false, false},
		"库根是换行":     {DirRefs{LibraryRoots: []string{"\n"}}, false, false},
		"源目录全是空白":   {DirRefs{MonitorSources: []string{"", " "}}, false, false},
		"两边都是空白":    {DirRefs{LibraryRoots: []string{" "}, MonitorSources: []string{""}}, false, false},
		"库根只带尾斜杠":   {DirRefs{LibraryRoots: []string{"/"}}, true, false},
		"库根是真目录加空白": {DirRefs{LibraryRoots: []string{"", "/media/影视"}}, true, false},
	} {
		t.Run(name, func(t *testing.T) {
			got := ResolveDirReferences(context.Background(), "/somewhere", tc.refs)
			if !tc.hasLibrary {
				for _, h := range got.Hints {
					if strings.Contains(h.Text, "媒体库") {
						t.Errorf("没配媒体库根却给出了媒体库提示：%q", h.Text)
					}
				}
			}
			if !tc.hasMonitor {
				for _, h := range got.Hints {
					if strings.Contains(h.Text, "源目录") {
						t.Errorf("没配源目录却给出了源目录提示：%q", h.Text)
					}
				}
			}
			// 等效于没配 ⇒ 一条提示都不出。
			if !tc.hasLibrary && !tc.hasMonitor && len(got.Hints) != 0 {
				t.Errorf("什么都没配却给出了提示：%+v", got.Hints)
			}
		})
	}
}

// TestLongestLibraryRootWins 配了 /media/影视 和 /media/影视/4K 时，
// 目录 /media/影视/4K/某剧 实际落在 4K 那个库里。报短名会让用户
// 去错的地方核对，而 4K 库多半就是没有这个文件的那个。
func TestLongestLibraryRootWins(t *testing.T) {
	refs := DirRefs{LibraryRoots: []string{"/media/影视", "/media/影视/4K"}}
	got := ResolveDirReferences(context.Background(), "/media/影视/4K/某剧", refs)
	if !got.InLibrary {
		t.Fatalf("应判定在库内，实际 %+v", got)
	}
	if got.LibraryName != libraryNameSingle {
		t.Errorf("库根没有自带名字时应给通用名 %q，实际 %q", libraryNameSingle, got.LibraryName)
	}
	// 短根也覆盖它（它确实是 /media/影视 的子目录），两条都在 InLibrary 上为真。
	if !ResolveDirReferences(context.Background(), "/media/影视/某剧", refs).InLibrary {
		t.Errorf("/media/影视/某剧 应判定在库内")
	}
}

// TestMatchLibraryPrefersLongestRoot 直接钉住「最长根」这条语义，
// 不用绕道 DTO 字段。
func TestMatchLibraryPrefersLongestRoot(t *testing.T) {
	name, ok := matchLibrary("/media/影视/4K/某剧", []string{"/media/影视", "/media/影视/4K"}, nil)
	if !ok {
		t.Fatalf("应命中，实际未命中")
	}
	if name != libraryNameSingle {
		t.Errorf("名字=%q，期望 %q", name, libraryNameSingle)
	}
	// 顺序反过来也要选最长的（不能只比较第一个）。
	name, ok = matchLibrary("/media/影视/4K/某剧", []string{"/media/影视/4K", "/media/影视"}, nil)
	if !ok || name != libraryNameSingle {
		t.Errorf("逆序输入命中=%v/%q，期望 true/%q", ok, name, libraryNameSingle)
	}
	if _, ok := matchLibrary("/media/影视2", []string{"/media/影视"}, nil); ok {
		t.Errorf("/media/影视2 不应命中 /media/影视")
	}
	if _, ok := matchLibrary("", []string{"/media/影视"}, nil); ok {
		t.Errorf("空路径不应命中任何根")
	}
	if _, ok := matchLibrary("/media/影视", nil, []string{"/media/影视"}); !ok {
		t.Errorf("EmbyLocations 里的路径同样算媒体库根")
	}
}

func TestMatchAny(t *testing.T) {
	roots := []string{"/watch/cas", "/watch/离线"}
	if !matchAny("/watch/cas/某剧", roots) {
		t.Errorf("子目录应命中")
	}
	if !matchAny("/watch/cas", roots) {
		t.Errorf("根本身应命中")
	}
	if matchAny("/watch/cas2", roots) {
		t.Errorf("兄弟目录不应命中")
	}
	if matchAny("/watch", roots) {
		t.Errorf("父目录不应命中")
	}
	if matchAny("/watch/cas", nil) {
		t.Errorf("空列表不应命中")
	}
	if matchAny("/watch/cas", []string{""}) {
		t.Errorf("空根不应命中")
	}
}

func TestSplitRoots(t *testing.T) {
	got := SplitRoots("/media/影视,/media/电影、/media/动漫；/media/音乐")
	want := []string{"/media/影视", "/media/电影", "/media/动漫", "/media/音乐"}
	if len(got) != len(want) {
		t.Fatalf("SplitRoots=%v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 项=%q，期望 %q", i, got[i], want[i])
		}
	}

	// 每项都要归一：尾斜杠、漏首斜杠、重复项都要处理掉。
	if got := SplitRoots("/media/影视/,media/影视,/media/影视"); len(got) != 1 || got[0] != "/media/影视" {
		t.Errorf("归一+去重失败：%v", got)
	}
	// 空格是合法目录名的一部分，不能拿来切。
	if got := SplitRoots("/media/我的 影视"); len(got) != 1 || got[0] != "/media/我的 影视" {
		t.Errorf("空格不该被当作分隔符：%v", got)
	}
	// 空输入与纯分隔符都切出空列表。
	if got := SplitRoots(""); len(got) != 0 {
		t.Errorf("空输入切出 %v", got)
	}
	if got := SplitRoots("，,、;；\n\r  "); len(got) != 0 {
		t.Errorf("纯分隔符切出 %v，期望空", got)
	}
}

// TestSplitRootsOutputIsUsableAsLibraryRoots SplitRoots 的产物要能直接
// 喂回 ResolveDirReferences —— 中间不再做归一，否则调用方会漏掉归一。
func TestSplitRootsOutputIsUsableAsLibraryRoots(t *testing.T) {
	refs := DirRefs{LibraryRoots: SplitRoots("/media/影视/, /media/电影")}
	got := ResolveDirReferences(context.Background(), "/media/影视/某剧", refs)
	if !got.InLibrary {
		t.Errorf("SplitRoots 的产物应可直接当库根用，实际 %+v", got)
	}
}

// TestResolveDirReferencesDoesNotMutateInputs 纯函数契约：
// 调用方传进来的切片不能被改写，否则第二次解析的结果会依赖第一次。
func TestResolveDirReferencesDoesNotMutateInputs(t *testing.T) {
	roots := []string{"/media/影视/"}
	srcs := []string{"/watch/cas/"}
	refs := DirRefs{LibraryRoots: roots, MonitorSources: srcs}

	first := ResolveDirReferences(context.Background(), "/media/影视/某剧", refs)
	second := ResolveDirReferences(context.Background(), "/media/影视/某剧", refs)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("两次解析结果不同：%+v vs %+v", first, second)
	}
	if roots[0] != "/media/影视/" || srcs[0] != "/watch/cas/" {
		t.Errorf("输入切片被改写：roots=%v srcs=%v", roots, srcs)
	}
}
