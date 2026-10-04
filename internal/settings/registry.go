package settings

import "litepan/internal/domain"

// 全局设置：默认值在代码，DB 仅存用户改过的项。

// 设置键。oauth 复用 domain 常量，保证与驱动层读取一致。
const (
	KeyOAuthServerURL              = domain.SettingOAuthServerURL
	KeyCacheEnabled                = "cache_enabled"
	KeyCacheTTL                    = "cache_ttl"
	KeyCacheMaxItems               = "cache_max_items"
	KeyCacheMemoryLimitMB          = "cache_memory_limit_mb"
	KeyCachePersistenceEnabled     = "cache_persistence_enabled"
	KeyCachePersistenceIntervalMin = "cache_persistence_interval_minutes"
	KeyUploadTaskConcurrency       = "upload_task_concurrency"
	KeyBuiltinOfflineTempDir       = "builtin_offline_temp_dir"
	KeyBuiltinOfflineMaxSpeedMB    = "builtin_offline_max_speed_mb"
	KeyBuiltinOfflineBTPort        = "builtin_offline_bt_port"
	KeyWebDAVCacheEnabled          = "webdav_cache_enabled"
	KeyFuseReadCacheEnabled        = "fuse_read_cache_enabled"
	KeyFuseReadCacheMaxGB          = "fuse_read_cache_max_gb"
	KeyFuseReadCacheRetentionDays  = "fuse_read_cache_retention_days"
	KeyFuseReadCacheEvictionPolicy = "fuse_read_cache_eviction_policy"
	KeyAuthActiveRefresh           = "auth_active_refresh_enabled"
	KeyAccountShowProfile          = "account_show_profile"
	KeyAccountShowMembership       = "account_show_membership"
	KeyLogLevel                    = "log_level"
	KeyLogRetentionDays            = "log_retention_days"
	KeyLogErrorAckAt               = "log_error_ack_at"
	KeyAnnouncementReadVersion     = "announcement_read_version"
	KeyEmbyEnabled                 = "emby_enabled"
	KeyEmbyProxyInstances          = "emby_proxy_instances"
	KeyEmbyDeleteNetdiskEnabled    = "emby_delete_netdisk_enabled"
	KeyEmbyWebhookEnabled          = "emby_webhook_enabled"
	KeyEmbyWebhookAuthEnabled      = "emby_webhook_auth_enabled"
	KeyEmbyNotifyEnabled           = "emby_notify_enabled"
	KeyEmbyPlaybackNotifyEnabled   = "emby_playback_notify_enabled"
	KeyEmbyPlaybackOverviewEnabled = "emby_playback_overview_enabled"
	KeyFnosEnabled                 = "fnos_enabled"
	KeyFnosName                    = "fnos_name"
	KeyFnosURL                     = "fnos_url"
	KeyFnosProxyPort               = "fnos_proxy_port"
	KeyFnosStrmPathMaps            = "fnos_strm_path_maps"
	KeyFnosDirectSTRMClients       = "fnos_direct_strm_clients"
	KeyFnosAdminUsername           = "fnos_admin_username"
	KeyFnosAdminPassword           = "fnos_admin_password"
	KeyStrmToken                   = "strm_token"
	KeyStrmBaseURL                 = "strm_base_url"
	KeyStrmSignatureEnabled        = "strm_signature_enabled"
	KeyStrmDefaultScanInterval     = "strm_default_scan_interval"
	KeyStrmDefaultExtensions       = "strm_default_extensions"
	KeyStrmISOFilenameEnabled      = "strm_iso_filename_enabled"
	KeyStrmMinFileSizeMB           = "strm_min_file_size_mb"
	KeyStrmConflictPolicy          = "strm_conflict_policy"
	KeyStrmTaskConcurrency         = "strm_task_concurrency"
	KeyStrmMetadataExtensions      = "strm_metadata_extensions"
	KeyStrmMetadataMaxSizeMB       = "strm_metadata_max_size_mb"
	KeyStrmMetadataParentEnabled   = "strm_metadata_parent_enabled"
	KeyStrmMetadataSyncMode        = "strm_metadata_sync_mode"
	KeyStrmTool115TreeEnabled      = "strm_tool_115_tree_enabled"
	KeyLocalUploadEnabled          = "local_upload_enabled"
	KeyLocalUploadMappings         = "local_upload_mappings"
	KeyCoverExtractEnabled         = "cover_extract_enabled"
	KeyCoverExtractStyle           = "cover_extract_style"
	KeyQuarkTVEnabled              = "quark_tv_enabled"
	KeyQuarkTVPlayMode             = "quark_tv_play_mode"
	KeyQuarkTVClientListMode       = "quark_tv_client_list_mode"
	KeyQuarkTVProxyClients         = "quark_tv_proxy_clients"
	KeyStrmScrapeWriteMode         = "strm_scrape_write_mode"
	KeyStrmScrapeEpisodeInfo       = "strm_scrape_episode_info"
	KeyStrmScrapeFanart            = "strm_scrape_fanart"
	KeyStrmScrapeActors            = "strm_scrape_actors"
	KeyStrmScrapeClearLogo         = "strm_scrape_clearlogo"
	KeyStrmScrapeScopes            = "strm_scrape_scopes"

	KeyMOProxyEnabled          = "mo_proxy_enabled"
	KeyMOProxyURL              = "mo_proxy_url"
	KeyMOProxyUsername         = "mo_proxy_username"
	KeyMOProxyPassword         = "mo_proxy_password"
	KeyMOTmdbAPIKey            = "mo_tmdb_api_key"
	KeyMOTmdbLanguage          = "mo_tmdb_language"
	KeyMOTmdbAPIHost           = "mo_tmdb_api_host"
	KeyMOTmdbImageHost         = "mo_tmdb_image_host"
	KeyMOTmdbProxyURL          = "mo_tmdb_proxy_url"
	KeyMOAPIRequestIntervalMS  = "mo_api_request_interval_ms"
	KeyMOTmdbRequestIntervalMS = "mo_tmdb_request_interval_ms"
	KeyMOFileExtensions        = "mo_file_extensions"
	KeyMOMetadataExtensions    = "mo_metadata_extensions"
	KeyMOMediaTagOrder         = "mo_media_tag_order"
	KeyMOAlignMediaTags        = "mo_align_media_tags"
	KeyMOMaxWorksPerRun        = "mo_max_works_per_run"
	KeyMOOverwriteExisting     = "mo_overwrite_existing"
	KeyAIOrganizeEnabled       = "ai_organize_enabled"
	KeyAIOrganizeInstances     = "ai_organize_instances"
	KeyAIOrganizeBaseURL       = "ai_organize_base_url"
	KeyAIOrganizeAPIKey        = "ai_organize_api_key"
	KeyAIOrganizeModel         = "ai_organize_model"
	KeyMOClassificationEnabled = "mo_classification_enabled"
	KeyMOClassificationConfig  = "mo_classification_config"
	// KeyDiscoverChannelCatchupHours TG 频道订阅停机追赶限制（小时）。
	// 0 = 不限：停机多久都从旧游标一路回补（原行为）。
	KeyDiscoverChannelCatchupHours = "discover_channel_catchup_hours"
	// 界面偏好：信息条（仪表带）在各页的开合状态，随备份一起导入导出。
	KeyUIBandHiddenStrm     = "ui_band_hidden_strm"
	KeyUIBandHiddenCache    = "ui_band_hidden_cache"
	KeyUIBandHiddenOrganize = "ui_band_hidden_organize"
	KeyUIBandHiddenFuse     = "ui_band_hidden_fuse"

	// MCP Server 与内置智能助理。助手 API Key 属敏感项：不进入普通设置快照。
	KeyMcpEnabled          = "mcp_enabled"
	KeyMcpAllowWriteTools  = "mcp_allow_write_tools"
	KeyMcpDisabledTools    = "mcp_disabled_tools"
	KeyMcpAssistantEnabled = "mcp_assistant_enabled"
	KeyMcpAssistantBaseURL = "mcp_assistant_base_url"
	KeyMcpAssistantAPIKey  = "mcp_assistant_api_key"
	KeyMcpAssistantModel   = "mcp_assistant_model"
	KeyMcpAssistantPrompt  = "mcp_assistant_prompt"
	KeyMcpMaxToolRounds    = "mcp_max_tool_rounds"
	KeyMcpTimeout          = "mcp_timeout"

	// ---- 字幕智能处理（搜索下载 + 智能匹配 + 时间轴校正）----
	// 对齐老版 diy-strm 的 models.SubtitleConfig；老版用单行 subtitle_configs 表存，
	// 现版统一走声明式设置注册表（键值存 configs 表）。
	KeySubtitleEnabled                = "subtitle_enabled"
	KeySubtitleAssrtEnabled           = "subtitle_assrt_enabled"
	KeySubtitleAssrtAPIKey            = "subtitle_assrt_api_key"
	KeySubtitleOpenSubtitlesEnabled   = "subtitle_opensubtitles_enabled"
	KeySubtitleOpenSubtitlesAPIKey    = "subtitle_opensubtitles_api_key"
	KeySubtitleOpenSubtitlesUserAgent = "subtitle_opensubtitles_user_agent"
	KeySubtitleOpenSubtitlesUsername  = "subtitle_opensubtitles_username"
	KeySubtitleOpenSubtitlesPassword  = "subtitle_opensubtitles_password"
	KeySubtitleSubhdEnabled           = "subtitle_subhd_enabled"
	KeySubtitleSubhdCookie            = "subtitle_subhd_cookie"
	KeySubtitleZimukuEnabled          = "subtitle_zimuku_enabled"
	KeySubtitleZimukuCookie           = "subtitle_zimuku_cookie"
	KeySubtitleLanguagePriority       = "subtitle_language_priority"
	KeySubtitleFormatPriority         = "subtitle_format_priority"
	KeySubtitleAutoMatch              = "subtitle_auto_match"
	KeySubtitleAutoDownload           = "subtitle_auto_download"
	KeySubtitleMinMatchScore          = "subtitle_min_match_score"
	KeySubtitleAutoSync               = "subtitle_auto_sync"
	KeySubtitleSyncMinConfidence      = "subtitle_sync_min_confidence"
	KeySubtitleSyncMode               = "subtitle_sync_mode"
	KeySubtitleSyncDryRun             = "subtitle_sync_dry_run"
	KeySubtitleTargetDirPolicy        = "subtitle_target_dir_policy"
	KeySubtitleKeepOriginal           = "subtitle_keep_original"
	KeySubtitleOverwrite              = "subtitle_overwrite"
	KeySubtitleConcurrency            = "subtitle_concurrency"
	KeySubtitleTimeoutSeconds         = "subtitle_timeout_seconds"
	KeySubtitleProxy                  = "subtitle_proxy"
	KeySubtitleUserAgent              = "subtitle_user_agent"
)

// Type 决定后台表单控件与校验方式。
type Type string

const (
	TypeString Type = "string"
	TypeInt    Type = "int"
	TypeBool   Type = "bool"
	TypeSelect Type = "select"
)

// Option 是 select 类型的可选项。
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Spec 声明单个全局设置的元数据，驱动后台表单渲染与写入校验。
type Spec struct {
	Key         string
	Type        Type
	Category    string
	Label       string
	Description string
	Default     string // 默认值的规范字符串形式（与 configs 表存储一致）
	Unit        string
	Min, Max    *int     // 仅 TypeInt
	Options     []Option // 仅 TypeSelect
	Sensitive   bool
	Hidden      bool
	// SilentLog 表示这类设置改动不写「系统设置已更新」日志（界面偏好等低价值噪音）。
	SilentLog bool
	// normalize 对字符串值做规范化/兜底（如 OAuth 地址校验），nil 表示不处理。
	normalize func(string) string
}

// Category 是设置分组，用于后台分区展示。
type Category struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

func intp(n int) *int { return &n }

// defaultSpecs 是全部全局设置的有序声明。新增全局设置只改这里。
func boolSpec(key, category, label, description, def string) Spec {
	return Spec{Key: key, Type: TypeBool, Category: category, Label: label, Description: description, Default: def}
}

func stringSpec(key, category, label, description, def string) Spec {
	return Spec{Key: key, Type: TypeString, Category: category, Label: label, Description: description, Default: def}
}

func intSpec(key, category, label, description, def, unit string, min, max int) Spec {
	return Spec{Key: key, Type: TypeInt, Category: category, Label: label, Description: description, Default: def, Unit: unit, Min: intp(min), Max: intp(max)}
}

func selectSpec(key, category, label, description, def string, options []Option) Spec {
	return Spec{Key: key, Type: TypeSelect, Category: category, Label: label, Description: description, Default: def, Options: options}
}

func defaultSpecs() []Spec {
	return []Spec{
		boolSpec(KeyCacheEnabled, "performance", "启用元数据缓存", "关闭后所有目录列表都直连网盘，不走缓存。", "true"),
		intSpec(KeyCacheTTL, "performance", "全局缓存时间", "缓存过期时间", "30", "分钟", 0, 1440),
		intSpec(KeyCacheMaxItems, "performance", "缓存条目上限", "元数据缓存最多保留的条目数，超出按 LRU 淘汰。", "10000", "条", 1000, 1000000),
		intSpec(KeyCacheMemoryLimitMB, "performance", "缓存内存上限", "元数据缓存的字节软上限，接近上限触发分级淘汰。", "128", "MB", 64, 16384),
		boolSpec(KeyCachePersistenceEnabled, "performance", "启用缓存持久化", "定时将未过期元数据缓存写入磁盘，重启后恢复。", "true"),
		intSpec(KeyCachePersistenceIntervalMin, "performance", "持久化快照间隔", "缓存写入磁盘的间隔，修改后立即生效。", "10", "分钟", 1, 1440),
		intSpec(KeyUploadTaskConcurrency, "performance", "任务并发数", "上传、跨盘下载和内置下载三个队列各自使用该并发上限，队列之间不共享槽位；修改后立即生效。", "3", "个", 1, 5),
		stringSpec(KeyBuiltinOfflineTempDir, "performance", "内置离线临时目录", "内置下载器缓存文件所在的容器内路径；Docker 请先把宿主机目录映射进容器。修改后新任务立即使用。", "data/builtin_offline"),
		intSpec(KeyBuiltinOfflineMaxSpeedMB, "performance", "内置离线限速", "HTTP 与 Magnet 共用的全局下载限速；填 0 表示不限速。", "0", "MB/s", 0, 10240),
		intSpec(KeyBuiltinOfflineBTPort, "performance", "磁力下载端口", "用于磁力/BT 下载连接其他节点；Docker Bridge 网络需同时映射同一 TCP/UDP 端口，Host 网络无需映射。填 0 表示随机端口，修改后立即应用。", "42069", "", 0, 65535),
		boolSpec(KeyWebDAVCacheEnabled, "performance", "WebDAV 路径与 PROPFIND 缓存", "开启后缓存 WebDAV 路径解析与 PROPFIND 响应，减少客户端列目录时的网盘 API 调用。", "true"),
		boolSpec(KeyFuseReadCacheEnabled, "performance", "FUSE 读缓存", "开启后 FUSE 读取过的文件块会写入本地磁盘，与元数据缓存无关。在「文件共享 → 本地挂载」页配置。", "false"),
		intSpec(KeyFuseReadCacheMaxGB, "performance", "FUSE 读缓存容量上限", "磁盘块缓存最大占用，在「文件共享 → 本地挂载」页配置。", "10", "GB", 1, 500),
		intSpec(KeyFuseReadCacheRetentionDays, "performance", "FUSE 读缓存保留天数", "超过该天数的缓存块会被删除，在「文件共享 → 本地挂载」页配置。", "7", "天", 1, 90),
		selectSpec(KeyFuseReadCacheEvictionPolicy, "performance", "FUSE 读缓存淘汰策略", "容量满时的淘汰方式，在「文件共享 → 本地挂载」页配置。", "lru", []Option{
			{Value: "lru", Label: "最近最少使用（LRU）"},
			{Value: "large_file", Label: "大文件优先"},
		}),
		boolSpec(KeyAuthActiveRefresh, "system", "智能主动认证刷新", "后台按 token 有效期预刷新、Cookie 健康检查；关闭后仅保留被动刷新。", "true"),
		boolSpec(KeyAccountShowProfile, "account_display", "显示账号信息", "在网盘账号卡片第二行显示昵称、手机号或邮箱；关闭后显示创建时间。", "true"),
		boolSpec(KeyAccountShowMembership, "account_display", "显示会员标签", "在网盘账号名称后显示该网盘返回的 VIP 或 SVIP 标签。", "true"),
		selectSpec(KeyLogLevel, "system", "日志级别", "控制控制台与落盘日志的最低级别；认证调度、刷新结果等默认可在 Info 查看。", "info", []Option{
			{Value: "debug", Label: "Debug（调试）"},
			{Value: "info", Label: "Info（常规）"},
			{Value: "warn", Label: "Warn（警告）"},
			{Value: "error", Label: "Error（错误）"},
		}),
		intSpec(KeyLogRetentionDays, "system", "日志保留天数", "按天落盘日志的保留期。自动清理与日志页手动清理都会按该天数删除更早的旧日志。", "30", "天", 1, 365),
		{Key: KeyEmbyEnabled, Type: TypeBool, Default: "false", Hidden: true},
		{Key: KeyEmbyProxyInstances, Type: TypeString, Default: "[]", Sensitive: true, Hidden: true},
		boolSpec(KeyEmbyDeleteNetdiskEnabled, "emby", "Emby 删除联动网盘删除", "默认关闭。开启后，在 Emby 删除影片/剧集时同步删除本地索引对应的网盘文件；映射不唯一或不明确时只会记录日志，不会删除。", "false"),
		boolSpec(KeyEmbyWebhookEnabled, "emby", "启用 Emby Webhook 接收", "开启后对外开放 POST /open/emby/webhook，Emby 入库/删除/播放事件会转成站内通知。需先在 Emby 后台添加 Webhook。", "false"),
		boolSpec(KeyEmbyWebhookAuthEnabled, "emby", "Webhook 校验 API Key", "开启后调用 Webhook 必须携带 X-API-Key 请求头或 api_key 查询参数，且该 Key 必须已在「API Key」页面创建。", "false"),
		boolSpec(KeyEmbyNotifyEnabled, "emby", "入库与删除通知", "开启后 Emby 新增媒体、删除媒体时推送通知；单集入库会在 10 秒窗口内合并成一条剧集通知。", "true"),
		boolSpec(KeyEmbyPlaybackNotifyEnabled, "emby", "播放状态通知", "开启后 Emby 开始播放/暂停/停止时推送通知；相同用户与设备的同一事件 1 分钟内只通知一次。", "false"),
		boolSpec(KeyEmbyPlaybackOverviewEnabled, "emby", "播放通知附带简介", "开启后播放通知里会附带影片简介（最多 100 字）。", "false"),
		boolSpec(KeyFnosEnabled, "fnos", "启用飞牛影视反代", "开启后且填写反代端口时，diy-strm 会启动飞牛影视反代服务。", "false"),
		stringSpec(KeyFnosName, "fnos", "飞牛影视配置名称", "飞牛影视反代配置的名称，仅用于界面区分。", "飞牛影视"),
		stringSpec(KeyFnosURL, "fnos", "飞牛影视地址", "飞牛影视服务地址，默认端口 8005，例如 http://192.168.1.10:8005。", ""),
		stringSpec(KeyFnosProxyPort, "fnos", "反代端口", "可留空。填写并启用后，diy-strm 会在该端口启动飞牛影视反代服务。", ""),
		stringSpec(KeyFnosStrmPathMaps, "fnos", "飞牛 STRM 目录", "填写 Docker 中映射到 /app/strm 的左边路径。例：/vol1/.../diy-strmGO:/app/strm → 填 /vol1/.../diy-strmGO。两边相同可留空。", ""),
		{Key: KeyFnosDirectSTRMClients, Type: TypeString, Default: "Infuse", Hidden: true},
		{Key: KeyFnosAdminUsername, Type: TypeString, Default: "", Hidden: true},
		{Key: KeyFnosAdminPassword, Type: TypeString, Default: "", Sensitive: true, Hidden: true},
		stringSpec(KeyStrmToken, "strm", "STRM 播放令牌", "STRM 播放路径鉴权令牌，请在系统设置「API 秘钥」中管理。", ""),
		stringSpec(KeyStrmBaseURL, "strm", "STRM 基础地址", "生成本地 .strm 时使用的站点基址（例如 https://example.com）。留空时使用当前服务监听地址。", ""),
		boolSpec(KeyStrmSignatureEnabled, "strm", "启用 STRM 路径签名", "开启后 STRM 播放地址必须携带有效签名。", "false"),
		intSpec(KeyStrmDefaultScanInterval, "strm", "STRM 默认扫描间隔", "新建任务未指定扫描间隔时使用。", "360", "分钟", 1, 1440),
		stringSpec(KeyStrmDefaultExtensions, "strm", "默认同步文件类型", "STRM 任务未单独指定扩展名时使用，英文分号分隔。", "mp4;mkv;avi;mov;wmv;flv;ts;m2ts;mpg;mpeg;webm;m4v;iso;rmvb;mp3;flac;aac;wav;m4a"),
		boolSpec(KeyStrmISOFilenameEnabled, "strm", "ISO 使用 .iso.strm 文件名", "开启后网盘 .iso 文件生成“文件名.iso.strm”，方便 Infuse 识别 ISO。关闭时保持现有“文件名.strm”命名。", "false"),
		intSpec(KeyStrmMinFileSizeMB, "strm", "小文件过滤", "忽略小于该大小的媒体文件，0 表示不过滤。", "0", "MB", 0, 10240),
		stringSpec(KeyStrmConflictPolicy, "strm", "同名冲突策略", "同目录同名不同后缀时保留哪一个：size_desc / size_asc / name_asc。", "size_desc"),
		intSpec(KeyStrmTaskConcurrency, "strm", "STRM 任务并发", "同时运行的 STRM 扫描任务上限。", "3", "", 1, 10),
		stringSpec(KeyStrmMetadataExtensions, "strm", "元数据扩展名", "任务开启同步元数据时使用的扩展名，英文分号分隔。", "srt;ass;ssa;sub;sup;idx;vtt;nfo;jpg;jpeg;png;webp;bmp;gif"),
		intSpec(KeyStrmMetadataMaxSizeMB, "strm", "元数据大小上限", "同步元数据时忽略超过该大小的文件。", "10", "MB", 1, 1024),
		boolSpec(KeyStrmMetadataParentEnabled, "strm", "父目录元数据同步", "子目录有影片时，也同步父目录下的海报、nfo 等元数据。", "true"),
		boolSpec(KeyStrmTool115TreeEnabled, "strm", "115 网盘 STRM 增强（目录树清单模式）", "开启后 115Open 账号的 STRM 任务改用全量清单 + 增量对账方式执行，减少逐目录递归请求；配了分支的任务维持原逻辑。", "false"),
		selectSpec(KeyStrmMetadataSyncMode, "strm", "元数据同步策略", "local_primary=保留本地并从云端补缺；cloud_primary=本地目录与云端保持一致；bidirectional=本地与云端互相补缺。", "local_primary", []Option{
			{Value: "cloud_primary", Label: "网盘元数据为主"},
			{Value: "local_primary", Label: "本地元数据补缺"},
			{Value: "bidirectional", Label: "本地与云端互补"},
		}),
		stringSpec(KeyStrmScrapeWriteMode, "strm", "STRM 刮削写入策略", "missing_only=仅补缺；overwrite=覆盖已有 nfo/海报。", "missing_only"),
		boolSpec(KeyStrmScrapeEpisodeInfo, "strm", "刮削分集信息", "生成季/集 NFO、季海报和分集预览图。", "true"),
		boolSpec(KeyStrmScrapeFanart, "strm", "刮削详情页背景图", "为电影和剧集生成 fanart.jpg。", "false"),
		boolSpec(KeyStrmScrapeActors, "strm", "刮削演员信息", "将主要演员、角色和头像地址写入作品 NFO。", "false"),
		boolSpec(KeyStrmScrapeClearLogo, "strm", "刮削影片 Logo", "按搜索语言优先生成 clearlogo.png。", "false"),
		{Key: KeyStrmScrapeScopes, Type: TypeString, Default: "{}", Hidden: true},
		// 界面偏好：信息条开合（后台不展示，仅持久化 + 随备份还原）
		{Key: KeyUIBandHiddenStrm, Type: TypeBool, Default: "false", Hidden: true, SilentLog: true},
		{Key: KeyUIBandHiddenCache, Type: TypeBool, Default: "false", Hidden: true, SilentLog: true},
		{Key: KeyUIBandHiddenOrganize, Type: TypeBool, Default: "false", Hidden: true, SilentLog: true},
		{Key: KeyUIBandHiddenFuse, Type: TypeBool, Default: "false", Hidden: true, SilentLog: true},
		boolSpec(KeyMOProxyEnabled, "media_organize", "启用代理", "TMDB 请求经代理出站。", "false"),
		stringSpec(KeyMOProxyURL, "media_organize", "代理地址", "HTTP/HTTPS 代理地址，例如 http://127.0.0.1:7890。", ""),
		stringSpec(KeyMOProxyUsername, "media_organize", "代理用户名", "代理认证用户名，无认证可留空。", ""),
		stringSpec(KeyMOProxyPassword, "media_organize", "代理密码", "代理认证密码。", ""),
		stringSpec(KeyMOTmdbAPIKey, "media_organize", "TMDB API Key", "The Movie Database API 密钥。", ""),
		stringSpec(KeyMOTmdbLanguage, "media_organize", "TMDB 搜索语言", "TMDB 搜索与详情语言，例如 zh-CN。", "zh-CN"),
		stringSpec(KeyMOTmdbAPIHost, "media_organize", "TMDB API 主域名", "自建反代时填写主域名，程序自动补 /3。", "https://api.themoviedb.org"),
		stringSpec(KeyMOTmdbImageHost, "media_organize", "TMDB 图片主域名", "自建反代时填写主域名，程序自动补 /t/p。", "https://image.tmdb.org"),
		stringSpec(KeyMOTmdbProxyURL, "media_organize", "TMDB HTTP 代理", "仅用于 TMDB API 出站请求，形如 http://127.0.0.1:7890 或 socks5://127.0.0.1:1080。留空表示直连。", ""),
		intSpec(KeyMOAPIRequestIntervalMS, "media_organize", "API 额外补偿间隔", "网盘 API 请求之间的额外等待时间。", "300", "毫秒", 50, 10000),
		intSpec(KeyMOTmdbRequestIntervalMS, "media_organize", "TMDB 请求间隔", "两次 TMDB API 请求之间的最小间隔。", "250", "毫秒", 100, 5000),
		stringSpec(KeyMOFileExtensions, "media_organize", "媒体文件扩展名", "参与整理的媒体扩展名，英文分号分隔。", "mkv;mp4;avi;ts;mov;wmv;iso;m2ts;rmvb;flv;m4v;webm"),
		stringSpec(KeyMOMetadataExtensions, "media_organize", "元数据文件扩展名", "随媒体一起整理的元数据扩展名，英文分号分隔。", "nfo;ass;ssa;srt;sub;idx;sup;vtt;jpg;jpeg;png;webp;bmp"),
		stringSpec(KeyMOMediaTagOrder, "media_organize", "媒体信息标签排序", "重命名时媒体标签的排列顺序，JSON 数组字符串。", `["screen_size","video_codec","audio_codec","audio_channels"]`),
		boolSpec(KeyMOAlignMediaTags, "media_organize", "强迫症模式", "同后缀文件保持媒体信息标签一致。", "false"),
		intSpec(KeyMOMaxWorksPerRun, "media_organize", "每次最多整理作品数", "单次执行最多处理的作品数，0 表示不限制。", "50", "", 0, 10000),
		boolSpec(KeyMOOverwriteExisting, "media_organize", "同名冲突时覆盖", "目标位置已有同名文件时覆盖，默认跳过。", "false"),
		{
			Key:     KeyAIOrganizeEnabled,
			Type:    TypeBool,
			Default: "false",
			Hidden:  true,
		},
		{
			Key:       KeyAIOrganizeInstances,
			Type:      TypeString,
			Default:   "[]",
			Sensitive: true,
			Hidden:    true,
		},
		{
			Key:     KeyAIOrganizeBaseURL,
			Type:    TypeString,
			Default: "https://api.deepseek.com",
			Hidden:  true,
		},
		{
			Key:       KeyAIOrganizeAPIKey,
			Type:      TypeString,
			Default:   "",
			Sensitive: true,
			Hidden:    true,
		},
		{
			Key:     KeyAIOrganizeModel,
			Type:    TypeString,
			Default: "deepseek-chat",
			Hidden:  true,
		},
		{
			Key:     KeyMOClassificationEnabled,
			Type:    TypeBool,
			Default: "false",
			Hidden:  true,
		},
		{
			Key:     KeyMOClassificationConfig,
			Type:    TypeString,
			Default: "",
			Hidden:  true,
		},
		{
			Key:         KeyOAuthServerURL,
			Type:        TypeString,
			Category:    "system",
			Label:       "OAuth 代理服务地址",
			Description: "添加账号时「自动获取 Token」经此服务转发。留空或无效地址将回落默认值。本地调试可填 http://127.0.0.1:8000。",
			Default:     domain.DefaultOAuthServerURL,
			normalize:   domain.NormalizeOAuthServerURL,
		},
		{
			Key:     KeyLogErrorAckAt,
			Type:    TypeString,
			Default: "",
			Hidden:  true,
		},
		{
			Key:     KeyAnnouncementReadVersion,
			Type:    TypeString,
			Default: "",
			Hidden:  true,
		},
		{
			Key:     KeyLocalUploadEnabled,
			Type:    TypeBool,
			Default: "false",
			Hidden:  true,
		},
		{
			Key:     KeyLocalUploadMappings,
			Type:    TypeString,
			Default: "[]",
			Hidden:  true,
		},
		{
			Key:     KeyCoverExtractEnabled,
			Type:    TypeBool,
			Default: "false",
			Hidden:  true,
		},
		{
			// 海报默认样式（JSON）：shape/height/panel_color/opacity/text_color/packaged
			Key:     KeyCoverExtractStyle,
			Type:    TypeString,
			Default: "",
			Hidden:  true,
		},
		{
			Key:     KeyQuarkTVEnabled,
			Type:    TypeBool,
			Default: "false",
			Hidden:  true,
		},
		{
			Key:     KeyQuarkTVPlayMode,
			Type:    TypeString,
			Default: "adaptive",
			Hidden:  true,
		},
		{
			Key:     KeyQuarkTVClientListMode,
			Type:    TypeString,
			Default: "proxy_list",
			Hidden:  true,
		},
		{
			Key:     KeyQuarkTVProxyClients,
			Type:    TypeString,
			Default: "vidhub",
			Hidden:  true,
		},
		// MCP：全部 Hidden —— 本组设置由专用的 MCP 设置页管理，
		// 不适合混在通用设置表单里（工具禁用清单是列表、助理提示词是长文本）。
		{Key: KeyMcpEnabled, Type: TypeBool, Default: "false", Hidden: true},
		{Key: KeyMcpAllowWriteTools, Type: TypeBool, Default: "false", Hidden: true},
		{Key: KeyMcpDisabledTools, Type: TypeString, Default: "[]", Hidden: true, SilentLog: true},
		{Key: KeyMcpAssistantEnabled, Type: TypeBool, Default: "true", Hidden: true},
		{Key: KeyMcpAssistantBaseURL, Type: TypeString, Default: "", Hidden: true},
		{Key: KeyMcpAssistantAPIKey, Type: TypeString, Default: "", Hidden: true, Sensitive: true},
		{Key: KeyMcpAssistantModel, Type: TypeString, Default: "", Hidden: true},
		{Key: KeyMcpAssistantPrompt, Type: TypeString, Default: "", Hidden: true, SilentLog: true},
		intSpec(KeyMcpMaxToolRounds, "system", "MCP 工具调用轮数上限", "", "6", "轮", 1, 20),
		intSpec(KeyMcpTimeout, "system", "MCP 助理超时", "", "120", "秒", 10, 600),
		intSpec(KeyDiscoverChannelCatchupHours, "discover", "TG 频道停机追赶窗口",
			"服务停机或频道长期拉取失败后重启，超过该时长就不再深翻积压历史，直接从频道最新一页开始处理，避免一次性补转存打爆网盘；被跳过的积压会冻结保存并在之后每轮回补一小段，直到追上。填 0 表示不限（停机多久都从头补）。",
			"12", "小时", 0, 720),

		// ---- 字幕智能处理 ----
		boolSpec(KeySubtitleEnabled, "subtitle", "启用字幕智能处理",
			"总开关。关闭后整理流程不会为视频自动检索字幕，接口仍可用于手动搜索与时间轴校正。", "false"),
		boolSpec(KeySubtitleAssrtEnabled, "subtitle", "启用射手网(assrt)",
			"需要在下方填入 API Token；免费额度有频率限制，触发限流会直接报错而不重试。", "false"),
		stringSpec(KeySubtitleAssrtAPIKey, "subtitle", "射手网 API Token",
			"射手网开放接口的访问令牌，未填写时该来源不可用。", ""),
		boolSpec(KeySubtitleOpenSubtitlesEnabled, "subtitle", "启用 OpenSubtitles",
			"需要 API Key；官方接口强制要求声明用途的 User-Agent，未登录时下载配额极低。", "false"),
		stringSpec(KeySubtitleOpenSubtitlesAPIKey, "subtitle", "OpenSubtitles API Key",
			"OpenSubtitles v1 REST 接口的订阅密钥。", ""),
		stringSpec(KeySubtitleOpenSubtitlesUserAgent, "subtitle", "OpenSubtitles User-Agent",
			"站点要求调用方标识自己，缺失会直接返回 403。", "litepan v1.0"),
		stringSpec(KeySubtitleOpenSubtitlesUsername, "subtitle", "OpenSubtitles 账号",
			"可选。填写账号密码可登录换取更高下载配额。", ""),
		stringSpec(KeySubtitleOpenSubtitlesPassword, "subtitle", "OpenSubtitles 密码",
			"可选。仅用于登录换取下载令牌，不会长期驻留内存。", ""),
		boolSpec(KeySubtitleSubhdEnabled, "subtitle", "启用 SubHD",
			"站点在 Cloudflare 之后，通常需要填 Cookie 才能检索；本模块不做挑战页绕过。", "false"),
		stringSpec(KeySubtitleSubhdCookie, "subtitle", "SubHD Cookie",
			"浏览器通过验证后复制站点的 Cookie 填入，可显著降低被反爬拦截的概率。", ""),
		boolSpec(KeySubtitleZimukuEnabled, "subtitle", "启用字幕库(zimuku)",
			"站点对高频访问有限流，被要求人机校验时需填入 Cookie；该来源打包为 zip，会自动解包挑选字幕。", "false"),
		stringSpec(KeySubtitleZimukuCookie, "subtitle", "字幕库 Cookie",
			"可选。被限流或要求校验时填入站点 Cookie。", ""),
		stringSpec(KeySubtitleLanguagePriority, "subtitle", "语言优先级",
			"逗号分隔，靠前的语言得分更高。支持 zh-cn、zh-tw、zh、en、ja、ko 等写法。", "zh-cn,zh-tw,zh,en"),
		stringSpec(KeySubtitleFormatPriority, "subtitle", "格式优先级",
			"逗号分隔，可填 srt、ass、ssa、sub、sup、vtt。ass/srt 支持时间轴校正，sup 为图形字幕无法校正。", "ass,srt,ssa,sub,sup"),
		boolSpec(KeySubtitleAutoMatch, "subtitle", "整理时自动匹配字幕",
			"媒体整理流程中为缺失字幕的视频自动检索并打分。已有同名字幕的视频会直接跳过。", "false"),
		boolSpec(KeySubtitleAutoDownload, "subtitle", "自动下载匹配到的字幕",
			"关闭时只记录匹配结果不下载，便于先观察匹配质量。", "true"),
		intSpec(KeySubtitleMinMatchScore, "subtitle", "最低匹配分数",
			"低于该分数的候选会被跳过而不下载。分数由发布组、标题、语言、年份、季集、格式加权得出，满分 100。", "60", "分", 0, 100),
		boolSpec(KeySubtitleAutoSync, "subtitle", "下载后自动校正时间轴",
			"基于语音活动检测对齐字幕时间轴，需要系统已安装 ffmpeg 与 ffprobe。", "false"),
		stringSpec(KeySubtitleSyncMinConfidence, "subtitle", "时间轴校正最低置信度",
			"0 到 1 之间。低于该置信度时保留原字幕不改写。", "0.7"),
		selectSpec(KeySubtitleSyncMode, "subtitle", "时间轴校正方式",
			"vad 用语音活动互相关对齐（推荐）；offset 只算固定偏移；scale 按视频/字幕时长比例拉伸后再算偏移；auto 由服务层择一。",
			"vad", []Option{
				{Value: "vad", Label: "语音活动检测（推荐）"},
				{Value: "offset", Label: "仅固定偏移"},
				{Value: "scale", Label: "时长比例 + 偏移"},
				{Value: "auto", Label: "自动"},
			}),
		boolSpec(KeySubtitleSyncDryRun, "subtitle", "只试算不写入",
			"开启后校正只计算偏移与置信度并返回，不修改字幕文件，适合先在库里验证效果。", "true"),
		selectSpec(KeySubtitleTargetDirPolicy, "subtitle", "字幕存放位置",
			"same 与视频同目录同名（播放器可自动挂载）；subtitle 放到视频目录下的 subtitle 子目录。",
			"same", []Option{
				{Value: "same", Label: "与视频同目录"},
				{Value: "subtitle", Label: "subtitle 子目录"},
			}),
		boolSpec(KeySubtitleKeepOriginal, "subtitle", "校正前备份原字幕",
			"写入前把原字幕另存为 .bak，便于回退。", "true"),
		boolSpec(KeySubtitleOverwrite, "subtitle", "覆盖已存在的字幕",
			"关闭时目标路径已存在字幕会跳过下载，不做覆盖。", "false"),
		intSpec(KeySubtitleConcurrency, "subtitle", "批量处理并发数",
			"批量整理字幕时的并发上限，实际会被收敛到 8 以内——上游字幕站对并发敏感。", "2", "个", 0, 16),
		intSpec(KeySubtitleTimeoutSeconds, "subtitle", "字幕源请求超时",
			"单个字幕源的检索与下载超时；批量与校正的接口超时会按该值放宽倍数。", "30", "秒", 0, 600),
		stringSpec(KeySubtitleProxy, "subtitle", "字幕源代理地址",
			"可选。形如 http://127.0.0.1:7890，仅用于访问字幕站点。留空表示直连。", ""),
		stringSpec(KeySubtitleUserAgent, "subtitle", "字幕源 User-Agent",
			"留空使用内置的常见浏览器 UA——字幕站点对空 UA 或 Go 默认 UA 敏感。", ""),
	}
}

// categories 返回有序分组定义；只保留当前实际用到的分组。
func categories() []Category {
	return []Category{
		{ID: "system", Label: "系统设置"},
		{ID: "account_display", Label: "网盘账号显示"},
		{ID: "performance", Label: "性能设置"},
		{ID: "strm", Label: "STRM 设置"},
		{ID: "media_organize", Label: "媒体整理设置"},
		{ID: "emby", Label: "Emby 设置"},
		{ID: "discover", Label: "影视发现设置"},
		{ID: "subtitle", Label: "字幕智能处理"},
	}
}
