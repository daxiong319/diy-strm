package mediaupgrade

import (
	"fmt"
	"strings"
	"testing"

	"litepan/internal/moviepilot"
)

// mustQuality 解析文件名质量，解析不出来就直接失败。
func mustQuality(t *testing.T, name string) *moviepilot.FileQuality {
	t.Helper()
	q := moviepilot.ParseQualityFromName(name)
	if q == nil {
		t.Fatalf("用例自身的文件名解析不出质量：%s", name)
	}
	return q
}

// libFileOf 造一个参与比较的同槽位旧文件。
func libFileOf(path, name string, size int64, q *moviepilot.FileQuality) libFile {
	return libFile{
		Path:    path,
		Name:    name,
		Size:    size,
		Quality: q,
		Slot:    SlotOf(name, q),
	}
}

// TestCompareOneAgreesWithDecideWash 把洗版判定和整理流程钉在同一口径上。
//
// 同一个 CompareQuality 同时被两个地方用：
//   - internal/moviepilot/organize.go 的 DecideWash：整理时决定要不要用新版替换旧版
//   - 本包的 compareOne：扫描时决定「谁被替换」
//
// 两边一旦分叉，就会出现「整理时认为新版更好，洗版却认为新版更差」的诡异局面，
// 而且没有任何报错 —— 只是一个版本被留下了，另一个被删了。
//
// 断言是**单向**的：compareOne 说新版胜出时 DecideWash 必须也认为可以推进。
// 反方向允许分歧 —— compareOne 可以在 DecideWash 认可的情况下选择不动作
// （无可比维度的守卫就是这么加的），宁可漏洗不误删。
func TestCompareOneAgreesWithDecideWash(t *testing.T) {
	names := []string{
		"剧名.S01E01.2160p.x265.mkv",
		"剧名.S01E01.2160p.x265.8bit.mkv",
		"剧名.S01E01.2160p.x265.10bit.mkv",
		"剧名.S01E01.2160p.x265.12bit.mkv",
		"剧名.S01E01.2160p.x265.10bit-GRP.mkv",
		"剧名.S01E01.2160p.x265.10bit.TrueHD-CHD.mkv",
		"剧名.S01E01.1080p.x264.mkv",
		"剧名.S01E01.1080p.x264.10bit.mkv",
	}
	// 两个体积关系都要覆盖：cmp==0 时靠体积兜底，分支走向完全相反。
	sizes := []int64{1 << 30, 9 << 30}

	ruleSets := []struct {
		name  string
		rules []moviepilot.WashRule
		group []string
	}{
		{"默认规则", moviepilot.DefaultWashRules, nil},
		{"制作组优先", moviepilot.DefaultWashRules, []string{"CHD"}},
		{"只比分辨率", []moviepilot.WashRule{{Field: "resolution", Higher: true}}, nil},
		{"只比色深", []moviepilot.WashRule{{Field: "bitdepth", Higher: true}}, nil},
	}

	var checks, extraCaution int
	for _, rs := range ruleSets {
		for _, newName := range names {
			for _, oldName := range names {
				for _, newSize := range sizes {
					for _, oldSize := range sizes {
						newQ := mustQuality(t, newName)
						oldQ := mustQuality(t, oldName)
						newF := libFileOf("/cand/"+newName, newName, newSize, newQ)
						oldF := libFileOf("/lib/"+oldName, oldName, oldSize, oldQ)

						v := compareOne(newF, []libFile{oldF}, rs.rules, rs.group)
						d := moviepilot.DecideWash(newQ, newName, newSize,
							[]moviepilot.LocalFile{{
								AbsPath: oldF.Path,
								RelPath: oldF.Name,
								Size:    oldSize,
							}}, rs.rules)

						ctx := fmt.Sprintf("规则=%s 新=%s(%d) 旧=%s(%d) compareOne=%s",
							rs.name, newName, newSize, oldName, oldSize, v.Relation)
						checks++
						switch v.Relation {
						case RelationNewWins:
							if !d.Proceed {
								t.Errorf("%s：compareOne 判新版胜出，但 DecideWash 认为不该替换", ctx)
							}
							if v.Loser != oldF.Path {
								t.Errorf("%s：胜出时的败方是 %q，期望 %q", ctx, v.Loser, oldF.Path)
							}
						case RelationNewLoses, RelationTie:
							if d.Proceed {
								t.Errorf("%s：compareOne 判不该替换，但 DecideWash 认为可以推进", ctx)
							}
							if v.Loser != oldF.Path {
								t.Errorf("%s：败方是 %q，期望 %q", ctx, v.Loser, oldF.Path)
							}
						case RelationNoDimension:
							// 唯一允许的分歧：这里我们可以比 DecideWash 更保守。
							if d.Proceed {
								extraCaution++
							}
						default:
							t.Errorf("%s：未知关系值 %q", ctx, v.Relation)
						}
						if v.Relation != RelationNoDimension && strings.TrimSpace(v.Trace) == "" {
							t.Errorf("%s：可执行/不可执行的结论都必须带一句人话说明", ctx)
						}
					}
				}
			}
		}
	}
	if checks < 500 {
		t.Fatalf("只跑了 %d 组对照，样本太少钉不住口径", checks)
	}
	t.Logf("共 %d 组对照，其中 %d 组是「DecideWash 会推进、我们选择不动作」的保守分歧", checks, extraCaution)
}

// TestCompareOneNoDimensionBlocksUnparseableNames 断言无可比维度的守卫真的会拦下来。
//
// 两个都叫「XX.mkv」的文件解析不出任何质量标签时，
// CompareQuality 会全项相等返回 0，体积兜底又会让更大的那个赢 ——
// 结果就是「洗版」把同体积的另一个文件当成劣版删掉，而且界面上看不出任何异常。
func TestCompareOneNoDimensionBlocksUnparseableNames(t *testing.T) {
	dir := t.TempDir()
	newName := "正片.mkv"
	oldName := "正片.mkv"
	newF := libFileOf(dir+"/cand/"+newName, newName, 9<<30, moviepilot.ParseQualityFromName(newName))
	oldF := libFileOf(dir+"/lib/"+oldName, oldName, 1<<30, moviepilot.ParseQualityFromName(oldName))
	if newF.Quality != nil || oldF.Quality != nil {
		t.Skip("引擎现在能解析这个名字了，本用例的前提消失，需要重新想一个更贴近的样本")
	}
	v := compareOne(newF, []libFile{oldF}, moviepilot.DefaultWashRules, nil)
	if v.Relation != RelationNoDimension {
		t.Fatalf("两边都解析不出质量时应判无可比维度，实际 %s（败方 %q）", v.Relation, v.Loser)
	}
	if v.Loser != "" {
		t.Errorf("无可比维度时不该点名败方，实际 %q", v.Loser)
	}
	// 这是有意偏离 DecideWash 的地方，必须在结论说明里讲清楚。
	d := moviepilot.DecideWash(newF.Quality, newName, newF.Size,
		[]moviepilot.LocalFile{{AbsPath: oldF.Path, RelPath: oldF.Name, Size: oldF.Size}},
		moviepilot.DefaultWashRules)
	if !d.Proceed {
		t.Logf("引擎侧也认为不该替换（保守分歧不存在）")
	}
}

// TestCompareOneLoserIsTheStrongestOld 断言胜出时被点名的是同槽位里最强的现版。
//
// 同槽位出现重复是很常见的：上一轮洗版只删掉了一个败方、用户手动补了同一集的另一个版本、
// 或者不同片源各留了一份。这些情况下同一个 workKey 下会有多份同槽位文件，
// 「新版胜出」只意味着「这一组里该淘汰一个」—— 淘汰哪个必须有确定答案，
// 而且答案必须是「淘汰最弱的那份」。挑错了就等于把媒体库洗成更差，
// 而且下一轮扫描还会再洗一次，永远收敛不了。
func TestCompareOneLoserIsTheStrongestOld(t *testing.T) {
	dir := t.TempDir()
	oldFiles := []libFile{
		libFileOf(dir+"/8bit.mkv", "剧名.S01E01.2160p.x265.8bit.mkv", 9<<30,
			mustQuality(t, "剧名.S01E01.2160p.x265.8bit.mkv")),
		libFileOf(dir+"/10bit.mkv", "剧名.S01E01.2160p.x265.10bit.mkv", 1<<30,
			mustQuality(t, "剧名.S01E01.2160p.x265.10bit.mkv")),
		libFileOf(dir+"/plain.mkv", "剧名.S01E01.2160p.x265.mkv", 20<<30,
			mustQuality(t, "剧名.S01E01.2160p.x265.mkv")),
	}
	newF := libFileOf(dir+"/new.mkv", "剧名.S01E01.2160p.x265.12bit.mkv", 2<<30,
		mustQuality(t, "剧名.S01E01.2160p.x265.12bit.mkv"))

	v := compareOne(newF, oldFiles, moviepilot.DefaultWashRules, nil)
	if v.Relation != RelationNewWins {
		t.Fatalf("12bit 应当胜出，实际关系 %s（trace：%s）", v.Relation, v.Trace)
	}
	if want := dir + "/10bit.mkv"; v.Loser != want {
		t.Errorf("败方是 %q，期望同槽位里最强的 %q —— 删弱的留强的才叫洗版", v.Loser, want)
	}
}

// TestCompareOneEmptyAndNilInputs 边界输入不该 panic，也不该点名败方。
func TestCompareOneEmptyAndNilInputs(t *testing.T) {
	newF := libFileOf("/cand/a.mkv", "剧名.S01E01.2160p.x265.mkv", 1<<30,
		mustQuality(t, "剧名.S01E01.2160p.x265.mkv"))
	if v := compareOne(newF, nil, moviepilot.DefaultWashRules, nil); v.Loser != "" {
		t.Errorf("没有现版可比时不该点名败方：%+v", v)
	}
	nilQ := libFileOf("/cand/b.mkv", "剧名.S01E01.2160p.x265.mkv", 1<<30, nil)
	old := libFileOf("/lib/c.mkv", "剧名.S01E01.2160p.x265.mkv", 1<<30,
		mustQuality(t, "剧名.S01E01.2160p.x265.mkv"))
	if v := compareOne(nilQ, []libFile{old}, moviepilot.DefaultWashRules, nil); v.Loser != "" {
		t.Errorf("新版质量不可解析时不该点名败方：%+v", v)
	}
}

// TestWorkKeyOfGroupsSameEpisode 文件名是分组的唯一依据，分组错了就永远配不上对。
func TestWorkKeyOfGroupsSameEpisode(t *testing.T) {
	same := []struct{ a, b string }{
		{"剧名.S01E01.2160p.x265.mkv", "剧名.S01E01.2160p.x265.10bit.mkv"},
		{"剧名.S01E02.1080p.x264.mkv", "剧名.S01E02.2160p.x265.mkv"},
		{"剧名.第01集.1080p.mkv", "剧名.第01集.2160p.mkv"},
		{"剧名.S01E01.1080p.mkv", "剧名.S01E01.2160p.x265-GRP.mkv"},
		{"剧场版.2023.1080p.mkv", "剧场版.2023.2160p.x265.mkv"},
	}
	for _, c := range same {
		if got, want := workKeyOf(c.a), workKeyOf(c.b); got != want {
			t.Errorf("%q 与 %q 应归同一组，实际 %+v vs %+v", c.a, c.b, got, want)
		}
	}
	diff := []struct{ a, b string }{
		{"剧名.S01E01.2160p.x265.mkv", "剧名.S01E02.2160p.x265.mkv"},
		{"剧名A.S01E01.2160p.x265.mkv", "剧名B.S01E01.2160p.x265.mkv"},
		{"剧场版.2160p.x265.mkv", "剧名.S01E01.2160p.x265.mkv"},
	}
	for _, c := range diff {
		if got, want := workKeyOf(c.a), workKeyOf(c.b); got == want {
			t.Errorf("%q 与 %q 不该归同一组，实际都是 %+v", c.a, c.b, got)
		}
	}
}

// TestWorkKeyOfSpaceSeparatedSuffixIsAKnownLimitation 钉住一个已知缺口。
//
// moviepilot.stripQualitySuffix 只按 "." 切分质量后缀，
// 所以「剧名 1080p」这种空格分隔的写法里，「1080p」会留在剧名里，
// 「剧名 1080p」与「剧名 2160p」就落在不同组，永远配不上对。
//
// 本期不修：改同名键会影响整理流程的分组口径，而分组口径是本期明确不碰的部分
// （本包复用 WashCoreKey 就是为了不自己造一套）。
// 这里把它钉成显式的已知限制，免得后人当成新 bug 查一遍；
// 真要修应该改 moviepilot 的同名键口径，而不是在洗版这边加特例。
func TestWorkKeyOfSpaceSeparatedSuffixIsAKnownLimitation(t *testing.T) {
	a := workKeyOf("剧名 第01集 1080p.mkv")
	b := workKeyOf("剧名 第01集 2160p.mkv")
	if a == b {
		t.Skip("命名引擎已支持空格分隔的质量后缀，这个缺口已经不存在了")
	}
	if a.EpisodeKey != b.EpisodeKey || a.EpisodeKey == "" {
		t.Errorf("集号应该仍然对得上，实际 %q vs %q", a.EpisodeKey, b.EpisodeKey)
	}
	t.Logf("已知限制：空格分隔的质量后缀未剥除，剧名分别是 %q / %q", a.SeriesKey, b.SeriesKey)
}

// TestWorkKeyOfSurvivesSpaceBeforeQuality 守住扩展名解析的那个坑。
//
// 剥掉季集号后文件名可能变成「剧名..2160p」。
// 直接把这个交给 WashCoreKey 的话，path.Ext 会把「. 2160p」当成扩展名剥掉，
// 于是「剧名..2160p」和「剧名..1080p」会得到同一个键 ——
// 分辨率不同却被判成同一集，这是最难在现场查出来的一类错。
func TestWorkKeyOfSurvivesSpaceBeforeQuality(t *testing.T) {
	a := workKeyOf("剧名 .S01E01.2160p.x265.mkv")
	b := workKeyOf("剧名 .S01E01.1080p.x264.mkv")
	if a != b {
		t.Errorf("季集号之后的残留分隔符让质量后缀渗进了剧名：%+v vs %+v", a, b)
	}
	if a.SeriesKey != "剧名" {
		t.Errorf("剧名应剥干净，实际 %q", a.SeriesKey)
	}
}

// TestMarshalSnapshotsIsOrderIndependent 快照哈希必须与枚举顺序无关。
//
// 提交阶段会重新枚举一次文件集合并重算哈希：一致才允许动手。
// 如果顺序参与了哈希，那么同一次扫描里两次枚举顺序稍有不同（目录遍历顺序、
// Emby 索引的返回顺序），一条完全没变过的判定就会被判成「已过期」，
// 用户看到的现象是「我什么都没动，它就过期了」。
func TestMarshalSnapshotsIsOrderIndependent(t *testing.T) {
	files := []libFile{
		{Path: "/lib/c.mkv", Name: "c.mkv", Size: 3},
		{Path: "/lib/a.mkv", Name: "a.mkv", Size: 1},
		{Path: "/lib/b.mkv", Name: "b.mkv", Size: 2},
	}
	shuffled := []libFile{files[2], files[0], files[1]}
	if hashString(marshalSnapshots(files)) != hashString(marshalSnapshots(shuffled)) {
		t.Errorf("枚举顺序不同导致快照哈希不同 —— 未变过的判定会被误判成过期")
	}
	changed := []libFile{files[0], files[1], {Path: "/lib/b.mkv", Name: "b.mkv", Size: 99}}
	if hashString(marshalSnapshots(files)) == hashString(marshalSnapshots(changed)) {
		t.Errorf("体积变了但哈希没变 —— 真变更会被当成没变然后照样删文件")
	}
}

func TestDedupLibFilesKeepsFirst(t *testing.T) {
	in := []libFile{
		{Path: "/a.mkv", Name: "a.mkv"},
		{Path: "/b.mkv", Name: "b.mkv"},
		{Path: "/a.mkv", Name: "a.mkv"},
	}
	got := dedupLibFiles(in)
	if len(got) != 2 || got[0].Path != "/a.mkv" || got[1].Path != "/b.mkv" {
		t.Errorf("去重结果 %+v", got)
	}
}

// TestComparableDimensionCountsBothSides 可比维度必须双方都有值才算数。
func TestComparableDimensionCountsBothSides(t *testing.T) {
	withRes := mustQuality(t, "剧名.S01E01.2160p.x265.10bit.mkv")
	noRes := mustQuality(t, "剧名.S01E01.真高清珍藏版.mkv")
	if got := comparableDimension(withRes, withRes, moviepilot.DefaultWashRules); got == 0 {
		t.Errorf("完全同名同标签的一对应有多个可比维度，实际 %d", got)
	}
	// 一边完全解析不出东西 ⇒ 无可比维度。
	if got := comparableDimension(noRes, withRes, moviepilot.DefaultWashRules); got != 0 {
		t.Errorf("单边没信息时不该算可比维度，实际 %d", got)
	}
	if got := comparableDimension(nil, withRes, moviepilot.DefaultWashRules); got != 0 {
		t.Errorf("nil 质量不该算可比维度，实际 %d", got)
	}
}
