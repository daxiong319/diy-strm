package mediaupgrade

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestDeleteBatchDegradationMatrix 把批量删除的降级路径一次性钉死。
//
// 网盘删除是「批量接口 + 逐条接口」两层，失败原因有四种组合，
// 每一种的处置方式不同，漏掉任何一种都等于把可删的文件留在原地
// 或者把已删的文件报成失败：
//
//	批量全成功          → 到此为止，不能再逐条（对已删文件再删一次会 ENOENT）
//	批量部分成功        → 只重试剩余
//	批量整体失败且给了剩余列表 → 只重试剩余
//	批量整体失败但没给列表    → 全部逐条重试（网络断了压根没收到响应的情况）
func TestDeleteBatchDegradationMatrix(t *testing.T) {
	const n = 4
	cases := []struct {
		name string
		// batchRest 是批量实现返回的「没能删除的路径下标」，nil 表示它报告全部成功。
		// batchErr 是整体错误。
		noBatch     bool
		batchRest   []int
		batchErr    error
		noSingle    bool
		singleFails map[int]bool

		wantSingleCalls int
		wantFailures    int
		wantFailPaths   []int
	}{
		{
			name:            "批量全成功时不得再逐条",
			wantSingleCalls: 0,
		},
		{
			name:            "批量部分成功只重试剩余",
			batchRest:       []int{1, 2},
			wantSingleCalls: 2,
		},
		{
			name:            "批量整体失败且给出剩余列表时只重试剩余",
			batchErr:        errors.New("限额"),
			batchRest:       []int{0, 2},
			wantSingleCalls: 2,
		},
		{
			name:            "批量整体失败但没给出剩余列表时全部逐条重试",
			batchErr:        errors.New("连接超时"),
			wantSingleCalls: n,
		},
		{
			name:            "没有批量实现时全部逐条",
			noBatch:         true,
			wantSingleCalls: n,
		},
		{
			name:            "逐条也失败才记为真失败",
			batchErr:        errors.New("限额"),
			singleFails:     map[int]bool{1: true, 3: true},
			wantSingleCalls: n,
			wantFailures:    2,
			wantFailPaths:   []int{1, 3},
		},
		{
			name:            "没有逐条实现时全部记为失败且带上原因",
			noBatch:         true,
			noSingle:        true,
			batchErr:        errors.New("批量被限流"),
			wantSingleCalls: 0,
			wantFailures:    n,
			wantFailPaths:   []int{0, 1, 2, 3},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			paths := make([]string, n)
			for i := range paths {
				paths[i] = writeVideo(t, filepath.Join(dir, fmt.Sprintf("f%d.mkv", i)), 1024)
			}

			var batchCalls, singleCalls int
			singleFails := map[string]bool{}
			for i := range tc.singleFails {
				singleFails[paths[i]] = tc.singleFails[i]
			}
			b := &CloudDeleteBatcher{}
			if !tc.noSingle {
				b.SingleDelete = func(p string) error {
					singleCalls++
					if singleFails[p] {
						return errors.New("网盘拒绝删除")
					}
					return os.Remove(p)
				}
			}
			if !tc.noBatch {
				b.BatchDelete = func(ps []string) ([]string, error) {
					batchCalls++
					if tc.batchErr == nil && tc.batchRest == nil {
						// 契约：返回 nil 切片表示全部成功。
						return nil, nil
					}
					rest := make([]string, 0, len(tc.batchRest))
					for _, idx := range tc.batchRest {
						rest = append(rest, paths[idx])
					}
					return rest, tc.batchErr
				}
			}

			failures := b.DeleteBatch(paths)

			if batchCalls != boolInt(!tc.noBatch) {
				t.Errorf("批量调用 %d 次，期望 %d 次", batchCalls, boolInt(!tc.noBatch))
			}
			if singleCalls != tc.wantSingleCalls {
				t.Errorf("逐条调用 %d 次，期望 %d 次", singleCalls, tc.wantSingleCalls)
			}
			if len(failures) != tc.wantFailures {
				t.Fatalf("失败明细 %d 条（%+v），期望 %d 条", len(failures), failures, tc.wantFailures)
			}
			for _, f := range failures {
				if f.Stage != DeleteStageSingle {
					t.Errorf("失败 %q 的 stage=%q，期望 %q —— 降级到逐条之后才真失败，标错 stage 会让人以为是批量接口的问题",
						f.Path, f.Stage, DeleteStageSingle)
				}
				if f.Reason == "" {
					t.Errorf("失败 %q 没有记录原因", f.Path)
				}
			}
			got := failureIndexes(dir, failures)
			if !equalInts(got, tc.wantFailPaths) {
				t.Errorf("失败的路径下标 %v，期望 %v", got, tc.wantFailPaths)
			}
		})
	}
}

// TestDeleteBatchEmptyAndNil 断言空输入不炸。
//
// 规则被关掉或败方动作改成 keep 时，调用方完全可能传空切片进来；
// 这时候既不该调批量接口，也不该返回一条「原因不明」的失败。
func TestDeleteBatchEmptyAndNil(t *testing.T) {
	var nilBatcher *CloudDeleteBatcher
	if got := nilBatcher.DeleteBatch([]string{"whatever"}); got != nil {
		t.Errorf("nil 删除器应返回 nil，实际 %+v", got)
	}
	called := false
	b := &CloudDeleteBatcher{
		BatchDelete:  func([]string) ([]string, error) { called = true; return nil, nil },
		SingleDelete: func(string) error { called = true; return nil },
	}
	if got := b.DeleteBatch(nil); got != nil {
		t.Errorf("空输入应返回 nil，实际 %+v", got)
	}
	if called {
		t.Errorf("空输入时不应该调用任何删除实现")
	}
}

// TestDeleteBatchRealFilesIsIdempotent 用真实 os.Remove 走一遍降级。
//
// 关键场景：批量删失败之后逐条重试，其中一部分文件其实已经被批量删掉了。
// 逐条再删一次会拿到 ENOENT —— 这必须算成功，
// 否则「批量删了 3 个、只报失败 1 个」会把已经删掉的 3 个记成失败，
// 用户在界面上看到的失败清单和磁盘上的真实状态完全对不上。
func TestDeleteBatchRealFilesIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	paths := make([]string, 3)
	for i := range paths {
		paths[i] = writeVideo(t, filepath.Join(dir, fmt.Sprintf("v%d.mkv", i)), 512)
	}
	b := defaultDeleter()
	// 先把 v1 删掉，让批量接口在它上面必然拿到 ENOENT。
	if err := os.Remove(paths[1]); err != nil {
		t.Fatalf("预删失败：%v", err)
	}

	failures := b.DeleteBatch(paths)
	if len(failures) != 0 {
		t.Fatalf("降级重试后仍报 %d 条失败（%+v），期望 0 条 —— 逐条重试必须是幂等的", len(failures), failures)
	}
	for i, p := range paths {
		if i == 1 {
			continue
		}
		if fileExists(p) {
			t.Errorf("%s 应该已被删掉", p)
		}
	}
}

// failureIndexes 把失败路径还原成文件名前缀下标。
func failureIndexes(dir string, failures []DeleteFailure) []int {
	out := make([]int, 0, len(failures))
	for _, f := range failures {
		base := strings.TrimSuffix(filepath.Base(f.Path), ".mkv")
		var n int
		if _, err := fmt.Sscanf(base, "f%d", &n); err != nil {
			out = append(out, -1)
			continue
		}
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
