package embyrefresh

import (
	"reflect"
	"testing"
	"time"
)

func TestLibraryAndItemTaskKey(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"库级键", LibraryTaskKey("lib-1"), "library:lib-1"},
		{"条目键", ItemTaskKey("item-9"), "item:item-9"},
		{"库级键去空白", LibraryTaskKey(" lib-1 "), "library:lib-1"},
		{"条目键去空白", ItemTaskKey(" item-9 "), "item:item-9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("task_key=%q，期望 %q", tc.got, tc.want)
			}
		})
	}
}

func TestNormalizeItemIDs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"空输入", nil, nil},
		{"去空白去重并排序", []string{" b ", "a", "b", "", "  ", "c"}, []string{"a", "b", "c"}},
		{"全部为空白", []string{" ", ""}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeItemIDs(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("NormalizeItemIDs=%v，期望 %v", got, tc.want)
			}
		})
	}
}

func TestUnionItemIDs(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want []string
	}{
		{"并集去重排序", []string{"b", "a"}, []string{"c", "a"}, []string{"a", "b", "c"}},
		{"左侧为空", nil, []string{"x"}, []string{"x"}},
		{"右侧为空", []string{"x"}, nil, []string{"x"}},
		{"两侧都为空", nil, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := UnionItemIDs(tc.a, tc.b); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("UnionItemIDs=%v，期望 %v", got, tc.want)
			}
		})
	}
}

func TestEncodeDecodeItemIDsRoundTrip(t *testing.T) {
	if got := encodeItemIDs(nil); got != "[]" {
		t.Fatalf("空列表应编码为 []，实际 %q", got)
	}
	if got := encodeItemIDs([]string{"b", "a"}); got != `["a","b"]` {
		t.Fatalf("编码结果异常: %q", got)
	}
	if got := decodeItemIDs("[]"); got != nil {
		t.Fatalf("空数组应解析为 nil，实际 %v", got)
	}
	if got := decodeItemIDs("not-json"); got != nil {
		t.Fatalf("非法 JSON 应解析为 nil，实际 %v", got)
	}
	round := decodeItemIDs(encodeItemIDs([]string{"x", "y", "x"}))
	if !reflect.DeepEqual(round, []string{"x", "y"}) {
		t.Fatalf("往返解析异常: %v", round)
	}
}

func TestCheckReady(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	pending := StatusPending
	cases := []struct {
		name      string
		task      *Task
		wantReady bool
		wantWhy   string
	}{
		{"空任务", nil, false, ReasonEmptyTask},
		{
			"非 pending",
			&Task{Status: StatusRefreshing, TargetType: TargetTypeLibrary, LibraryID: "lib-1"},
			false, ReasonNotPending,
		},
		{
			"已完成",
			&Task{Status: StatusCompleted, TargetType: TargetTypeLibrary, LibraryID: "lib-1"},
			false, ReasonNotPending,
		},
		{
			"超过截止时间",
			&Task{
				Status: pending, TargetType: TargetTypeLibrary, LibraryID: "lib-1",
				RefreshAfter: now.Add(-time.Minute).Unix(), DeadlineAt: now.Add(-time.Second).Unix(),
			},
			false, ReasonDeadlineExpired,
		},
		{
			"仍在防抖窗口内",
			&Task{
				Status: pending, TargetType: TargetTypeLibrary, LibraryID: "lib-1",
				RefreshAfter: now.Add(5 * time.Second).Unix(), DeadlineAt: now.Add(time.Hour).Unix(),
			},
			false, ReasonDebounce,
		},
		{
			"库级任务缺少媒体库",
			&Task{
				Status: pending, TargetType: TargetTypeLibrary,
				RefreshAfter: now.Add(-time.Second).Unix(), DeadlineAt: now.Add(time.Hour).Unix(),
			},
			false, ReasonEmptyTarget,
		},
		{
			"条目任务缺少条目",
			&Task{
				Status: pending, TargetType: TargetTypeItem, ItemIDs: nil,
				RefreshAfter: now.Add(-time.Second).Unix(), DeadlineAt: now.Add(time.Hour).Unix(),
			},
			false, ReasonEmptyTarget,
		},
		{
			"库级任务就绪",
			&Task{
				Status: pending, TargetType: TargetTypeLibrary, LibraryID: "lib-1",
				RefreshAfter: now.Add(-time.Second).Unix(), DeadlineAt: now.Add(time.Hour).Unix(),
			},
			true, ReasonReady,
		},
		{
			"条目任务就绪",
			&Task{
				Status: pending, TargetType: TargetTypeItem, ItemIDs: []string{"item-1"},
				RefreshAfter: now.Unix(), DeadlineAt: now.Add(time.Hour).Unix(),
			},
			true, ReasonReady,
		},
		{
			"零截止时间视为未设置",
			&Task{
				Status: pending, TargetType: TargetTypeLibrary, LibraryID: "lib-1",
				RefreshAfter: now.Add(-time.Second).Unix(),
			},
			true, ReasonReady,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckReady(tc.task, now)
			if got.Ready != tc.wantReady || got.Reason != tc.wantWhy {
				t.Fatalf("CheckReady=%+v，期望 ready=%v reason=%q", got, tc.wantReady, tc.wantWhy)
			}
		})
	}
}
