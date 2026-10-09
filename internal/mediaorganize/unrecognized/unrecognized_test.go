package unrecognized_test

import (
	"strings"
	"testing"

	"litepan/internal/mediaorganize/unrecognized"
)

// 三种填法 + skip_action + 沿用已有位置，一共 5 个岔路口。
// 这一层是纯函数，所以下面每一条都不是「跑一次整理看看」，而是一张可穷举的表。

// TestThreeFillFormsResolveExactlyAsDocumented 验收 ②：三种填法完全符合。
func TestThreeFillFormsResolveExactlyAsDocumented(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dir     string
		want    unrecognized.UnrecognizedTargetKind
		wantDir string // Path 或 DirName
	}{
		{
			name:    "以斜杠开头落绝对路径",
			dir:     "/media/未识别",
			want:    unrecognized.TargetAbsolute,
			wantDir: "/media/未识别",
		},
		{
			name:    "纯目录名落整理目录下一级分类",
			dir:     "未识别",
			want:    unrecognized.TargetRelative,
			wantDir: "未识别",
		},
		{
			name:    "留空留在源目录",
			dir:     "",
			want:    unrecognized.TargetKeep,
			wantDir: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := unrecognized.ResolveUnrecognizedTarget(unrecognized.Options{
				SkipAction:      unrecognized.UnrecognizedMove,
				UnrecognizedDir: tc.dir,
				TargetDirectory: "/media/整理",
				Category:        "电影",
			})
			if got.Kind != tc.want {
				t.Fatalf("形态 = %q，期望 %q（填法 %q）", got.Kind, tc.want, tc.dir)
			}
			actual := got.Path
			if got.Kind == unrecognized.TargetRelative {
				actual = got.DirName
			}
			if actual != tc.wantDir {
				t.Fatalf("目标 = %q，期望 %q", actual, tc.wantDir)
			}
			if got.KeepSource != (tc.want == unrecognized.TargetKeep) {
				t.Fatalf("KeepSource = %v，与形态 %q 不一致", got.KeepSource, got.Kind)
			}
			if got.SkipReason == "" {
				t.Fatal("每种形态都要有给用户看的说法，否则计划里只有一行裸 relocate")
			}
		})
	}
}

// TestSkipActionOverridesTheDirectory 关键一步：skip_action=keep 时不看目录填法。
//
// 这条最容易被「后来优化」掉。三种填法都是「keep 之后去哪」的备选，
// 而 keep 的定义就是不去 —— 开了 keep 还把文件移走，用户会发现自己的
// 「留在源目录」设置完全不起作用。
func TestSkipActionOverridesTheDirectory(t *testing.T) {
	for _, dir := range []string{"未识别", "/media/未识别", "", "随便写的名字"} {
		got := unrecognized.ResolveUnrecognizedTarget(unrecognized.Options{
			SkipAction:      unrecognized.UnrecognizedKeep,
			UnrecognizedDir: dir,
			TargetDirectory: "/media",
		})
		if got.Kind != unrecognized.TargetKeep || !got.KeepSource {
			t.Fatalf("skip_action=keep 但目录填了 %q 时给出了 %q —— keep 的意思是不去", dir, got.Kind)
		}
	}
	// 空动作值等同 keep（不能因为配置为空就把文件移走）。
	got := unrecognized.ResolveUnrecognizedTarget(unrecognized.Options{SkipAction: "", UnrecognizedDir: "未识别"})
	if got.Kind != unrecognized.TargetKeep {
		t.Fatalf("skip_action 为空时给出了 %q，期望 keep", got.Kind)
	}
}

// TestExistingLocationWinsOnlyWhenSwitchIsOn 沿用已有位置的优先级与开关。
func TestExistingLocationWinsOnlyWhenSwitchIsOn(t *testing.T) {
	base := unrecognized.Options{
		SkipAction:      unrecognized.UnrecognizedMove,
		UnrecognizedDir: "未识别",
		TargetDirectory: "/media/整理",
	}
	// 开关关着：已有的位置不该影响结果（反查根本不该发出去）。
	off := base
	off.FollowExistingLocation = false
	off.ExistingLocation = "/media/影视/某剧 (2019)"
	if got := unrecognized.ResolveUnrecognizedTarget(off); got.Kind != unrecognized.TargetRelative {
		t.Fatalf("开关关闭却用了已有位置：%q", got.Kind)
	}
	// 开关开着且查到：优先用已有位置。
	on := base
	on.FollowExistingLocation = true
	on.ExistingLocation = "/media/影视/某剧 (2019)"
	got := unrecognized.ResolveUnrecognizedTarget(on)
	if got.Kind != unrecognized.TargetAbsolute || got.Path != "/media/影视/某剧 (2019)" {
		t.Fatalf("开关开启却没用已有位置：%+v", got)
	}
	// 开关开着但查不到：落回兜底目录，不留源目录。
	miss := base
	miss.FollowExistingLocation = true
	miss.ExistingLocation = ""
	if got := unrecognized.ResolveUnrecognizedTarget(miss); got.Kind != unrecognized.TargetRelative {
		t.Fatalf("反查未命中时给出了 %q，期望落回兜底目录", got.Kind)
	}
}

// TestDotDotCannotEscapeTheConfiguredDirectory 验收：填的路径不能逃出去。
func TestDotDotCannotEscapeTheConfiguredDirectory(t *testing.T) {
	for _, tc := range []struct{ name, dir string }{
		{"往上跳一级", "/media/整理/../../../etc"},
		{"混合写法", "/media/../.."},
		{"直接就是 ..", ".."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := unrecognized.ResolveUnrecognizedTarget(unrecognized.Options{
				SkipAction:      unrecognized.UnrecognizedMove,
				UnrecognizedDir: tc.dir,
			})
			// `..` 这种纯目录名会被清洗成无效（不是路径形态），留源目录。
			if got.Kind == unrecognized.TargetAbsolute && strings.Contains(got.Path, "..") {
				t.Fatalf("判定给出了逃逸路径 %q", got.Path)
			}
			if got.Kind != unrecognized.TargetKeep && got.Kind != unrecognized.TargetRelative && strings.Contains(got.DirName, "..") {
				t.Fatalf("判定给出了逃逸目录名 %q", got.DirName)
			}
		})
	}
}

// TestAbsoluteRootIsRejected 根目录当兜底目录 = 整盘皆兜底。
func TestAbsoluteRootIsRejected(t *testing.T) {
	for _, dir := range []string{"/", "///", " / "} {
		got := unrecognized.ResolveUnrecognizedTarget(unrecognized.Options{
			SkipAction:      unrecognized.UnrecognizedMove,
			UnrecognizedDir: dir,
		})
		if got.Kind != unrecognized.TargetKeep {
			t.Fatalf("兜底目录 %q 被判成了 %q，根目录会让整理失去任何约束", dir, got.Kind)
		}
	}
}

// TestRelativePathNeverEmitsStraySlashes 拼相对路径时不能出现 /未识别 或 //未识别。
func TestRelativePathNeverEmitsStraySlashes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		targetDir  string
		category   string
		wantPath   string
		wantPrefix string
	}{
		{"齐活", "/media/整理", "电影", "/media/整理/电影/未识别", "/media/整理"},
		{"没有分类目录", "/media/整理", "", "/media/整理/未识别", "/media/整理"},
		{"两个都没有", "", "", "未识别", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := unrecognized.UnrecognizedTarget{Kind: unrecognized.TargetRelative, DirName: "未识别"}
			got := unrecognized.RelativeTargetPath(target, tc.targetDir, tc.category)
			if got != tc.wantPath {
				t.Fatalf("拼出 %q，期望 %q", got, tc.wantPath)
			}
			if strings.HasPrefix(got, "/") && tc.wantPrefix == "" {
				t.Fatalf("没有整理目录时拼出了绝对路径 %q", got)
			}
		})
	}
	// 非相对形态不该被拼成路径。
	abs := unrecognized.UnrecognizedTarget{Kind: unrecognized.TargetAbsolute, Path: "/media/x"}
	if got := unrecognized.RelativeTargetPath(abs, "/a", "b"); got != "" {
		t.Fatalf("绝对形态被拼成了相对路径 %q", got)
	}
}

// TestDirectoryNameCannotSmugglePathSeparators 网盘按字面判同名，
// 一个带斜杠的「目录名」会静默变成两级目录。
func TestDirectoryNameCannotSmugglePathSeparators(t *testing.T) {
	for _, tc := range []struct{ name, dir, want string }{
		{"带斜杠", "未识别/乱", "未识别_乱"},
		{"带反斜杠", `未\识别`, "未_识别"},
		{"前后空白", "  未识别  ", "未识别"},
		{"只是点", ".", ""},
		{"两个点", "..", ""},
		{"全是控制字符", "\x01\x02", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := unrecognized.ResolveUnrecognizedTarget(unrecognized.Options{
				SkipAction:      unrecognized.UnrecognizedMove,
				UnrecognizedDir: tc.dir,
			})
			if tc.want == "" {
				if got.Kind != unrecognized.TargetKeep {
					t.Fatalf("填了 %q 却给出了 %q，应判为无效", tc.dir, got.Kind)
				}
				return
			}
			if got.Kind != unrecognized.TargetRelative || got.DirName != tc.want {
				t.Fatalf("填了 %q → %+v，期望目录名 %q", tc.dir, got, tc.want)
			}
		})
	}
}

// TestEveryBranchHasADistinctReason 每一支都要有一句给用户看的话。
//
// 计划里 relocate 的 Reason 是用户判断「它想干嘛」的唯一线索；
// 空白 Reason 会让一屏动作里混着看不懂的移动。
func TestEveryBranchHasADistinctReason(t *testing.T) {
	seen := map[string]string{}
	for _, tc := range []struct {
		name string
		in   unrecognized.Options
	}{
		{"keep", unrecognized.Options{SkipAction: unrecognized.UnrecognizedKeep}},
		{"move 留空目录", unrecognized.Options{SkipAction: unrecognized.UnrecognizedMove}},
		{"move 纯目录名", unrecognized.Options{SkipAction: unrecognized.UnrecognizedMove, UnrecognizedDir: "未识别"}},
		{"move 绝对路径", unrecognized.Options{SkipAction: unrecognized.UnrecognizedMove, UnrecognizedDir: "/media/未识别"}},
		{"沿用已有位置", unrecognized.Options{SkipAction: unrecognized.UnrecognizedMove, FollowExistingLocation: true, ExistingLocation: "/media/影视/某剧"}},
	} {
		got := unrecognized.ResolveUnrecognizedTarget(tc.in)
		if got.SkipReason == "" {
			t.Errorf("%s：没有说法", tc.name)
			continue
		}
		if prev, dup := seen[got.SkipReason]; dup {
			t.Errorf("%s 与 %s 的说法相同（%q）", tc.name, prev, got.SkipReason)
			continue
		}
		seen[got.SkipReason] = tc.name
	}
}
