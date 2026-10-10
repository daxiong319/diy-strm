package inspection

import (
	"context"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// T15 遗留小活 2 · DeleteDirRepairer 删除前复核。
//
// 这段 DirItems 复查是「扫描后目录被放进了新文件」的唯一防线。
// 变异实测：把它短路掉（直接调 DeleteDir），下面两条用例立刻红 ——
// 也就是说没有别的东西会拦住「照着一份过期结论把目录连同新文件一起删掉」。
//
// 所以这里断言三件事，缺一不可：
//  1. 目录非空时 Repair 返回错误；
//  2. DeleteDir 一次都没被调用（不是「删了再报错」）；
//  3. 错误信息里带出项数，用户才知道该去看什么。
// ---------------------------------------------------------------------------

// fakeDirChecker 记录调用，用来证明「没删」。
type fakeDirChecker struct {
	items        []DirItem
	itemsErr     error
	dirCalls     int
	deletedDirID string
	accountID    int64
}

func (f *fakeDirChecker) DirItems(_ context.Context, _ int64, _ string) ([]DirItem, error) {
	if f.itemsErr != nil {
		return nil, f.itemsErr
	}
	return f.items, nil
}

func (f *fakeDirChecker) DeleteDir(_ context.Context, accountID int64, dirID string) error {
	f.dirCalls++
	f.accountID = accountID
	f.deletedDirID = dirID
	return nil
}

func deleteDirAction(dirID, path string) RepairAction {
	return RepairAction{
		Kind:   RepairDeleteDir,
		Label:  "删除空目录",
		Params: map[string]string{"account_id": "7", "dir_id": dirID, "path": path},
	}
}

func TestDeleteDirRepairerRefusesNonEmptyDir(t *testing.T) {
	files := &fakeDirChecker{items: []DirItem{
		{ID: "f1", Name: "新放进来的.mkv"},
		{ID: "f2", Name: "海报.jpg"},
	}}
	r := &DeleteDirRepairer{Files: files}

	_, err := r.Repair(context.Background(), deleteDirAction("dir-123", "/media/空目录"))
	if err == nil {
		t.Fatalf("目录已经非空了，删除却被放行")
	}
	// 第 2 条断言：不是「删完再报错」。
	if files.dirCalls != 0 {
		t.Fatalf("复核失败后仍然调用了 DeleteDir %d 次（dir=%s account=%d）",
			files.dirCalls, files.deletedDirID, files.accountID)
	}
	// 第 3 条：项数要出现在错误里 —— 用户得知道该去看什么。
	if !strings.Contains(err.Error(), "已跳过删除") || !strings.Contains(err.Error(), "2") {
		t.Fatalf("错误信息没有说清已跳过与项数：%v", err)
	}
}

func TestDeleteDirRepairerSkipsWhenRecheckFails(t *testing.T) {
	files := &fakeDirChecker{itemsErr: context.DeadlineExceeded}
	r := &DeleteDirRepairer{Files: files}

	if _, err := r.Repair(context.Background(), deleteDirAction("dir-123", "/media/空目录")); err == nil {
		t.Fatalf("复核查询失败时删除却被放行")
	}
	// 拿不到目录内容 ≠ 目录是空的。查不到就当空，等于把网络抖动
	// 翻译成一次静默删除，方向完全反了。
	if files.dirCalls != 0 {
		t.Fatalf("复核失败后仍然调用了 DeleteDir %d 次", files.dirCalls)
	}
}

func TestDeleteDirRepairerDeletesConfirmedEmptyDir(t *testing.T) {
	// 补一条正向用例：空目录仍然要能删掉，否则上面两条会「因为什么都删不掉」
	// 而通过 —— 那就把修复功能整体关掉了。
	files := &fakeDirChecker{items: nil}
	r := &DeleteDirRepairer{Files: files}

	msg, err := r.Repair(context.Background(), deleteDirAction("dir-123", "/media/空目录"))
	if err != nil {
		t.Fatalf("空目录应当可以删除：%v", err)
	}
	if files.dirCalls != 1 || files.deletedDirID != "dir-123" {
		t.Fatalf("没有删到目标目录：calls=%d dir=%s", files.dirCalls, files.deletedDirID)
	}
	if !strings.Contains(msg, "/media/空目录") {
		t.Fatalf("返回信息里没有路径：%q", msg)
	}
}
