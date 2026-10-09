package mediaupgrade

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// T31 的落地侧用例：真实扫描产出的记录必须带上结构化理由与规则指纹，
// 而且规则试算必须**不落库**。

// 复现一次真实扫描（复用 commit_test 的 oldName/newName 与 stageOldAndNew）。
func scanOnce(t *testing.T, mutate func(rs *RuleSet)) (Record, *RuleSet) {
	t.Helper()
	oldPath, newPath := stageOldAndNew(t)
	db := openTestDB(t)
	rs := testRuleSet(filepath.Dir(oldPath), filepath.Dir(newPath))
	if mutate != nil {
		mutate(rs)
	}
	ruleID := saveRule(t, db, rs)
	scanner := NewScanner(db, newFakeSettings(nil))
	scan, err := scanner.Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	return onlyRecord(t, db, scan.ID), rs
}

// TestScannedRecordCarriesRuleFingerprint 记录必须标出「这是哪一版规则下的判定」。
//
// 这是 rule_fingerprint 这一列存在的全部理由：规则一改，旧记录的
// 「因为色深被驳回」这句话的含义就变了，光看记录本身分不清。
func TestScannedRecordCarriesRuleFingerprint(t *testing.T) {
	rec, rs := scanOnce(t, nil)
	if rec.RuleFingerprint == "" {
		t.Fatal("记录的 rule_fingerprint 为空 —— 事后无法判断这条结论出自哪一版规则")
	}
	if rec.RuleFingerprint != rs.Fingerprint() {
		t.Fatalf("记录指纹 = %q，规则集指纹 = %q", rec.RuleFingerprint, rs.Fingerprint())
	}
	if len(rec.RuleFingerprint) != FingerprintLength {
		t.Fatalf("指纹长度 = %d，期望 %d", len(rec.RuleFingerprint), FingerprintLength)
	}
}

// TestScannedRecordFingerprintChangesWithTheRule 同一条业务规则改一个字段，指纹必须变。
func TestScannedRecordFingerprintChangesWithTheRule(t *testing.T) {
	base, _ := scanOnce(t, nil)
	// min_resolution 不影响这份规则的判定结论，但它属于有效字段 ⇒ 指纹必须变。
	// （如果只按「结论会不会变」来哈希，改门槛却不改判定就会漏掉版本切换，
	// 而用户改门槛正是最常见的调参动作。）
	changed, _ := scanOnce(t, func(rs *RuleSet) { rs.MinResolution = 2160 })
	if changed.RuleFingerprint == base.RuleFingerprint {
		t.Fatalf("改了 min_resolution 后指纹没变（%q）", base.RuleFingerprint)
	}
}

// TestScannedRecordWinsCarriesNoRejectReasons 新版胜出的记录不该有驳回理由。
func TestScannedRecordWinsCarriesNoRejectReasons(t *testing.T) {
	rec, _ := scanOnce(t, nil)
	if rec.QualityRelation != RelationNewWins {
		t.Fatalf("前提不成立：关系是 %q", rec.QualityRelation)
	}
	if rec.RejectReasons != "" {
		t.Fatalf("新版胜出不该有驳回理由，实际 %q", rec.RejectReasons)
	}
}

// TestScannedRecordNoSlotCarriesNoSlotReason 不同槽位的记录带 no_slot 理由。
func TestScannedRecordNoSlotCarriesNoSlotReason(t *testing.T) {
	// 用一个分辨率明显不同的候选 ⇒ 落到 skipped_no_slot 那条路径。
	rec := scanNoSlotOnce(t)
	if rec.RejectReasons == "" {
		t.Fatal("不同槽位的记录没有带理由 —— no_slot 这个 code 永远不会出现在库里")
	}
	if !containsCode(rec.RejectReasons, RejectNoSlot) {
		t.Fatalf("理由里没有 %q：%q", RejectNoSlot, rec.RejectReasons)
	}
}

// TestTrialDoesNotWriteAnything 验收 ④后半：试算不落库。
//
// 试算是纯函数只读端点。用户可能对着同一条规则试算几十次，
// 每次都往库里写一条记录的话，「洗版记录」列表会被纯粹的预览操作淹没，
// 而那份列表是用户判断「上次扫描都干了什么」的依据。
func TestTrialDoesNotWriteAnything(t *testing.T) {
	oldPath, newPath := stageOldAndNew(t)
	db := openTestDB(t)
	rs := testRuleSet(filepath.Dir(oldPath), filepath.Dir(newPath))
	ruleID := saveRule(t, db, rs)
	loaded, err := LoadRuleSet(db, newFakeSettings(nil), ruleID)
	if err != nil {
		t.Fatalf("装载规则集失败：%v", err)
	}

	var before int64
	if err := db.Model(&Record{}).Count(&before).Error; err != nil {
		t.Fatalf("统计记录数失败：%v", err)
	}
	var scansBefore int64
	if err := db.Model(&Scan{}).Count(&scansBefore).Error; err != nil {
		t.Fatalf("统计扫描数失败：%v", err)
	}

	for i := 0; i < 5; i++ {
		res := TrialVerdict(loaded, TrialInput{NewName: newName, NewSize: 9 << 30, HasNewSize: true})
		if res.Relation == "" {
			t.Fatal("试算没有给出结论")
		}
	}

	var after int64
	if err := db.Model(&Record{}).Count(&after).Error; err != nil {
		t.Fatalf("统计记录数失败：%v", err)
	}
	var scansAfter int64
	if err := db.Model(&Scan{}).Count(&scansAfter).Error; err != nil {
		t.Fatalf("统计扫描数失败：%v", err)
	}
	if after != before {
		t.Fatalf("试算写入了 %d 条判定记录（%d → %d）", after-before, before, after)
	}
	if scansAfter != scansBefore {
		t.Fatalf("试算创建了 %d 次扫描（%d → %d）", scansAfter-scansBefore, scansBefore, scansAfter)
	}
}

// TestTrialMatchesTheRecordTheScanProduced 试算结论必须与真实扫描对同一组文件给出的结论一致。
//
// 验收 ④的主用例：试算是配规则时的依据，扫描是执行依据。
// 两者不一致 ⇒ 用户照着错的配了规则，然后整个洗版行为都不可信。
func TestTrialMatchesTheRecordTheScanProduced(t *testing.T) {
	oldPath, newPath := stageOldAndNew(t)
	db := openTestDB(t)
	rs := testRuleSet(filepath.Dir(oldPath), filepath.Dir(newPath))
	ruleID := saveRule(t, db, rs)
	scanner := NewScanner(db, newFakeSettings(nil))
	scan, err := scanner.Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	rec := onlyRecord(t, db, scan.ID)

	loaded, err := LoadRuleSet(db, newFakeSettings(nil), ruleID)
	if err != nil {
		t.Fatalf("装载规则集失败：%v", err)
	}
	trial := TrialVerdict(loaded, TrialInput{
		NewName: newName, NewSize: 9 << 30, HasNewSize: true,
		OldName: oldName, OldSize: 3 << 30, HasOldSize: true,
	})
	if trial.Relation != rec.QualityRelation {
		t.Fatalf("试算 relation=%q，真实扫描记录=%q（trace：%s）", trial.Relation, rec.QualityRelation, rec.Trace)
	}
	if trial.Trace != rec.Trace {
		t.Fatalf("试算 trace=%q，真实扫描记录=%q", trial.Trace, rec.Trace)
	}
	if trial.RuleFingerprint != rec.RuleFingerprint {
		t.Fatalf("试算指纹 = %q，记录指纹 = %q", trial.RuleFingerprint, rec.RuleFingerprint)
	}
}

// scanNoSlotOnce 造一次「跨槽位」的扫描并返回那条记录。
func scanNoSlotOnce(t *testing.T) Record {
	t.Helper()
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	cand := filepath.Join(base, "cand")
	// 库 1080p、候选 2160p：不同槽位 ⇒ 两个都保留。
	writeVideo(t, filepath.Join(lib, "剧名.S01E01.1080p.x265.mkv"), 1<<30)
	writeVideo(t, filepath.Join(cand, "剧名.S01E01.2160p.x265.mkv"), 9<<30)

	db := openTestDB(t)
	rs := testRuleSet(lib, cand)
	ruleID := saveRule(t, db, rs)
	scanner := NewScanner(db, newFakeSettings(nil))
	scan, err := scanner.Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	recs := recordsOf(t, db, scan.ID)
	if len(recs) != 1 {
		t.Fatalf("期望恰好 1 条记录，实际 %d 条", len(recs))
	}
	if recs[0].Status != RecordStatusSkippedNoSlot {
		t.Fatalf("状态 = %q，期望 %q（前提不成立，后面测的就不是 no_slot 了）", recs[0].Status, RecordStatusSkippedNoSlot)
	}
	return recs[0]
}

// containsCode 判断序列化后的理由里有没有某个 code。
func containsCode(raw, code string) bool {
	return strings.Contains(raw, `"code":"`+code+`"`)
}
