package mediaupgrade

import (
	"fmt"
	"path/filepath"

	"litepan/internal/moviepilot"
)

// TrialInput 规则试算的输入。
//
// 只认文件名与体积：这是**纯函数**的硬约束 —— 试算不碰数据库、不碰网盘、
// 不发网络请求。代价是那些必须探测媒体才能得到的维度（真实码率、时长、
// 音轨条数）算不出来，界面上会把它们显示成「跳过」而不是猜一个值。
type TrialInput struct {
	NewName string
	NewSize int64
	OldName string
	OldSize int64
	// HasNewSize / HasOldSize 区分「体积为 0」与「没填体积」。
	//
	// 不填体积时必须真的不参与体积兜底：否则「两文件都没填体积」
	// 会因为 0 > 0 为假而被判成 tie，用户看到「质量持平且体积不占优」，
	// 而他根本没填过体积。
	HasNewSize bool
	HasOldSize bool
}

// TrialResult 规则试算的结果。
type TrialResult struct {
	// Relation new_wins / new_loses / tie / no_dimension，与真实扫描同一套枚举。
	Relation string `json:"relation"`
	// Trace 逐项对比说明（给人看），与真实扫描同一份渲染结果。
	Trace string `json:"trace"`
	// Reasons 结构化驳回理由（含门槛类），与真实扫描同一套枚举。
	Reasons []RejectReason `json:"reasons"`
	// Dimensions 逐维度明细，供界面画表。
	Dimensions []moviepilot.QualityDimension `json:"dimensions"`
	// RuleFingerprint 本次判定所用的规则指纹。
	RuleFingerprint string `json:"rule_fingerprint"`
	// NewQuality / OldQuality 解析结果（nil = 文件名里解析不出质量标签）。
	NewQuality *moviepilot.FileQuality `json:"new_quality"`
	OldQuality *moviepilot.FileQuality `json:"old_quality"`
	// NewSlotLabel / OldSlotLabel 槽位标签，界面用来解释「为什么两个文件不同槽位」。
	NewSlotLabel string `json:"new_slot_label"`
	OldSlotLabel string `json:"old_slot_label"`
	// SameSlot 两侧是否同一版本槽位。
	SameSlot bool `json:"same_slot"`
	// GateReasons 门槛类理由（分辨率/声道/字幕）。真实扫描里门槛不过的候选
	// 根本不产生记录，所以这部分只能在试算里看到 —— 这是刻意的：
	// 「我这份规则会不会把这个文件洗掉」是用户配规则时最想问的问题，
	// 而等扫描结果里找答案是找不到的。
	GateReasons []RejectReason `json:"gate_reasons"`
}

// TrialVerdict 按规则试算两个文件名的判定结果。
//
// **必须复用 compareOne**（而不是另写一份「试算专用」的比较逻辑）：
// T02 的预览端点就踩过这个坑 —— 预览少设了一个 Loader，
// 于是「预览说会洗」而真实执行不洗，用户完全没法信任预览。
// 这里把两个文件名包成 libFile 喂给 compareOne，比较口径、Trace 渲染、
// Reasons 派生全部走同一条路。
func TrialVerdict(rs *RuleSet, in TrialInput) TrialResult {
	eff := rs.Effective()
	res := TrialResult{
		RuleFingerprint: eff.Fingerprint(),
		Reasons:         []RejectReason{},
		Dimensions:      []moviepilot.QualityDimension{},
		GateReasons:     gateReasons(&eff, trialFile(in.NewName, in.NewSize, in.HasNewSize)),
	}

	newF := trialFile(in.NewName, in.NewSize, in.HasNewSize)
	oldF := trialFile(in.OldName, in.OldSize, in.HasOldSize)
	res.NewQuality = newF.Quality
	res.OldQuality = oldF.Quality
	res.NewSlotLabel = newF.Slot.Label()
	res.OldSlotLabel = oldF.Slot.Label()
	res.SameSlot = newF.Slot.Key() == oldF.Slot.Key()

	if !res.SameSlot {
		// 不同槽位在真实扫描里走的是 buildRecord 的 no_slot 分支，
		// 不进 compareOne。这里照抄那条路径的结论口径，
		// 免得试算说「同槽位才比」而界面让人以为是两个文件没法比。
		res.Relation = RelationNoDimension
		res.Trace = "不同版本槽位（" + newF.Slot.Label() + "），两个都保留"
		res.Reasons = noSlotReasons()
		return res
	}

	v := compareOne(newF, []libFile{oldF}, eff.WashRules, eff.GroupPriority)
	res.Relation = v.Relation
	res.Trace = v.Trace
	if v.Reasons != nil {
		res.Reasons = v.Reasons
	}
	// 逐维度明细：与 Trace 同一个来源（QualityDimensions）。
	// 注意这里刻意**不**用 v.Trace 反推，也不重跑一遍 —— 试算要展示的
	// 就是这次判定真正看过的东西。
	res.Dimensions = moviepilot.QualityDimensions(newF.Quality, oldF.Quality, eff.GroupPriority, eff.WashRules)
	return res
}

// trialFile 由文件名与体积构造一个 libFile，字段口径与真实枚举一致。
func trialFile(name string, size int64, hasSize bool) libFile {
	f := libFile{
		Path: filepath.Base(name),
		Name: name,
		// 没填体积 ⇒ 这一步不参与判定。真实枚举里 Size 来自 stat，
		// 永远可信，所以只有规则试算会走到这里。
		ignoreSize: !hasSize,
	}
	if hasSize {
		f.Size = size
	}
	f.Quality = moviepilot.ParseQualityFromName(name)
	if f.Quality != nil {
		f.Slot = SlotOf(name, f.Quality)
	} else {
		f.Slot = SlotOf(name, nil)
	}
	return f
}

// TrialSummary 把试算结果拼成一句人话，供 CLI/日志复用。
func TrialSummary(res TrialResult) string {
	if len(res.GateReasons) > 0 {
		return fmt.Sprintf("候选未达规则门槛：%s", res.Trace)
	}
	switch res.Relation {
	case RelationNewWins:
		return "新版胜出：" + res.Trace
	case RelationNewLoses:
		return "新版没赢现版：" + res.Trace
	case RelationTie:
		return "质量持平且体积不占优：" + res.Trace
	default:
		return "无可比维度：" + res.Trace
	}
}
