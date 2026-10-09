package mediaorganize

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 落盘测试的 fake：一律记录调用，不碰任何真实网盘。

type fakeImage struct {
	fail bool
	hits map[string]int
}

func (f *fakeImage) DownloadImage(_ context.Context, posterPath, _ string) ([]byte, error) {
	if f.fail {
		return nil, errImageBoom{}
	}
	if f.hits == nil {
		f.hits = map[string]int{}
	}
	f.hits[posterPath]++
	return []byte("image:" + posterPath), nil
}

type errImageBoom struct{}

func (errImageBoom) Error() string { return "图片服务不可用" }

type fakeSink struct {
	writes  map[string]string
	order   []string
	exists  map[string]bool
	failPut bool
}

func newFakeSink() *fakeSink {
	return &fakeSink{writes: map[string]string{}, exists: map[string]bool{}}
}

func (f *fakeSink) WriteText(ctx context.Context, accountID int64, parentID, name string, body []byte) error {
	return f.WriteBinary(ctx, accountID, parentID, name, body)
}

func (f *fakeSink) WriteBinary(_ context.Context, _ int64, _ string, name string, body []byte) error {
	if f.failPut {
		return errSinkBoom{}
	}
	if f.writes == nil {
		f.writes = map[string]string{}
	}
	f.writes[name] = string(body)
	f.order = append(f.order, name)
	return nil
}

func (f *fakeSink) Exists(_ context.Context, _ int64, _ string, name string) (bool, error) {
	return f.exists[name], nil
}

type errSinkBoom struct{}

func (errSinkBoom) Error() string { return "网盘写入失败" }

type collectingLog struct{ warns []string }

func (l *collectingLog) Warn(msg string, _ ...any) { l.warns = append(l.warns, msg) }

func readLocal(t *testing.T, dir, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", name, err)
	}
	return string(raw)
}

// TestWorkNFOParsesAsXML 是本任务的核心验收：「NFO 能被识别」
// 不能靠肉眼看生成的字符串，必须真的把它当 XML 解析一遍。
func TestWorkNFOParsesAsXML(t *testing.T) {
	dir := t.TempDir()
	svc := &ScrapeNFOService{}
	ctx := context.Background()

	if err := svc.WriteWorkMetadata(ctx, ScrapeWorkInput{
		Dir:       dir,
		Title:     "完美世界",
		Year:      2019,
		MediaType: MediaTypeTV,
		TMDBID:    281392,
		Plot:      "少年云翔因为一场意外穿越到了另一个世界。",
	}); err != nil {
		t.Fatalf("剧集 NFO 落盘失败：%v", err)
	}
	raw := readLocal(t, dir, "tvshow.nfo")
	if !strings.HasPrefix(raw, "<?xml") {
		t.Fatalf("NFO 缺少 XML 声明：%.40s", raw)
	}
	var show struct {
		XMLName   xml.Name `xml:"tvshow"`
		Title     string   `xml:"title"`
		SortTitle string   `xml:"sorttitle"`
		Year      int      `xml:"year"`
		Plot      string   `xml:"plot"`
		TMDBID    int64    `xml:"tmdbid"`
		ID        string   `xml:"id"`
		Premiered string   `xml:"premiered"`
	}
	if err := xml.Unmarshal([]byte(raw), &show); err != nil {
		t.Fatalf("剧集 NFO 无法被解析：%v\n%s", err, raw)
	}
	if show.XMLName.Local != "tvshow" {
		t.Fatalf("根节点应为 tvshow，实际 %s", show.XMLName.Local)
	}
	if show.Title != "完美世界" || show.Year != 2019 || show.TMDBID != 281392 {
		t.Fatalf("剧集 NFO 字段不对：%+v", show)
	}
	if show.Premiered != "2019-01-01" {
		t.Fatalf("premiered 应为 2019-01-01，实际 %q", show.Premiered)
	}

	if err := svc.WriteWorkMetadata(ctx, ScrapeWorkInput{
		Dir:       dir,
		Title:     "流浪地球 2",
		Year:      2023,
		MediaType: MediaTypeMovie,
		TMDBID:    76600,
	}); err != nil {
		t.Fatalf("电影 NFO 落盘失败：%v", err)
	}
	raw = readLocal(t, dir, "流浪地球 2 (2023).nfo")
	var movie struct {
		XMLName xml.Name `xml:"movie"`
		Title   string   `xml:"title"`
		ID      string   `xml:"id"`
	}
	if err := xml.Unmarshal([]byte(raw), &movie); err != nil {
		t.Fatalf("电影 NFO 无法被解析：%v\n%s", err, raw)
	}
	if movie.XMLName.Local != "movie" || movie.Title != "流浪地球 2" || movie.ID != "76600" {
		t.Fatalf("电影 NFO 字段不对：%+v", movie)
	}
}

func TestSeasonAndEpisodeNFOParsesAsXML(t *testing.T) {
	dir := t.TempDir()
	svc := &ScrapeNFOService{}
	ctx := context.Background()

	if err := svc.WriteSeasonMetadata(ctx, ScrapeSeasonInput{Dir: dir, Title: "完美世界", Season: 2, Premiered: "2020"}); err != nil {
		t.Fatalf("季 NFO 落盘失败：%v", err)
	}
	var season struct {
		XMLName xml.Name `xml:"season"`
		Title   string   `xml:"title"`
		Number  int      `xml:"seasonnumber"`
	}
	if err := xml.Unmarshal([]byte(readLocal(t, dir, "season2.nfo")), &season); err != nil {
		t.Fatalf("季 NFO 无法被解析：%v", err)
	}
	if season.Title != "完美世界" || season.Number != 2 {
		t.Fatalf("季 NFO 字段不对：%+v", season)
	}

	if err := svc.WriteEpisodeMetadata(ctx, ScrapeEpisodeInput{
		Dir: dir, Title: "少年归来", Season: 2, Episode: 5, ShowTitle: "完美世界", TMDBID: 281392,
	}); err != nil {
		t.Fatalf("集 NFO 落盘失败：%v", err)
	}
	var ep struct {
		XMLName  xml.Name `xml:"episodedetails"`
		Title    string   `xml:"title"`
		Season   int      `xml:"season"`
		Episode  int      `xml:"episode"`
		ShowLink string   `xml:"showtitle"`
	}
	if err := xml.Unmarshal([]byte(readLocal(t, dir, "S02E05 少年归来.nfo")), &ep); err != nil {
		t.Fatalf("集 NFO 无法被解析：%v", err)
	}
	if ep.Title != "少年归来" || ep.Season != 2 || ep.Episode != 5 || ep.ShowLink != "完美世界" {
		t.Fatalf("集 NFO 字段不对：%+v", ep)
	}
}

// TestEpisodeNFOFallsBackToCodeOnly covers 「没有集标题也别不写」：
// 缺标题时用集号当文件名，好过让 Emby 那边读不到任何元数据。
func TestEpisodeNFOFallsBackToCodeOnly(t *testing.T) {
	dir := t.TempDir()
	svc := &ScrapeNFOService{}
	if err := svc.WriteEpisodeMetadata(context.Background(), ScrapeEpisodeInput{Dir: dir, Season: 1, Episode: 7}); err != nil {
		t.Fatalf("集 NFO 落盘失败：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "S01E07.nfo")); err != nil {
		t.Fatalf("应落出 S01E07.nfo：%v", err)
	}
}

func TestInvalidSeasonOrEpisodeIsRejected(t *testing.T) {
	dir := t.TempDir()
	svc := &ScrapeNFOService{}
	ctx := context.Background()
	if err := svc.WriteSeasonMetadata(ctx, ScrapeSeasonInput{Dir: dir, Season: 0}); err == nil {
		t.Fatal("季号 0 应被拒绝")
	}
	if err := svc.WriteEpisodeMetadata(ctx, ScrapeEpisodeInput{Dir: dir, Season: 1, Episode: -1}); err == nil {
		t.Fatal("集号 -1 应被拒绝")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("非法集季号不该留下文件，实际 %d 个", len(entries))
	}
}

func TestLocalWriteRequiresDir(t *testing.T) {
	svc := &ScrapeNFOService{}
	err := svc.WriteWorkMetadata(context.Background(), ScrapeWorkInput{Title: "某剧", Dir: "  "})
	if err == nil || !strings.Contains(err.Error(), "目标目录为空") {
		t.Fatalf("空目录应被拒绝，实际 %v", err)
	}
}

func TestEmptyTitleIsRejected(t *testing.T) {
	dir := t.TempDir()
	svc := &ScrapeNFOService{}
	if err := svc.WriteWorkMetadata(context.Background(), ScrapeWorkInput{Dir: dir, Title: "   "}); err == nil {
		t.Fatal("空标题应被拒绝")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("空标题不该留下文件，实际 %d 个", len(entries))
	}
}

// TestImagesAreWrittenAlongsideNFO covers 剧集与电影两种命名习惯。
func TestImagesAreWrittenAlongsideNFO(t *testing.T) {
	dir := t.TempDir()
	img := &fakeImage{}
	svc := &ScrapeNFOService{Images: img}
	ctx := context.Background()

	if err := svc.WriteWorkMetadata(ctx, ScrapeWorkInput{
		Dir: dir, Title: "完美世界", Year: 2019, MediaType: MediaTypeTV,
		PosterURL: "/p.jpg", FanartURL: "/f.jpg",
	}); err != nil {
		t.Fatalf("剧集落盘失败：%v", err)
	}
	for _, name := range []string{"poster.jpg", "fanart.jpg"} {
		if raw := readLocal(t, dir, name); !strings.Contains(raw, "/") {
			t.Fatalf("%s 内容不对：%q", name, raw)
		}
	}

	if err := svc.WriteWorkMetadata(ctx, ScrapeWorkInput{
		Dir: dir, Title: "流浪地球 2", Year: 2023, MediaType: MediaTypeMovie, PosterURL: "/p2.jpg",
	}); err != nil {
		t.Fatalf("电影落盘失败：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "流浪地球 2 (2023)-poster.jpg")); err != nil {
		t.Fatalf("电影应落出 <基名>-poster.jpg：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "poster.jpg")); err != nil {
		t.Fatalf("电影应同时落出 poster.jpg：%v", err)
	}
}

// TestImageFailureNeverFailsTheWholeWrite 是本模块最重要的一条约定：
// 海报挂了不能连 NFO 一起丢。
func TestImageFailureNeverFailsTheWholeWrite(t *testing.T) {
	dir := t.TempDir()
	log := &collectingLog{}
	svc := &ScrapeNFOService{Images: &fakeImage{fail: true}, Log: log}
	err := svc.WriteWorkMetadata(context.Background(), ScrapeWorkInput{
		Dir: dir, Title: "完美世界", Year: 2019, MediaType: MediaTypeTV, PosterURL: "/p.jpg",
	})
	if err != nil {
		t.Fatalf("图片失败不该让整次落盘失败：%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "tvshow.nfo")); statErr != nil {
		t.Fatalf("NFO 仍应落盘：%v", statErr)
	}
	if len(log.warns) == 0 {
		t.Fatal("图片失败必须记警告，否则用户无从得知为什么没有海报")
	}
}

func TestExistingFileIsNotRewritten(t *testing.T) {
	dir := t.TempDir()
	img := &fakeImage{}
	svc := &ScrapeNFOService{Images: img}
	ctx := context.Background()
	in := ScrapeWorkInput{Dir: dir, Title: "完美世界", Year: 2019, MediaType: MediaTypeTV, PosterURL: "/p.jpg"}
	if err := svc.WriteWorkMetadata(ctx, in); err != nil {
		t.Fatalf("首次落盘失败：%v", err)
	}
	// 把海报改成一个哨兵内容，第二次落盘若覆盖它，说明「已存在就跳过」没生效。
	sentinel := filepath.Join(dir, "poster.jpg")
	if err := os.WriteFile(sentinel, []byte("哨兵"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := svc.WriteWorkMetadata(ctx, in); err != nil {
		t.Fatalf("二次落盘失败：%v", err)
	}
	if raw, _ := os.ReadFile(sentinel); string(raw) != "哨兵" {
		t.Fatalf("已存在的文件被覆盖了：%q", raw)
	}
	if img.hits["/p.jpg"] != 1 {
		t.Fatalf("图片只该下载一次，实际 %d 次", img.hits["/p.jpg"])
	}
}

// TestCloudTargetWritesThroughSink covers 目标是网盘时不再碰本地目录。
func TestCloudTargetWritesThroughSink(t *testing.T) {
	dir := t.TempDir()
	sink := newFakeSink()
	svc := &ScrapeNFOService{Uploader: sink}
	if err := svc.WriteWorkMetadata(context.Background(), ScrapeWorkInput{
		Dir: dir, Title: "完美世界", Year: 2019, MediaType: MediaTypeTV, AccountID: 7, ParentID: "parent-1",
	}); err != nil {
		t.Fatalf("网盘落盘失败：%v", err)
	}
	if _, ok := sink.writes["tvshow.nfo"]; !ok {
		t.Fatalf("应经网盘写入 tvshow.nfo，实际写了 %v", sink.writes)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("网盘目标不该同时写本地目录，实际留下 %d 个文件", len(entries))
	}
}

// TestCloudWithoutUploaderFallsBackToLocal covers 没装配上传器时的降级：
// 写本地，而不是报错——整理本身不该因为元数据通道缺失而失败。
func TestCloudWithoutUploaderFallsBackToLocal(t *testing.T) {
	dir := t.TempDir()
	svc := &ScrapeNFOService{}
	if err := svc.WriteWorkMetadata(context.Background(), ScrapeWorkInput{
		Dir: dir, Title: "完美世界", MediaType: MediaTypeTV, AccountID: 7,
	}); err != nil {
		t.Fatalf("无上传器时应落本地：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tvshow.nfo")); err != nil {
		t.Fatalf("应落本地 tvshow.nfo：%v", err)
	}
}

func TestSinkWriteFailureIsReported(t *testing.T) {
	svc := &ScrapeNFOService{Uploader: newFakeSink()}
	svc.Uploader.(*fakeSink).failPut = true
	if err := svc.WriteWorkMetadata(context.Background(), ScrapeWorkInput{
		Dir: t.TempDir(), Title: "完美世界", MediaType: MediaTypeTV, AccountID: 7,
	}); err == nil {
		t.Fatal("网盘写入失败必须报出来，不能静默丢 NFO")
	}
}

// ---- ParseTMDBWorkDetail ----

func TestParseTMDBWorkDetailMovie(t *testing.T) {
	raw := json.RawMessage(`{"title":"流浪地球 2","original_title":"流浪地球2","overview":"太阳即将毁灭。","poster_path":"/p.jpg","backdrop_path":"/b.jpg","release_date":"2023-01-22"}`)
	got, err := ParseTMDBWorkDetail(raw)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	want := tmdbWorkDetail{Title: "流浪地球 2", OriginalTitle: "流浪地球2", Year: 2023, Plot: "太阳即将毁灭。", PosterURL: "/p.jpg", FanartURL: "/b.jpg"}
	if got != want {
		t.Fatalf("解析结果不对：%+v", got)
	}
}

// TestParseTMDBWorkDetailFallsBackToTvFields covers 剧集响应里
// 没有 title/release_date，只有 name/first_air_date —— 这是真实响应形态。
func TestParseTMDBWorkDetailFallsBackToTvFields(t *testing.T) {
	raw := json.RawMessage(`{"name":"完美世界","original_name":"","overview":"","poster_path":"/p.jpg","first_air_date":"2019-07-31"}`)
	got, err := ParseTMDBWorkDetail(raw)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if got.Title != "完美世界" || got.Year != 2019 || got.OriginalTitle != "" {
		t.Fatalf("剧集回落不对：%+v", got)
	}
}

func TestParseTMDBWorkDetailRejectsGarbage(t *testing.T) {
	if _, err := ParseTMDBWorkDetail(nil); err == nil {
		t.Fatal("空输入应报错")
	}
	if _, err := ParseTMDBWorkDetail(json.RawMessage(`{不是 json`)); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
	// 有 title 但没有年份：解析必须成功且年份为 0，绝不能因为缺字段整条失败
	// ——那会让「海报有了、简介没了」这种降级变成「什么都不写」。
	got, err := ParseTMDBWorkDetail(json.RawMessage(`{"title":"无年份"}`))
	if err != nil || got.Year != 0 || got.Title != "无年份" {
		t.Fatalf("缺年份不该导致失败：%+v err=%v", got, err)
	}
}

// ---- 辅助函数 ----

func TestNormalizeMediaTypeDefaultsToMovie(t *testing.T) {
	cases := map[string]string{
		"":          MediaTypeMovie,
		"movie":     MediaTypeMovie,
		"tv":        MediaTypeTV,
		"TVShow":    MediaTypeTV,
		"剧集":        MediaTypeTV,
		"garbage":   MediaTypeMovie,
		"电视剧":       MediaTypeTV,
		"  series ": MediaTypeTV,
	}
	for in, want := range cases {
		if got := normalizeMediaType(in); got != want {
			t.Fatalf("normalizeMediaType(%q)=%q，期望 %q", in, got, want)
		}
	}
}

func TestSortTitleDropsLeadingArticle(t *testing.T) {
	cases := map[string]string{
		"The Matrix":       "Matrix",
		"A Beautiful Mind": "Beautiful Mind",
		"An Endless Night": "Endless Night",
		"流浪地球":             "流浪地球",
	}
	for in, want := range cases {
		if got := sortTitle(in); got != want {
			t.Fatalf("sortTitle(%q)=%q，期望 %q", in, got, want)
		}
	}
}

func TestYearFromDate(t *testing.T) {
	cases := map[string]int{"2023-01-22": 2023, "2019": 2019, "": 0, "abc": 0, "20xx-01-01": 0, "0000-01-01": 0}
	for in, want := range cases {
		if got := yearFromDate(in); got != want {
			t.Fatalf("yearFromDate(%q)=%d，期望 %d", in, got, want)
		}
	}
}

// TestEpisodeBaseNameSanitizesTitles covers 文件名里的斜杠必须被清掉：
// 带斜杠的名字会静默变成两级目录。
func TestEpisodeBaseNameSanitizesTitles(t *testing.T) {
	got := episodeBaseName("前半生/后半生", 1, 2)
	if strings.ContainsAny(got, "/\\") {
		t.Fatalf("集基名不应含路径分隔符：%q", got)
	}
	if !strings.HasPrefix(got, "S01E02 ") {
		t.Fatalf("集基名应以集号开头：%q", got)
	}
}

func TestIsYear(t *testing.T) {
	cases := map[string]bool{"2019": true, "199": false, "20x9": false, "": false, "20199": false}
	for in, want := range cases {
		if got := isYear(in); got != want {
			t.Fatalf("isYear(%q)=%v，期望 %v", in, got, want)
		}
	}
}

// TestEpisodeTitleFromName covers 从整理后的文件名反推集标题。
func TestEpisodeTitleFromName(t *testing.T) {
	cases := map[string]string{
		"S01E02 少年归来.mkv": "少年归来",
		"S01E02.mkv":      "",
		"完美世界 S01E02.mkv": "S01E02",
	}
	for in, want := range cases {
		if got := episodeTitleFromName(in); got != want {
			t.Fatalf("episodeTitleFromName(%q)=%q，期望 %q", in, got, want)
		}
	}
}

func TestFileNFOBasePicksPerMediaType(t *testing.T) {
	if got := fileNFOBase("完美世界", MediaTypeTV, 2019); got != "tvshow" {
		t.Fatalf("剧集 NFO 名应为 tvshow，实际 %q", got)
	}
	if got := fileNFOBase("流浪地球 2", MediaTypeMovie, 2023); got != "流浪地球 2 (2023)" {
		t.Fatalf("电影 NFO 名不对：%q", got)
	}
	// 没有年份时不能留出「(0)」这种一眼假的括号。
	if got := fileNFOBase("某片", MediaTypeMovie, 0); strings.Contains(got, "(0)") {
		t.Fatalf("缺年份不该编出 (0)：%q", got)
	}
}

func TestSinkWriteOrderIsDeterministic(t *testing.T) {
	sink := newFakeSink()
	svc := &ScrapeNFOService{Uploader: sink}
	if err := svc.WriteWorkMetadata(context.Background(), ScrapeWorkInput{
		Dir: t.TempDir(), Title: "完美世界", MediaType: MediaTypeTV, AccountID: 1,
	}); err != nil {
		t.Fatalf("落盘失败：%v", err)
	}
	got := append([]string(nil), sink.order...)
	sort.Strings(got)
	if len(got) != 1 || got[0] != "tvshow.nfo" {
		t.Fatalf("没有图片时只应写 NFO，实际 %v", sink.order)
	}
}
