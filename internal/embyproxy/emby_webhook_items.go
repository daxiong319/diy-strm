package embyproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"litepan/internal/domain"
	"litepan/internal/embywebhook"
)

// embyItemDetailFields 是入库/播放通知需要的条目字段。
const embyItemDetailFields = "Overview,Genres,People,ProviderIds,ImageTags,DateCreated,ProductionYear,CommunityRating,MediaSources,Path,SeriesName,IndexNumber,ParentIndexNumber"

// embyItemDetail 是 /Items 接口返回的条目结构（只取通知用得到的字段）。
type embyItemDetail struct {
	ID                string            `json:"Id"`
	Name              string            `json:"Name"`
	Type              string            `json:"Type"`
	Overview          string            `json:"Overview"`
	ProductionYear    int               `json:"ProductionYear"`
	CommunityRating   float64           `json:"CommunityRating"`
	Genres            []string          `json:"Genres"`
	People            []embyItemPerson  `json:"People"`
	ProviderIds       map[string]string `json:"ProviderIds"`
	ImageTags         map[string]string `json:"ImageTags"`
	DateCreated       string            `json:"DateCreated"`
	SeriesName        string            `json:"SeriesName"`
	SeriesID          string            `json:"SeriesId"`
	IndexNumber       int               `json:"IndexNumber"`
	ParentIndexNumber int               `json:"ParentIndexNumber"`
	MediaSources      []embyItemSource  `json:"MediaSources"`
}

// embyItemPerson 是条目演职人员。
type embyItemPerson struct {
	Name string `json:"Name"`
	Type string `json:"Type"`
}

// embyItemSource 是条目媒体源。
type embyItemSource struct {
	Path string `json:"Path"`
	Size int64  `json:"Size"`
}

// GetItemDetail 按条目 ID 读取通知所需的详情。
// 条目不存在时返回 (nil, nil)，避免调用方把「没查到」当成故障。
func (s *Service) GetItemDetail(ctx context.Context, itemID string) (*embywebhook.ItemDetail, error) {
	itemID = stripMediaSourcePrefix(strings.TrimSpace(itemID))
	if itemID == "" {
		return nil, nil
	}
	cfg, err := s.resolveConfig("")
	if err != nil {
		return nil, err
	}
	items, err := s.fetchItemDetails(ctx, cfg, func(params map[string][]string) {
		params["Ids"] = []string{itemID}
		params["Limit"] = []string{"1"}
	})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	detail := toItemDetail(items[0])
	return &detail, nil
}

// GetSeriesEpisodes 按剧集 ID 拉取季集列表。
// Emby 4.8 批量入库只推一条合并事件，需要回查具体新增了哪些季集。
func (s *Service) GetSeriesEpisodes(ctx context.Context, seriesID string) (map[int][]int, error) {
	seriesID = stripMediaSourcePrefix(strings.TrimSpace(seriesID))
	if seriesID == "" {
		return nil, nil
	}
	cfg, err := s.resolveConfig("")
	if err != nil {
		return nil, err
	}
	items, err := s.fetchItemDetails(ctx, cfg, func(params map[string][]string) {
		params["ParentId"] = []string{seriesID}
		params["IncludeItemTypes"] = []string{"Episode"}
		params["Recursive"] = []string{"true"}
		params["Limit"] = []string{"200"}
	})
	if err != nil {
		return nil, err
	}
	seasons := make(map[int][]int, len(items))
	for _, item := range items {
		season := item.ParentIndexNumber
		if season <= 0 {
			season = 1
		}
		seasons[season] = append(seasons[season], item.IndexNumber)
	}
	return seasons, nil
}

// FetchMediaItemsByLibraryID 按媒体库 ID 拉取条目详情。
// 保留该方法是给入库通知统计体积/发布组使用，当前为可选路径。
func (s *Service) FetchMediaItemsByLibraryID(ctx context.Context, libraryID string) ([]embywebhook.ItemDetail, error) {
	libraryID = stripMediaSourcePrefix(strings.TrimSpace(libraryID))
	if libraryID == "" {
		return nil, nil
	}
	cfg, err := s.resolveConfig("")
	if err != nil {
		return nil, err
	}
	items, err := s.fetchItemDetails(ctx, cfg, func(params map[string][]string) {
		params["ParentId"] = []string{libraryID}
		params["Recursive"] = []string{"true"}
		params["Limit"] = []string{"200"}
	})
	if err != nil {
		return nil, err
	}
	result := make([]embywebhook.ItemDetail, 0, len(items))
	for _, item := range items {
		result = append(result, toItemDetail(item))
	}
	return result, nil
}

// fetchItemDetails 走 /emby/Items 与 /Items 两个候选端点查询条目。
// mutate 用于按需附加查询参数（Ids / ParentId 等）。
func (s *Service) fetchItemDetails(ctx context.Context, cfg Config, mutate func(params map[string][]string)) ([]embyItemDetail, error) {
	base := strings.TrimRight(cfg.EmbyURL, "/")
	candidates := []string{base + "/emby/Items", base + "/Items"}
	if strings.HasSuffix(strings.ToLower(base), "/emby") {
		candidates = []string{base + "/Items"}
	}
	fields := embyItemDetailFields
	var lastErr error
	for _, endpoint := range candidates {
		params := mediaServerQuery(cfg.APIKey)
		params.Set("Fields", fields)
		values := map[string][]string{}
		for key, list := range params {
			values[key] = append([]string(nil), list...)
		}
		if mutate != nil {
			mutate(values)
		}
		query := url.Values{}
		for key, list := range values {
			query[key] = list
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
		if err != nil {
			lastErr = err
			continue
		}
		setMediaServerAuth(req, cfg.APIKey)
		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		var payload struct {
			Items []embyItemDetail `json:"Items"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			lastErr = embyTestHTTPError(resp.StatusCode)
			continue
		}
		if decodeErr != nil {
			lastErr = domain.Wrap(domain.CodeInternal, decodeErr)
			continue
		}
		return payload.Items, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, nil
}

// toItemDetail 把 Emby 返回结构转换成通知渲染结构。
func toItemDetail(item embyItemDetail) embywebhook.ItemDetail {
	people := make([]embywebhook.Person, 0, len(item.People))
	for _, person := range item.People {
		people = append(people, embywebhook.Person{Name: person.Name, Type: person.Type})
	}
	sources := make([]embywebhook.MediaSource, 0, len(item.MediaSources))
	for _, source := range item.MediaSources {
		sources = append(sources, embywebhook.MediaSource{Path: source.Path, Size: source.Size})
	}
	return embywebhook.ItemDetail{
		ID:                item.ID,
		Name:              item.Name,
		Type:              item.Type,
		Overview:          item.Overview,
		ProductionYear:    item.ProductionYear,
		CommunityRating:   item.CommunityRating,
		Genres:            item.Genres,
		People:            people,
		ProviderIDs:       item.ProviderIds,
		ImageTags:         item.ImageTags,
		DateCreated:       item.DateCreated,
		MediaSources:      sources,
		SeriesName:        item.SeriesName,
		IndexNumber:       item.IndexNumber,
		ParentIndexNumber: item.ParentIndexNumber,
	}
}
