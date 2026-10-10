package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"litepan/internal/inspection"
)

// ---------------------------------------------------------------------------
// T15 遗留小活 1 · preview_ack 门（巡检执行入口）。
//
// 这道 if 是「必须先预览」的唯一强制点。变异实测：把它拆掉，一个不带
// preview_ack 的请求就能直接走到执行 —— 也就是一个写错的客户端（或者某个
// 脚本）可以跳过预览直接删目录，而页面上完全看不出来。
//
// 所以这里不测「正常流程能跑通」，只测一件事：**门确实拦得住**。
// 用例跑在真实 Service 上（内存快照仓储 + 空注册表），所以即使门被拆掉，
// 拿到的也只是「快照为空」的失败，**绝不会碰到任何真实副作用** ——
// 拆掉门也不会删掉任何东西。
// ---------------------------------------------------------------------------

// gateSnapshotRepo 是内存版快照仓储：够跑通「写入→读取→作废」，不碰数据库。
type gateSnapshotRepo struct {
	rows map[string]string
}

func newGateSnapshotRepo() *gateSnapshotRepo {
	// 预置一个「存在但 Findings 为空」的快照：足以让请求走完 preview_ack
	// 那道门、进入 Service.Repair，然后被「快照为空」挡住。全程不碰任何修复器。
	return &gateSnapshotRepo{rows: map[string]string{"snapshot-abc": "[]"}}
}

func (r *gateSnapshotRepo) SaveInspectionSnapshot(_ context.Context, key, _, payload string, _ time.Time) error {
	r.rows[key] = payload
	return nil
}

func (r *gateSnapshotRepo) GetInspectionSnapshot(_ context.Context, key string) (string, []string, bool, error) {
	payload, ok := r.rows[key]
	if !ok {
		return "", nil, true, nil
	}
	return payload, []string{"empty_dir|empty_dir|/x"}, false, nil
}

func (r *gateSnapshotRepo) ConsumeInspectionSnapshot(_ context.Context, key string) error {
	if _, ok := r.rows[key]; !ok {
		return errors.New("该巡检快照已被执行，请重新扫描后再试")
	}
	delete(r.rows, key)
	return nil
}

func (r *gateSnapshotRepo) DeleteOldInspectionSnapshots(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func postInspectionRepair(t *testing.T, svc *inspection.Service, body map[string]any) (int, Resp) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/tools/inspection/repair", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h := &Handler{inspection: svc}
	h.inspectionRepair(rec, req)
	var resp Resp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
	}
	return rec.Code, resp
}

func TestInspectionRepairRejectsRequestWithoutPreviewAck(t *testing.T) {
	svc := inspection.NewService(inspection.ServiceOptions{
		Repo:     newGateSnapshotRepo(),
		Registry: inspection.NewRegistry(),
	})
	cases := []struct {
		name string
		body map[string]any
	}{
		{
			name: "完全不带 preview_ack 字段",
			body: map[string]any{"snapshot_id": "snapshot-abc", "finding_ids": []string{"empty_dir|empty_dir|/x"}},
		},
		{
			name: "preview_ack 显式为 false",
			body: map[string]any{
				"snapshot_id": "snapshot-abc",
				"finding_ids": []string{"empty_dir|empty_dir|/x"},
				"preview_ack": false,
			},
		},
		{
			// 空 finding_ids 本身也该被拒 —— 但理由不同（没选任何项）。
			// 这条用例把两道门的顺序钉死：preview_ack 先判。
			name: "空选择且没确认",
			body: map[string]any{"snapshot_id": "snapshot-abc", "finding_ids": []string{}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, resp := postInspectionRepair(t, svc, tc.body)
			if code == http.StatusOK || resp.Success {
				t.Fatalf("不带 preview_ack 的修复请求被放行了：code=%d body=%+v", code, resp)
			}
			// 精确匹配这句话：门被拆掉时报的会是后面的
			// 「没有选中任何待修复项」，措辞完全不同。
			if resp.Message != "请先查看修复预览再执行" {
				t.Fatalf("不是被 preview_ack 门拦下的，message=%q", resp.Message)
			}
		})
	}
}

// TestInspectionRepairStillRejectsEmptySnapshotAfterAck 钉住另一侧：
// 即使带了 preview_ack，也不代表能执行任何东西 —— 快照里没有的 finding
// 照旧不执行。两条一起说明 preview_ack 是**必要**条件，不是充分条件。
func TestInspectionRepairStillRejectsEmptySnapshotAfterAck(t *testing.T) {
	svc := inspection.NewService(inspection.ServiceOptions{
		Repo:     newGateSnapshotRepo(),
		Registry: inspection.NewRegistry(),
	})
	code, resp := postInspectionRepair(t, svc, map[string]any{
		"snapshot_id": "snapshot-abc",
		"finding_ids": []string{"empty_dir|empty_dir|/x"},
		"preview_ack": true,
	})
	if resp.Message != "巡检快照为空或已过期，请重新扫描" {
		t.Fatalf("带确认后的错误路径不对：code=%d message=%q", code, resp.Message)
	}
}
