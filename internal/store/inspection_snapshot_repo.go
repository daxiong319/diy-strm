package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

type inspectionSnapshotRepo struct{ db *DB }

// SaveInspectionSnapshot 存一份扫描快照，返回可直接回传给前端的 key。
//
// 快照是「预览过才能执行」这条规则的物理载体：执行接口拿的是 key，
// 不是用户重新传来的动作描述。用户能改的只有「做哪几条」，
// 改不了「系统打算怎么做」—— 否则预览就成了一纸空文。
func (r *inspectionSnapshotRepo) SaveInspectionSnapshot(ctx context.Context, key, findingIDs, payload string, scannedAt time.Time) error {
	_, err := r.db.write.ExecContext(ctx,
		`INSERT INTO inspection_snapshots (snapshot_key, finding_ids, payload, scanned_at, created_at)
		 VALUES (?,?,?,?,?)
		 ON CONFLICT(snapshot_key) DO UPDATE SET
		   finding_ids=excluded.finding_ids,
		   payload=excluded.payload,
		   scanned_at=excluded.scanned_at,
		   consumed_at=0`,
		key, findingIDs, payload, scannedAt.Unix(), time.Now().Unix())
	return wrapDB(err)
}

// GetInspectionSnapshot 取一份快照，第二个返回值表示是否已过期或已被消费。
func (r *inspectionSnapshotRepo) GetInspectionSnapshot(ctx context.Context, key string) (string, []string, bool, error) {
	var (
		payload    string
		findingIDs string
		consumed   int64
	)
	err := r.db.read.QueryRowContext(ctx,
		`SELECT payload, finding_ids, consumed_at FROM inspection_snapshots WHERE snapshot_key=?`, key).
		Scan(&payload, &findingIDs, &consumed)
	if err == sql.ErrNoRows {
		return "", nil, true, nil
	}
	if err != nil {
		return "", nil, true, wrapDB(err)
	}
	ids := splitCSV(findingIDs)
	if consumed > 0 {
		return payload, ids, true, nil
	}
	return payload, ids, false, nil
}

// ConsumeInspectionSnapshot 标记快照已被执行。
//
// 这里故意做成「按 key 整份作废」而不是「按 finding 逐条作废」：
// 一次预览对应一次决策，重放同一个 key 就意味着在执行一份已经过期的判断
// （用户可能已经把目录改过了）。宁可让他重新扫一次。
func (r *inspectionSnapshotRepo) ConsumeInspectionSnapshot(ctx context.Context, key string) error {
	res, err := r.db.write.ExecContext(ctx,
		`UPDATE inspection_snapshots SET consumed_at=? WHERE snapshot_key=? AND consumed_at=0`,
		time.Now().Unix(), key)
	if err != nil {
		return wrapDB(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errSnapshotConsumed
	}
	return nil
}

// DeleteOldInspectionSnapshots 清掉过期快照，返回删除条数。
func (r *inspectionSnapshotRepo) DeleteOldInspectionSnapshots(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.write.ExecContext(ctx,
		`DELETE FROM inspection_snapshots WHERE created_at < ?`, before.Unix())
	if err != nil {
		return 0, wrapDB(err)
	}
	n, err := res.RowsAffected()
	return n, wrapDB(err)
}

var errSnapshotConsumed = snapshotError("该巡检快照已被执行，请重新扫描后再试")

type snapshotError string

func (e snapshotError) Error() string { return string(e) }

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}