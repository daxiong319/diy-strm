package moviepilot

import (
	"fmt"
	"path"
	"strings"
	"sync"

	"github.com/flosch/pongo2/v5"

	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
)

// 默认命名模板（与历史 buildAutoOrganizeNewName 输出完全一致，作为兜底常量）
const (
	defaultMovieNameTemplate = `{{ title }}{% if year %} ({{ year }}){% endif %}{% if tags %}.{{ tags }}{% endif %}{{ ext }}`
	defaultTvNameTemplate    = `{{ title }}{% if year %}.{{ year }}{% endif %}.{{ s }}{{ e }}.第{{ ep }}集{% if tags %}.{{ tags }}{% endif %}{{ ext }}`
)

var washTemplateCache sync.Map // key: 模板字符串 → *pongo2.Template

// washNamingVars 命名模板上下文变量
type washNamingVars struct {
	Title         string
	Year          int
	Category      string // movie/tv
	Season        int
	Episode       int
	S             string // "S01"
	E             string // "E05"
	EP            int    // 集号（第N集）
	Tags          string // 质量标签段（2160p.WEB-DL.H.265-Ocat）
	Ext           string // ".mp4"
	Resolution    string
	Codec         string
	Audio         string
	Format        string
	HDR           string
	BitDepth      string
	Edition       string
	Group         string
	Customization string // 命中的定制词（如 NF / Baha），未配置为空
	TMDBID        int64
}

func defaultNameTemplateFor(category string) string {
	if category == "tv" {
		return defaultTvNameTemplate
	}
	return defaultMovieNameTemplate
}

func compileWashTemplate(tpl string) (*pongo2.Template, error) {
	if v, ok := washTemplateCache.Load(tpl); ok {
		return v.(*pongo2.Template), nil
	}
	t, err := pongo2.FromString(tpl)
	if err != nil {
		return nil, err
	}
	washTemplateCache.Store(tpl, t)
	return t, nil
}

func (v washNamingVars) pongoContext() pongo2.Context {
	return pongo2.Context{
		"title":         v.Title,
		"year":          v.Year,
		"category":      v.Category,
		"season":        v.Season,
		"episode":       v.Episode,
		"s":             v.S,
		"e":             v.E,
		"ep":            v.EP,
		"tags":          v.Tags,
		"ext":           v.Ext,
		"resolution":    v.Resolution,
		"codec":         v.Codec,
		"audio":         v.Audio,
		"format":        v.Format,
		"hdr":           v.HDR,
		"bitdepth":      v.BitDepth,
		"edition":       v.Edition,
		"group":         v.Group,
		"customization": v.Customization,
		"tmdb_id":       v.TMDBID,
	}
}

// renderWashName 按模板渲染整理后文件名；模板为空或渲染失败时回退默认模板，
// 默认模板亦失败时返回错误（调用方回退历史命名函数）。
func renderWashName(tpl, category string, vars washNamingVars) (string, error) {
	if strings.TrimSpace(tpl) == "" {
		tpl = defaultNameTemplateFor(category)
	}
	t, err := compileWashTemplate(tpl)
	if err != nil {
		helpers.AppLogger.Warnf("洗版命名模板编译失败，使用默认模板：%v", err)
		t, err = compileWashTemplate(defaultNameTemplateFor(category))
		if err != nil {
			return "", err
		}
	}
	out, err := t.Execute(vars.pongoContext())
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if out == "" || strings.Contains(out, "{{") {
		return "", fmt.Errorf("命名模板渲染结果无效：%q", out)
	}
	return out, nil
}

// buildAutoOrganizeNewNameEx 生成整理后的文件名（P2-2 模板化）：
// 用户配置了 movie/tv 命名模板时按模板渲染（变量见 washNamingVars），
// 否则保持历史命名（标题.年份.季集.质量标签），即默认模板输出。
func buildAutoOrganizeNewNameEx(cfg *models.AutoOrganizeConfig, category, title string, season, episode, year int, tmdbID int64, origFileName string, customization string, q *FileQuality) string {
	ext := path.Ext(origFileName)
	tags := extractQualityTags(origFileName, title)

	vars := washNamingVars{
		Title:         title,
		Year:          year,
		Category:      category,
		Season:        season,
		Episode:       episode,
		EP:            episode,
		Tags:          tags,
		Ext:           ext,
		TMDBID:        tmdbID,
		Customization: customization,
	}
	if season > 0 {
		vars.S = fmt.Sprintf("S%02d", season)
	}
	if episode > 0 {
		vars.E = fmt.Sprintf("E%02d", episode)
	}
	if q != nil {
		vars.Resolution = q.ResTag
		vars.Codec = q.CodecTag
		vars.Audio = q.AudioTag
		vars.Format = q.VideoFormat
		vars.HDR = q.HDR
		vars.BitDepth = q.BitDepth
		vars.Edition = q.Edition
		vars.Group = q.Group
	}
	tpl := ""
	if cfg != nil {
		if category == "tv" {
			tpl = cfg.TvNameTemplate
		} else {
			tpl = cfg.MovieNameTemplate
		}
	}
	name, err := renderWashName(tpl, category, vars)
	if err != nil {
		helpers.AppLogger.Warnf("洗版命名模板渲染失败，回退历史命名：%v", err)
		return buildAutoOrganizeNewName(category, title, season, episode, year, origFileName)
	}
	return name
}