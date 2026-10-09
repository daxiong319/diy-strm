package mediaupgrade

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"litepan/internal/moviepilot"
)

// workKey 是一个「作品分组键」：同一部剧的同一集，或同一部电影。
//
// 用 moviepilot 自己的同名匹配口径（EpisodeKeyOf / WashCoreKey）而不是自己写一套正则：
// 「剧名.S01E02.1080p.mkv」与「剧名.S01E02.2160p.mkv」必须落在同一个组里，
// 否则洗版永远配不上对。这套口径已经在整理流程里跑着，复用它才不会两处各认一套。
type workKey struct {
	SeriesKey  string // 剧名（已剥离季集号与质量后缀）
	EpisodeKey string // S01E02；电影为空
}

// Key 返回可入库的分组键。
func (w workKey) Key() string { return w.SeriesKey + "|" + w.EpisodeKey }

// episodeTokenRe 季集号 token：SxxExx、中文「第N集/話/话/期」。
var episodeTokenRe = regexp.MustCompile(`(?i)S\d{1,2}\s*E\d{1,3}|第\s*\d{1,3}\s*[集話话期]`)

// workKeyOf 由文件名算出作品分组键。
func workKeyOf(fileName string) workKey {
	ext := ""
	if e := extensionOf(fileName); e != "" {
		ext = e
	}
	stem := strings.TrimSuffix(fileName, ext)
	// 去掉季集号再取同名键，这样 S01E01 与 S01E02 才是同一剧、且 episode 分开。
	stem = episodeTokenRe.ReplaceAllString(stem, "")
	// 复原扩展名再交给 WashCoreKey：直接传 stem 的话 path.Ext 会把
	//「剧名..2160p」里的「. 2160p」当成扩展名剥掉，结果就错了。
	series := moviepilot.WashCoreKey(stem + ext)
	return workKey{
		SeriesKey:  strings.TrimRight(series, " ."),
		EpisodeKey: moviepilot.EpisodeKeyOf(fileName),
	}
}

// extensionOf 取扩展名（含点）。
func extensionOf(fileName string) string {
	idx := strings.LastIndexByte(fileName, '.')
	if idx <= 0 || idx == len(fileName)-1 {
		return ""
	}
	return fileName[idx:]
}

// displayTitle 作品展示名：从原始文件名里剥季集号，剩下的当标题。
func displayTitle(fileName string) string {
	stem := strings.TrimSuffix(fileName, extensionOf(fileName))
	stem = episodeTokenRe.ReplaceAllString(stem, " ")
	stem = moviepilot.WashCoreKey(stem)
	if stem == "" {
		return fileName
	}
	return stem
}

// episodeLabel 集号标签。
func episodeLabel(fileName string) string { return moviepilot.WashEpisodeLabel(fileName) }

// ---- 结构化驳回理由（T31） ----
//
// 与 Trace 并存、**不替换** Trace：Trace 是给人读的一句话，
// Reasons 是给机器查的稳定枚举。两者出自同一份维度明细
// （见 moviepilot.QualityDimensions），所以不会互相矛盾。
//
// 为什么必须结构化：用户问「最近一周有多少条是因为分辨率不够被驳回的」，
// 翻 Trace 字符串统计不出来 —— 那句话的措辞会随文案改动而变。
const (
	// RejectInferiorDimension 某个维度上新版更劣（new_loses 的主因）。
	RejectInferiorDimension = "inferior_dimension"
	// RejectBelowMinResolution 分辨率低于规则 min_resolution。
	RejectBelowMinResolution = "below_min_resolution"
	// RejectBelowMinChannels 声道数低于规则 min_channels。
	RejectBelowMinChannels = "below_min_channels"
	// RejectMissingRequiredSubtitle 缺规则要求的字幕。
	RejectMissingRequiredSubtitle = "missing_required_subtitle"
	// RejectNoComparableDimension 无可比维度（宁可漏洗不误删的兜底）。
	RejectNoComparableDimension = "no_comparable_dimension"
	// RejectNoSlot 新旧不在同一版本槽位，两个都保留。
	RejectNoSlot = "no_slot"
)

// RejectReason 一条结构化驳回理由。
type RejectReason struct {
	// Code 稳定枚举，见上。查询与统计只认这个字段。
	Code string `json:"code"`
	// Field 命中维度：resolution/codec/format/channels/bitdepth/group。
	// 门槛类理由（below_min_*）填对应维度；no_slot / no_comparable_dimension 留空。
	Field string `json:"field"`
	// New / Old 该维度两侧的显示值，与 Trace 里的字符串完全一致（共用同一份明细）。
	New string `json:"new"`
	Old string `json:"old"`
}

// reasonsFromDimensions 从同一份维度明细里挑出「新版更劣」的那一条。
//
// 只取决胜维度（明细在决胜处就停了，后面那些没参与判定）；
// 逐项持平时一条都没有 —— 「两边一样」不是「被某个维度驳回」。
func reasonsFromDimensions(dims []moviepilot.QualityDimension) []RejectReason {
	for _, d := range dims {
		if !d.Worse {
			continue
		}
		return []RejectReason{{
			Code:  RejectInferiorDimension,
			Field: d.Field,
			New:   d.New,
			Old:   d.Old,
		}}
	}
	return nil
}

// gateReasons 返回候选没过的规则门槛理由。
//
// 与 meetsCandidateGate 用**同一份**门槛字段，但这里只负责「说明为什么」，
// 判定本身仍在 meetsCandidateGate —— 两处各判一次就会出现
// 「记录说因为分辨率被驳回，扫描却当它通过了门槛」。
func gateReasons(rs *RuleSet, f libFile) []RejectReason {
	var out []RejectReason
	if rs.MinResolution > 0 {
		res := 0
		if f.Quality != nil {
			res = f.Quality.Resolution
		}
		if res < rs.MinResolution {
			out = append(out, RejectReason{
				Code:  RejectBelowMinResolution,
				Field: "resolution",
				New:   displayResolution(f.Quality),
				Old:   strconv.Itoa(rs.MinResolution) + "p",
			})
		}
	}
	if rs.MinChannels > 0 {
		ch := 0
		if f.Quality != nil {
			ch = f.Quality.Channels
		}
		if ch < rs.MinChannels {
			out = append(out, RejectReason{
				Code:  RejectBelowMinChannels,
				Field: "channels",
				New:   displayChannels(f.Quality),
				Old:   strconv.Itoa(rs.MinChannels) + "ch",
			})
		}
	}
	if rs.RequireSubtitle && f.Slot.Subtitle != "sub" {
		out = append(out, RejectReason{
			Code:  RejectMissingRequiredSubtitle,
			Field: "subtitle",
			New:   displaySubtitle(f.Slot),
		})
	}
	return out
}

// displayResolution / displayChannels / displaySubtitle 门槛理由里用的显示值。
//
// 复用 qualityField 而不是各写一份：门槛理由里的「1080p」必须与 Trace 里的
// 「1080p」长得一样，否则用户对照两处会以为说的不是一回事。
func displayResolution(q *moviepilot.FileQuality) string {
	if q == nil {
		return ""
	}
	return qualityField(q, "resolution")
}

func displayChannels(q *moviepilot.FileQuality) string {
	if q == nil {
		return ""
	}
	return qualityField(q, "channels")
}

func displaySubtitle(s Slot) string {
	if s.Subtitle == slotUnknown || s.Subtitle == "" {
		return "无"
	}
	return s.Subtitle
}

// noDimensionReasons 无可比维度时的兜底理由。
func noDimensionReasons() []RejectReason {
	return []RejectReason{{Code: RejectNoComparableDimension}}
}

// noSlotReasons 不同版本槽位时的理由（两个都保留，不是「被驳回」但仍要能查）。
func noSlotReasons() []RejectReason {
	return []RejectReason{{Code: RejectNoSlot}}
}

// verdict 一次比较的结论。
type verdict struct {
	Relation string // new_wins / new_loses / tie / no_dimension
	Trace    string // 逐项对比说明
	// Reasons 结构化驳回理由，与 Trace 同源（见 reasonsFromDimensions）。
	Reasons []RejectReason
	Loser   string // 败方文件路径（新版赢时是旧文件路径）
}

// comparableDimension 返回一次比较里双方都有值的维度数。
//
// 0 = 无可比维度。这是「宁可漏洗不误删」的兜底：
// 两个都叫「XX.mkv」解析不出任何质量标签的文件，
// 如果不拦，CompareQuality 的「解析不出就当可覆盖」分支会让新版无理由获胜并删掉旧版。
func comparableDimension(newQ, oldQ *moviepilot.FileQuality, rules []moviepilot.WashRule) int {
	if newQ == nil || oldQ == nil {
		return 0
	}
	n := 0
	for _, r := range rules {
		if r.Field == "group" {
			if strings.TrimSpace(newQ.Group) != "" && strings.TrimSpace(oldQ.Group) != "" {
				n++
			}
			continue
		}
		if qualityField(newQ, r.Field) != "" && qualityField(oldQ, r.Field) != "" {
			n++
		}
	}
	return n
}

// qualityField 取一个规则字段的可读值（空串 = 该侧没这个信息）。
func qualityField(q *moviepilot.FileQuality, field string) string {
	switch field {
	case "resolution":
		if q.ResTag != "" {
			return strings.ToLower(q.ResTag)
		}
		if q.Resolution > 0 {
			return strconv.Itoa(q.Resolution) + "p"
		}
	case "codec":
		if q.CodecTag != "" {
			return q.CodecTag
		}
		return strings.ToUpper(q.Codec)
	case "format":
		if q.VideoFormat != "" {
			return strings.ToUpper(q.VideoFormat)
		}
	case "channels":
		if q.AudioTag != "" {
			return q.AudioTag
		}
		if q.Channels > 0 {
			return strconv.Itoa(q.Channels) + "ch"
		}
	case "bitdepth":
		if q.BitDepth != "" {
			return strings.ToUpper(q.BitDepth)
		}
	case "group":
		return q.Group
	}
	return ""
}

// compareOne 比较一个新候选与一组同槽位的旧文件。
//
// 口径必须与 moviepilot.DecideWash 完全一致（决策：>0 新优则跳过、==0 用体积兜底），
// 差别只在于这里需要**拿到败方是谁**而 DecideWash 只给一个 Proceed 布尔。
// compareMatchesDecideWash 这个测试把两者钉在一起，防止日后改一边忘了改另一边。
func compareOne(newF libFile, oldFiles []libFile, rules []moviepilot.WashRule, groupPriority []string) verdict {
	if len(oldFiles) == 0 {
		return verdict{Relation: RelationNoDimension, Trace: "同槽位没有现版可比较"}
	}
	newQ := newF.Quality
	if newQ == nil {
		return verdict{Relation: RelationNoDimension, Trace: "新版质量不可解析"}
	}
	var (
		loser  *libFile
		best   *libFile
		trace  string
		sawOld bool
		// 决胜维度的明细。与 trace 一份来源，reasons 从它派生。
		dims []moviepilot.QualityDimension
		// 逐项比不过（cmp<0）才算「新版质量差」，cmp==0 靠体积兜底是另一种情况，
		// 两者在状态机上要落到不同的关系值上，界面上也得说不同的话。
		strictlyWorse bool
		// 有可比维度的对照对数。为 0 时一律不动作（见下面的守卫）。
		compared int
		// sizeUndecided：体积没填，逐项持平的对照对因此没有得到结论。
		// 结果仍是「保持不变」，但要在 trace 里说清是「判不了」而不是「输了体积」。
		sizeUndecided bool
	)
	for i := range oldFiles {
		old := &oldFiles[i]
		sawOld = true
		if comparableDimension(newQ, old.Quality, rules) > 0 {
			compared++
		}
		// 记下同槽位里最强的现版：新版胜出时，被替换的就是它
		// （而不是「随便挑一个删」—— 删弱的留强的等于把库洗成更差）。
		if best == nil ||
			moviepilot.CompareQuality(old.Quality, best.Quality, groupPriority, rules) > 0 {
			best = old
		}
		cmp := moviepilot.CompareQuality(newQ, old.Quality, groupPriority, rules)
		if cmp > 0 {
			continue
		}
		if cmp == 0 {
			// 逐项都没信息：体积不能当质量依据，否则「大文件」会赢。
			if comparableDimension(newQ, old.Quality, rules) == 0 {
				continue
			}
			// 体积没填（规则试算）时体积不参与判定：不参与 ≠ 输。
			// 少了这一句，用户什么都没填就会被告知「体积不占优」，
			// 而真正该给的结论是「逐项持平、判不了」。
			if newF.ignoreSize || old.ignoreSize {
				dims = moviepilot.QualityDimensions(newQ, old.Quality, groupPriority, rules)
				trace = moviepilot.TraceFromDimensions(dims) + "；体积未填写，不参与兜底"
				sizeUndecided = true
				continue
			}
			if newF.Size > old.Size {
				continue
			}
			// 逐项持平、靠体积兜底没兜住：没有哪个维度「驳回」了它，
			// 所以 Reasons 留空，只有人读的 trace。
			dims = moviepilot.QualityDimensions(newQ, old.Quality, groupPriority, rules)
			trace = moviepilot.TraceFromDimensions(dims)
			loser = old
			continue
		}
		strictlyWorse = true
		dims = moviepilot.QualityDimensions(newQ, old.Quality, groupPriority, rules)
		trace = moviepilot.TraceFromDimensions(dims)
		loser = old
	}
	if !sawOld {
		return verdict{Relation: RelationNoDimension, Trace: "没有可配对的现版"}
	}
	// 守卫：所有对照对都没有一个可比维度（两边都解析不出分辨率/编码/色深…）。
	// CompareQuality 在这种输入下会全项相等返回 0，体积兜底又会让「更大的文件」赢，
	// 于是两个都叫「XX.mkv」的文件会被判成「新版胜出」并删掉旧版 ——
	// 宁可漏洗不误删，所以这里直接不给动作。
	if compared == 0 {
		return verdict{
			Relation: RelationNoDimension,
			Trace:    "无可比维度：新旧都没解析出可比较的质量标签",
			Reasons:  noDimensionReasons(),
		}
	}
	if loser == nil {
		// 逐项持平、但体积没填 ⇒ 判不了，不是「新版赢」。
		// 报成 new_wins 会让界面建议删掉现版 —— 而我们连体积都没比过。
		if sizeUndecided {
			return verdict{
				Relation: RelationTie,
				Trace:    traceOrDefault(trace, "逐项持平；体积未填写，不参与兜底"),
				Loser:    "",
			}
		}
		v := verdict{
			Relation: RelationNewWins,
			Trace:    traceOrDefault(trace, "新版质量不低于现版"),
		}
		if best != nil {
			v.Loser = best.Path
		}
		return v
	}
	relation := RelationNewLoses
	if !strictlyWorse {
		relation = RelationTie
	}
	return verdict{
		Relation: relation,
		Trace:    traceOrDefault(trace, "新版质量不高于现版"),
		// 只有「某个维度上新版更劣」才结构化成驳回理由；
		// 持平（体积兜底没兜住）不算被驳回，Reasons 保持 nil。
		Reasons: reasonsFromDimensions(dims),
		Loser:   loser.Path,
	}
}

// traceOrDefault 空 trace 时给一句人话，避免界面上出现空的对比说明。
func traceOrDefault(trace, def string) string {
	if strings.TrimSpace(trace) == "" {
		return def
	}
	return trace
}

// marshalReasons 序列化驳回理由；nil/空 → 空串（不是 "null"）。
//
// 走 marshalJSON 而不是手拼：code/field/new/old 的引号转义交给 json 包，
// 手拼遇到文件名里带引号就会写出一列坏 JSON。
func marshalReasons(reasons []RejectReason) string {
	if len(reasons) == 0 {
		return ""
	}
	return marshalJSON(reasons)
}

// marshalJSON 序列化，失败时返回空串（这些字段都只是留痕，不该因此让扫描失败）。
func marshalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// marshalSnapshots 序列化文件快照数组。
//
// 顺序按 Path 排过序（collectLocalFiles / loadEmbyIndexFiles 都已排序），
// 哈希才稳定 —— 否则同一份文件集合两次枚举顺序不同就会被误判成「判定已过期」。
func marshalSnapshots(files []libFile) string {
	snaps := make([]fileSnapshot, 0, len(files))
	for _, f := range files {
		snaps = append(snaps, f.snapshot())
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].Path < snaps[j].Path })
	return marshalJSON(snaps)
}

// dedupLibFiles 按路径去重（媒体库与候选目录重叠时会出现同一个文件）。
func dedupLibFiles(in []libFile) []libFile {
	seen := make(map[string]bool, len(in))
	out := make([]libFile, 0, len(in))
	for _, f := range in {
		if seen[f.Path] {
			continue
		}
		seen[f.Path] = true
		out = append(out, f)
	}
	return out
}
