package inspection

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// MissingChecker 巡检「查漏补缺」：目录里有季、但集数不齐。
//
// 判据：把目录结构解析成 (剧名, 季, 已有集号)，再和 TMDB 上该季的集数比。
// 缺集就是「TMDB 说这一季有 N 集，树上只找到 M 个集号」。
//
// 它为什么不给自动下载的修复动作：补哪一集、要不要补、下载源是什么，
// 是用户的选择而不是系统的。缺集信息本身才是最值钱的产出 ——
// 所以修复动作是空 Kind（只报告），预览里写清缺哪几集。
type MissingChecker struct {
	KeyName   string
	LabelText string
	// Roots 每次扫描现取，理由同 checkers_tree.go。
	Roots     func(ctx context.Context) ([]Root, error)
	Source    TreeSource
	// Expect 返回某剧某一季在 TMDB 上应有的集号集合。
	// 返回空集合表示「查不到」，检查器跳过该季 —— 宁可漏报不可错报。
	Expect func(ctx context.Context, seriesTitle string, season int) ([]int, error)
}

// MissingResult 是一季的缺失结论。
type MissingResult struct {
	Title    string
	Season   int
	Have     []int
	Missing  []int
	SeasonPath string
}

func (c *MissingChecker) Key() string   { return c.KeyName }
func (c *MissingChecker) Label() string { return c.LabelText }

func (c *MissingChecker) Scan(ctx context.Context) ([]Finding, error) {
	if c.Source == nil || c.Expect == nil || c.Roots == nil {
		return nil, nil
	}
	roots, err := c.Roots(ctx)
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, root := range roots {
		nodes, err := c.Source.List(ctx, root)
		if err != nil {
			continue
		}
		tree := NewTree(root, nodes)
		for _, res := range c.scanTree(ctx, tree) {
			out = append(out, c.finding(root, res))
		}
	}
	return out, nil
}

func (c *MissingChecker) scanTree(ctx context.Context, tree *Tree) []MissingResult {
	have := map[string]map[int]map[int]bool{} // 剧名 → 季 → 集号集合
	for _, f := range tree.Files() {
		if !IsMediaName(f.Name) {
			continue
		}
		series, season, ep, ok := parseMediaName(f.Name)
		if !ok {
			continue
		}
		bySeason, ok := have[series]
		if !ok {
			bySeason = map[int]map[int]bool{}
			have[series] = bySeason
		}
		if bySeason[season] == nil {
			bySeason[season] = map[int]bool{}
		}
		bySeason[season][ep] = true
	}
	seriesNames := make([]string, 0, len(have))
	for s := range have {
		seriesNames = append(seriesNames, s)
	}
	sort.Strings(seriesNames)

	var out []MissingResult
	for _, series := range seriesNames {
		seasons := make([]int, 0, len(have[series]))
		for s := range have[series] {
			seasons = append(seasons, s)
		}
		sort.Ints(seasons)
		for _, season := range seasons {
			// present[ep] = 该集号的文件名（可能有多份副本，取第一份即可）。
			present := have[series][season]
			want, err := c.Expect(ctx, series, season)
			if err != nil || len(want) == 0 {
				continue
			}
			var missing []int
			for _, ep := range want {
				if !present[ep] {
					missing = append(missing, ep)
				}
			}
			if len(missing) == 0 {
				continue
			}
			sort.Ints(missing)
			haveList := make([]int, 0, len(present))
			for ep := range present {
				haveList = append(haveList, ep)
			}
			sort.Ints(haveList)
			out = append(out, MissingResult{
				Title:      series,
				Season:     season,
				Have:       haveList,
				Missing:    missing,
				SeasonPath: seasonDirPath(tree, series, season),
			})
		}
	}
	return out
}

func (c *MissingChecker) finding(root Root, res MissingResult) Finding {
	missing := make([]string, len(res.Missing))
	for i, ep := range res.Missing {
		missing[i] = fmt.Sprintf("E%02d", ep)
	}
	have := make([]string, len(res.Have))
	for i, ep := range res.Have {
		have[i] = fmt.Sprintf("E%02d", ep)
	}
	return Finding{
		CheckerKey: c.Key(),
		Kind:       "missing_episodes",
		Target:     fmt.Sprintf("account=%d series=%s season=%d", root.AccountID, res.Title, res.Season),
		Detail: map[string]any{
			"title":   res.Title,
			"season":  res.Season,
			"have":    have,
			"missing": missing,
			"path":    res.SeasonPath,
		},
		Repair: RepairAction{
			Kind:   RepairNone,
			Label:  "仅报告缺集，不自动下载",
			Preview: fmt.Sprintf("%s 第 %d 季缺 %d 集：%s（现有 %s）。" +
				"补哪一集、到哪里下载由你决定，系统不代劳。",
				res.Title, res.Season, len(res.Missing),
				strings.Join(missing, "、"), strings.Join(have, "、")),
			Reversible: false,
		},
	}
}

func fileNameOf(path string) string {
	idx := strings.LastIndex(path, "/")
	if idx < 0 {
		return path
	}
	return path[idx+1:]
}

func seasonDirPath(tree *Tree, series string, season int) string {
	want := seasonFolderPattern(season)
	for _, d := range tree.Dirs() {
		if strings.EqualFold(d.Name, want) {
			return d.Path
		}
	}
	return ""
}

func seasonFolderPattern(season int) string {
	return fmt.Sprintf("Season %02d", season)
}

// parseMediaName 从媒体文件名里解析出剧名、季号、集号。
//
// 只认两种形状，因为只有这两种在本仓的命名规范里是确定的：
//
//	S01E02 / S01.E02          —— 剧集
//	剧名 S01E02                —— 带剧名的剧集
//
// 「第二季」这类中文季名不做解析：中文数字到阿拉伯数字的转换有太多写法
// （二/两/2），猜错的代价是把整季判成缺集。认不出来就跳过，比判错好。
func parseMediaName(name string) (series string, season, episode int, ok bool) {
	base := strings.TrimSuffix(name, fileExt(name))
	upper := strings.ToUpper(base)
	// 逐个 'S' 位置试解析，取**最后**一个成功的。
	//
	// 为什么不能取第一个：剧名本身常含 S（SHOW、SPIDER-MAN），
	// 第一个 S 后面跟的是 H 而不是数字。从右往左试同时解决了
	// 「剧名带 S」和「文件名里有多个 S01」两种情况。
	for idx := strings.LastIndex(upper, "S"); idx >= 0; idx = strings.LastIndex(upper[:idx], "S") {
		season, episode, _, ok2 := parseSeasonEpisode(upper[idx+1:])
		if !ok2 {
			continue
		}
		series = strings.TrimSpace(base[:idx])
		series = strings.Trim(series, " .-_")
		if series == "" {
			series = strings.TrimSpace(base[:idx])
		}
		return series, season, episode, true
	}
	return "", 0, 0, false
}

// parseSeasonEpisode 解析 "01E02" / "01.E02" 形状，返回季号与集号。
//
// 集号之后必须已经到文件名末尾：后面还挂着 1080p 之类的尾巴说明这是版本命名
// 而不是「纯粹的集号」，把它算作某一集会让人以为知道缺的是哪一集，而其实
// 连这集存不存在都没确认。认不出来就跳过，比判错好。
func parseSeasonEpisode(rest string) (season, episode int, tail string, ok bool) {
	sn, consumed, ok := scanTwoDigits(rest)
	if !ok {
		return 0, 0, rest, false
	}
	rest = rest[consumed:]
	rest = strings.TrimLeft(rest, "E-. ")
	en, used, ok := scanTwoDigits(rest)
	if !ok {
		return 0, 0, rest, false
	}
	tail = rest[used:]
	if strings.TrimSpace(tail) != "" {
		return 0, 0, tail, false
	}
	return sn, en, "", true
}

// scanTwoDigits 从字符串开头读两个数字，返回值与消耗长度。
func scanTwoDigits(s string) (value, consumed int, ok bool) {
	if len(s) < 2 || s[0] < '0' || s[0] > '9' || s[1] < '0' || s[1] > '9' {
		return 0, 0, false
	}
	return (int(s[0]-'0'))*10 + int(s[1]-'0'), 2, true
}