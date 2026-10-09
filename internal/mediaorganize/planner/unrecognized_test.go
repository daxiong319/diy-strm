package planner_test

import (
	"context"
	"strings"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/mediaorganize/moplan"
	"litepan/internal/mediaorganize/planner"
)

// 未识别兜底在计划期的集成测试。
//
// 纯判定层（unrecognized 包）已经测过「填什么得到什么形态」，这里测的是
// 「形态落到计划里长什么样」：动作为什么、顺序为什么、找不到时为什么不硬来。

// newUnrecognizedPlanner 建一个 move 型整理任务，跑一次真实 Build。
func newUnrecognizedPlanner(t *testing.T, fs *mockFS, tmdb planner.TMDBClient, rootID string, settings planner.Settings) (*moplan.Plan, *planner.Planner) {
	t.Helper()
	p := planner.New(
		context.Background(),
		fs,
		1,
		planner.TaskConfig{
			TargetDirectoryID: rootID,
			ActionType:        "move",
			MediaType:         "auto",
			UseTMDB:           false,
			Recursive:         true,
		},
		settings,
		"task-unrecognized",
		tmdb,
		func(string) {},
		nil,
		func() error { return nil },
	)
	plan, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	return plan, p
}

// fallbackRelocates 挑出兜底产生的移动动作。
//
// 注意兜底的 ensure_dir 由共用的 ensureDirAction 生成，不带 fallback 标记，
// 所以「为了落点而建的目录」要靠 relocate 的 TargetParentID 引用链回溯，
// 不能只按 metadata 过滤 —— 否则会把「目录没建」这种最严重的缺陷漏掉。
func fallbackRelocates(plan *moplan.Plan) []moplan.PlanAction {
	var out []moplan.PlanAction
	for _, a := range plan.Actions {
		if a.Metadata["unrecognized_fallback"] == true && a.Kind == moplan.ActionKindRelocate {
			out = append(out, a)
		}
	}
	return out
}

// ensureChain 沿 TargetParentID 引用回溯这条路径上所有 ensure_dir，
// 由外到内（根目录在前）。这一步同时验证了顺序：父目录必须排在子目录之前。
func ensureChain(plan *moplan.Plan, ref string) []moplan.PlanAction {
	var chain []moplan.PlanAction
	for strings.HasPrefix(ref, "ref:") {
		id := ref[4:]
		idx := -1
		for i := range plan.Actions {
			if plan.Actions[i].ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		action := plan.Actions[idx]
		if action.Kind != moplan.ActionKindEnsureDir {
			break
		}
		chain = append(chain, action)
		ref = action.TargetParentID
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// TestUnrecognizedFallbackMovesFileIntoConfiguredFolder 纯目录名形态。
//
// 断言的是「用户能看到的那一屏」：先建目录、再移动、文件名不动。
func TestUnrecognizedFallbackMovesFileIntoConfiguredFolder(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root": {{ID: "junk", Name: "1234.mkv"}},
	}}
	plan, _ := newUnrecognizedPlanner(t, fs, nil, "root", planner.Settings{
		"mo_scrape_skip_action":      "move",
		"mo_scrape_unrecognized_dir": "未识别",
	})
	relocates := fallbackRelocates(plan)
	if len(relocates) != 1 {
		t.Fatalf("期望 1 个兜底移动，实际 %d（actions=%+v skipped=%+v）", len(relocates), plan.Actions, plan.Skipped)
	}
	move := relocates[0]
	if move.SourceID != "junk" {
		t.Fatalf("移动的源文件不对：%q", move.SourceID)
	}
	if move.TargetName != "1234.mkv" {
		t.Fatalf("未识别文件不该改名，实际改成 %q", move.TargetName)
	}
	if move.Reason == "" {
		t.Fatal("兜底移动必须带说法，否则预览里只有一行看不懂的移动")
	}
	chain := ensureChain(plan, move.TargetParentID)
	if len(chain) != 1 || chain[0].TargetName != "未识别" {
		t.Fatalf("应先确保「未识别」目录存在，实际链路 %+v", chain)
	}
	if move.TargetParentID != "ref:"+chain[0].ID {
		t.Fatalf("移动应指向新建目录的引用，实际 %q", move.TargetParentID)
	}
	// 顺序：建目录在移动之前，否则执行期解析 ref 会找不到父目录。
	if idxOf(plan, chain[0].ID) > idxOf(plan, move.ID) {
		t.Fatal("ensure_dir 必须排在兜底移动之前")
	}
	// 仍然记 needs_match：用户匹配之后还要再整理一次。
	if !hasNeedsMatchWithFallback(plan) {
		t.Fatalf("兜底之后仍应记 needs_match，实际 diagnostics=%+v", plan.Diagnostics)
	}
	// 兜底接管之后不应再留下「无法识别」的 skip。
	for _, s := range plan.Skipped {
		if reason, _ := s["reason"].(string); strings.Contains(reason, "无法识别") {
			t.Fatalf("兜底接管后不应再把文件 skip 掉：%+v", s)
		}
	}
}

// TestUnrecognizedFallbackKeepsFileWhenSkipActionIsKeep keep 时一切照旧。
func TestUnrecognizedFallbackKeepsFileWhenSkipActionIsKeep(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root": {{ID: "junk", Name: "1234.mkv"}},
	}}
	plan, _ := newUnrecognizedPlanner(t, fs, nil, "root", planner.Settings{
		"mo_scrape_skip_action":      "keep",
		"mo_scrape_unrecognized_dir": "未识别",
	})
	if relocates := fallbackRelocates(plan); len(relocates) != 0 {
		t.Fatalf("skip_action=keep 不应移动任何文件，实际 %+v", relocates)
	}
	if !skippedWithReason(plan, "无法识别") {
		t.Fatalf("应保留原有的 skip，实际 skipped=%+v", plan.Skipped)
	}
}

// TestUnrecognizedFallbackKeepsFileOnRenameOnlyTask rename-only 任务的约定是
// 「只改名不动位置」，所以不擅自替用户改动作类型。
func TestUnrecognizedFallbackKeepsFileOnRenameOnlyTask(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root": {{ID: "junk", Name: "1234.mkv"}},
	}}
	p := planner.New(
		context.Background(),
		fs,
		1,
		planner.TaskConfig{
			TargetDirectoryID: "root",
			ActionType:        "rename",
			MediaType:         "auto",
			Recursive:         true,
		},
		planner.Settings{
			"mo_scrape_skip_action":      "move",
			"mo_scrape_unrecognized_dir": "未识别",
		},
		"task-unrecognized",
		nil,
		func(string) {},
		nil,
		func() error { return nil },
	)
	plan, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	if relocates := fallbackRelocates(plan); len(relocates) != 0 {
		t.Fatalf("rename-only 任务不应产生移动，实际 %+v", relocates)
	}
}

// TestUnrecognizedAbsolutePathResolvesToExistingDir 绝对路径形态。
func TestUnrecognizedAbsolutePathResolvesToExistingDir(t *testing.T) {
	// 用户填的绝对路径是从网盘根算起的，所以 mock 里 "" 就是网盘根，
	// 里面挂着整理目录和 /media/杂项 两级真实目录。
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"": {
			{ID: "root", Name: "整理", IsDir: true},
			{ID: "media", Name: "media", IsDir: true},
		},
		"root": {
			{ID: "junk", Name: "1234.mkv"},
		},
		"media": {{ID: "misc", Name: "杂项", IsDir: true}},
	}}
	plan, _ := newUnrecognizedPlanner(t, fs, nil, "root", planner.Settings{
		"mo_scrape_skip_action":      "move",
		"mo_scrape_unrecognized_dir": "/media/杂项",
	})
	relocates := fallbackRelocates(plan)
	if len(relocates) != 1 {
		t.Fatalf("期望 1 个兜底移动，实际 %d（actions=%+v）", len(relocates), plan.Actions)
	}
	if relocates[0].TargetParentID != "misc" {
		t.Fatalf("绝对路径应指向已存在的目录 ID，实际 %q", relocates[0].TargetParentID)
	}
	// 目录已存在时不该再发 ensure_dir（执行期会幂等，但多一条噪音动作）。
	for _, a := range plan.Actions {
		if a.Kind == moplan.ActionKindEnsureDir {
			t.Fatalf("目标目录已存在，不应产生 ensure_dir：%+v", a)
		}
	}
}

// TestUnrecognizedAbsolutePathMissingDoesNotSilentlyRedirect 用户填了一个
// 真实网盘上不存在的绝对路径 —— 宁可留在源目录，也不要悄悄落到别处。
func TestUnrecognizedAbsolutePathMissingDoesNotSilentlyRedirect(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root": {{ID: "junk", Name: "1234.mkv"}},
	}}
	plan, _ := newUnrecognizedPlanner(t, fs, nil, "root", planner.Settings{
		"mo_scrape_skip_action":      "move",
		"mo_scrape_unrecognized_dir": "/media/根本不存在",
	})
	if relocates := fallbackRelocates(plan); len(relocates) != 0 {
		t.Fatalf("路径不存在时不应产生移动，实际 %+v", relocates)
	}
	for _, a := range plan.Actions {
		if a.Kind == moplan.ActionKindRelocate && a.SourceID == "junk" {
			t.Fatalf("路径不存在时不应把文件搬到别处：%+v", a)
		}
	}
}

// TestUnrecognizedFallbackFollowsEmbyExistingLocation 沿用已有位置。
//
// 这里刻意不注入反查器（embyLookup=nil）：离线时开关打开也必须安静落回
// 兜底目录，而不是把文件留在源目录或报错。
func TestUnrecognizedFallbackFollowsEmbyExistingLocation(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root": {{ID: "junk", Name: "1234.mkv"}},
	}}
	plan, _ := newUnrecognizedPlanner(t, fs, nil, "root", planner.Settings{
		"mo_scrape_skip_action":              "move",
		"mo_scrape_unrecognized_dir":         "未识别",
		"mo_scrape_follow_existing_location": true,
	})
	relocates := fallbackRelocates(plan)
	if len(relocates) != 1 {
		t.Fatalf("反查器缺席时应落回兜底目录，实际 %d 个移动", len(relocates))
	}
	chain := ensureChain(plan, relocates[0].TargetParentID)
	if len(chain) != 1 || chain[0].TargetName != "未识别" {
		t.Fatalf("应落回「未识别」目录，实际链路 %+v", chain)
	}
}

func idxOf(plan *moplan.Plan, id string) int {
	for i, a := range plan.Actions {
		if a.ID == id {
			return i
		}
	}
	return -1
}

// ——— 小工具 ———

func hasNeedsMatchWithFallback(plan *moplan.Plan) bool {
	raw, ok := plan.Diagnostics["needs_match"]
	if !ok {
		return false
	}
	list, ok := raw.([]map[string]any)
	if !ok {
		return false
	}
	for _, entry := range list {
		if entry["unrecognized_fallback"] == true {
			return true
		}
	}
	return false
}

func skippedWithReason(plan *moplan.Plan, want string) bool {
	for _, s := range plan.Skipped {
		if reason, _ := s["reason"].(string); strings.Contains(reason, want) {
			return true
		}
	}
	return false
}
