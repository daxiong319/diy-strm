package settings

import (
	"strconv"
	"strings"

	"litepan/internal/domain"
)

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
	// KeyMOClassificationSecondaryEnabled 二级维度目录开关（华语电影这类）。
	// 关掉它会连带关掉三级目录与系列目录 —— 它们的父目录不存在，
	// 单独留着只会让用户配了一个永远不生效的层级。
	KeyMOClassificationSecondaryEnabled = "mo_classification_secondary_enabled"
	// KeyMOClassificationTertiaryEnabled 三级目录开关（2000-2009 这类年份段）。
	// 可以单独关，不影响二级与系列。
	KeyMOClassificationTertiaryEnabled = "mo_classification_tertiary_enabled"
	// KeyMOClassificationSeriesEnabled 系列目录开关（流浪地球系列这类）。
	// 单独关：用户可能想要年份分段但不想要系列分组。
	KeyMOClassificationSeriesEnabled = "mo_classification_series_enabled"
	// KeyMOClassificationPrimaryEnabled 一级分类目录开关（T27 C-3）。
	//
	// 以前一级是写死的「电影/电视剧」两条且不可关闭，所以没有它的开关。
	// 一级表化之后一级变成可配置的一层，别的层级都有对应的关闭办法而它没有，
	// 用户想退回扁平结构时只能整体停用分类整理 —— 那等于把新功能也一起扔了。
	KeyMOClassificationPrimaryEnabled = "mo_classification_primary_enabled"
	// KeyDiscoverChannelCatchupHours TG 频道订阅停机追赶限制（小时）。
	// 0 = 不限：停机多久都从旧游标一路回补（原行为）。
	KeyDiscoverChannelCatchupHours = "discover_channel_catchup_hours"
	// 订阅转存前的身份校验（参考实现 不变式：未通过身份校验，一律不转存）。
	KeyMOSubscriptionIdentityEnabled = "mo_subscription_identity_enabled"
	// KeyMOSubscriptionIdentityPurityRatio 纯度阈值（0~1，小数）。
	// ⚠️ 默认 0.8 是**推测值**，不是从 参考实现 逆向确认的数值：参考实现 的阈值只存在于
	// 编译后的 .so 里，读不到字面量。上线后应按实际误转存率校准。
	KeyMOSubscriptionIdentityPurityRatio = "mo_subscription_identity_purity_ratio"
	// KeyMOSubscriptionIdentityAllowUnavailable 保留的排查开关，默认关闭。
	// 打开后「拿不到任何证据」的候选也会放行，会破坏不变式，界面必须标红警告。
	KeyMOSubscriptionIdentityAllowUnavailable = "mo_subscription_identity_allow_unavailable"
	// KeyMOSubscriptionTransferProtectHours 转存保护期（小时，0~24）。
	// 刚转存完网盘写入有延迟、文件清单可能还没刷新，这段时间内不再动同一批内容；
	// 同时它也是个限流器：媒体库索引不可用时，最多也只每 N 小时重转一次。
	// 调成 0 = 立刻可重转（排查用，会导致反复转存）。
	KeyMOSubscriptionTransferProtectHours = "mo_subscription_transfer_protect_hours"

	// ---- T14 刮削落盘 / 未识别兜底 / 备份目标 ----

	// KeyMOScrapeNFOEnabled 整理完成后自动生成 NFO + 海报。
	KeyMOScrapeNFOEnabled = "mo_scrape_nfo_enabled"
	// KeyMOScrapeNFOTarget 刮削落盘目标：local 写本地 STRM 目录，cloud 上传网盘。
	KeyMOScrapeNFOTarget = "mo_scrape_nfo_target"
	// KeyMOScrapeUnrecognizedDir 未识别文件的兜底目录（三种填法见
	// mediaorganize.ResolveUnrecognizedTarget：绝对路径 / 纯目录名 / 留空）。
	KeyMOScrapeUnrecognizedDir = "mo_scrape_unrecognized_dir"
	// KeyMOScrapeFollowExistingLocation 从 Emby 反查该作品已有的库内位置。
	KeyMOScrapeFollowExistingLocation = "mo_scrape_follow_existing_location"
	// KeyMOScrapeSkipAction 未识别文件的动作：keep 留在源目录，move 移到兜底目录。
	KeyMOScrapeSkipAction = "mo_scrape_skip_action"
	// KeyMOBackupTarget 备份恢复目标：local 恢复到本地，cloud 恢复到网盘。
	KeyMOBackupTarget = "mo_backup_target"

	// ---- T15 目录监控风控三件套 ----
	// 阈值语义见 internal/guardrail/guardrail.go 顶部的「哪些数字是文档确认的」注释。
	// 连续调用接口次数超限就暂停，0 表示不限。
	KeyMOScrapeMaxCallsPerWindow = "mo_scrape_max_calls_per_window"
	// 连续调用次数的统计窗口（秒）。
	KeyMOScrapeCallWindowSeconds = "mo_scrape_call_window_seconds"
	// 触顶后的暂停时长上限（秒）。文档确认的上限是 86400（24 小时）。
	KeyMOScrapeCallPauseSeconds = "mo_scrape_call_pause_seconds"
	// 连续整理时长超限就暂停，0 表示不限（分钟）。
	KeyMOScrapeMaxWorkMinutes = "mo_scrape_max_work_minutes"
	// 整理时长触顶后的暂停时长（分钟）。文档确认的上限是 1440（24 小时）。
	KeyMOScrapeWorkPauseMinutes = "mo_scrape_work_pause_minutes"
	// 媒体文件最小体积（字节）。0 = 不启用；启用后 move 模式下小于它的文件会被
	// 移到隔离目录而不是真删（见 guardrail 的说明）。
	KeyMOMinMediaSizeBytes = "mo_min_media_size_bytes"
	// KeyMOQuarantineDir 小文件隔离目录（网盘路径）。小文件被移到这里，可恢复。
	KeyMOQuarantineDir = "mo_quarantine_dir"
	// KeyMOSmallFileAcked 小文件隔离的二次确认标记。
	// 默认 false：不勾选就只报告不动手，避免「打开阈值 = 静默搬走文件」。
	KeyMOSmallFileAcked = "mo_small_file_acked"

	// ---- T05 搜索连接器 ----

	// KeyMOSubscriptionSearchSources 订阅默认使用的搜索源 key 列表（逗号分隔）。
	// 默认只有 tgto123：这是接入连接器之前 litepan 唯一那个源，保证存量订阅行为不变。
	KeyMOSubscriptionSearchSources = "mo_subscription_search_sources"
	// KeyMOSubscriptionSearchSourcesDefault 上面这个 key 的默认值。
	// 单独提成常量，是为了让「迁移 0036 的列默认值」和「运行时回落值」引用同一处，
	// 避免两边各写一个字符串、改一个忘一个。
	KeyMOSubscriptionSearchSourcesDefault = "tgto123"
	// KeyMOConnectorTimeoutSeconds 单个搜索源的超时（秒，1~300，默认 30）。
	KeyMOConnectorTimeoutSeconds = "mo_connector_timeout_seconds"
	// KeyMOConnectorSearchBudgetSeconds 一条订阅检索所有源的总预算（秒，1~1800，默认 45）。
	// 串行检索下每源各等一次超时，总时长会线性膨胀，这个上限是总的闸门。
	KeyMOConnectorSearchBudgetSeconds = "mo_connector_search_budget_seconds"
	// ---- 订阅执行护栏（T06）----
	// KeyMOSubscriptionExecutionMode 执行强度四档（默认均衡）。
	// 保守=1 次/45~65s，均衡=2 次/25~40s，激进=3 次/10~18s，自定义见下面三项。
	// 这四个数字是 参考实现 官网 docs 逐字给定的，不是推测值。
	KeyMOSubscriptionExecutionMode = "mo_subscription_execution_mode"
	// KeyMOSubscriptionCustomAttempts 自定义档每轮最多转存几条（1~10，默认 2）。
	KeyMOSubscriptionCustomAttempts = "mo_subscription_custom_attempts"
	// KeyMOSubscriptionCustomIntervalSec 自定义档两次转存之间的间隔基准（秒，默认 30）。
	KeyMOSubscriptionCustomIntervalSec = "mo_subscription_custom_interval_sec"
	// KeyMOSubscriptionCustomJitterSec 自定义档在间隔基准上叠加的随机抖动上限（秒，默认 10）。
	KeyMOSubscriptionCustomJitterSec = "mo_subscription_custom_jitter_sec"
	// KeyMOSubscriptionTimeWindows 允许检索的时段，如 "00:00-08:00,23:00-06:00"，空=不限。
	KeyMOSubscriptionTimeWindows = "mo_subscription_time_windows"
	// KeyMOSubscriptionFinishedGraceDays 本季转存齐后的宽限天数（默认 7），0=立即停。
	KeyMOSubscriptionFinishedGraceDays = "mo_subscription_finished_grace_days"
	// KeyMOMediaParseResAliases 分辨率别名表，如 "超高清=2160p,蓝光原盘=2160p"。
	KeyMOMediaParseResAliases = "mo_media_parse_res_aliases"
	// KeyMOMediaParseHDRTexts / DVTexts / SDRTexts 特效识别文本，可追加站点自定义写法。
	KeyMOMediaParseHDRTexts = "mo_media_parse_hdr_texts"
	KeyMOMediaParseDVTexts  = "mo_media_parse_dv_texts"
	KeyMOMediaParseSDRTexts = "mo_media_parse_sdr_texts"
	// ---- 洗版（T07）----
	// KeyMOMediaUpgradeEnabled 洗版总开关。
	//
	// ⚠️ 默认 false，且不打算默认打开。洗版会删用户文件：
	// 一条「新版更好」的判定结论一旦配上 loser_action=delete，就等于删除磁盘上的文件。
	// 升个版本不该顺带获得删用户文件的权力，所以必须显式开启。
	KeyMOMediaUpgradeEnabled = "mo_media_upgrade_enabled"
	// ---- RBAC（T08）----
	// KeyMORBACEnabled 权限系统总开关。
	//
	// 默认 false：**开启前后行为必须完全一致**。
	// 关着的时候不查用户表、不算权限、不裁导航，
	// 管理员凭 admin_username/admin_password 进后台，和这个功能上线前一模一样。
	KeyMORBACEnabled = "mo_rbac_enabled"
	// KeyMORBACDefaultUserGroup 新建用户时自动加入的默认组名，留空表示不自动入组。
	//
	// 指向一个不存在的组名不报错：用户会落在一个「无权限」的状态，
	// 界面上把该用户名显示出来让人去建组，比启动时报错把后台锁死强。
	KeyMORBACDefaultUserGroup = "mo_rbac_default_user_group"
	// ---- 求片中心（T09）----
	// KeyMOMediaRequestEnabled 求片中心总开关。
	//
	// 默认 false：关着时求片端口根本不监听（不是「监听但返回 403」），
	// 扫描这个端口的人连「这里有个东西」都探测不到。
	// 之所以不做成「默认开着、登录后可用」，是因为它需要先有 RBAC 用户
	// （没有用户的求片站等于一个无口令的公开搜索代理）。
	KeyMOMediaRequestEnabled = "mo_media_request_enabled"
	// KeyMOMediaRequestPort 求片站独立端口。
	//
	// 默认 7812，与 参考实现 对齐 —— 换端口会让「照着 参考实现 文档配 Docker 映射」
	// 的用户直接踩空，而 7812 与 litepan 管理台默认端口 5211 也不冲突。
	// 管理台端口本身可配（LISTEN_ADDR），所以真撞上了由 supervisor 报错并放弃起这个口，
	// 而不是把整个后台拖死。
	KeyMOMediaRequestPort = "mo_media_request_port"
	// KeyMOMediaRequestRequireReview 是否需要管理员过审。
	//
	// 默认 true。关掉之后提交即建订阅，等于每个拿到求片站账号的家人
	// 都能直接往订阅表里写东西 —— 这是「家里没人管」时想要的，
	// 但默认值必须偏向多一道复核。
	KeyMOMediaRequestRequireReview = "mo_media_request_require_review"
	// KeyMOMediaRequestDailyLimit 每人每天可提交的求片条数，0=不限。
	//
	// 默认 5，对齐 参考实现 的 daily_new_request_limit。
	// 存在的理由不是省配额，是防止一个人（或一个脚本）把订阅表刷爆。
	KeyMOMediaRequestDailyLimit = "mo_media_request_daily_limit"
	// KeyMOMediaRequestTagMaxPerUser 每人一部作品最多留多少个入库标签，0=不限。
	//
	// 默认 20，对齐 参考实现。标签是写进入库文件名的，超了会让文件名长得没法看。
	KeyMOMediaRequestTagMaxPerUser = "mo_media_request_tag_max_per_user"
	// KeyMOMediaRequestTagMaxLength 单个入库标签最长多少字，0=不限。
	//
	// 默认 100，对齐 参考实现。
	KeyMOMediaRequestTagMaxLength = "mo_media_request_tag_max_length"
	// ---- 免登录分享页（T10）----
	// KeyMOLibraryShareEnabled 媒体库分享页总开关。
	//
	// 默认 false，且关闭时**整棵访客路由树都不注册**（不是「注册了但返回 403」）：
	// 这类页面一旦挂上去就会被人到处转发，关着的时候连路由都不该存在。
	// 分享本身也是「把媒体库里的东西公开给外部人」，默认值必须偏向关。
	KeyMOLibraryShareEnabled = "mo_library_share_enabled"
	// KeyMOLibraryShareDefaultExpireDays 新建分享的默认有效期（天），0=永久。
	//
	// 默认 7，对齐 参考实现 的分享有效期档位（1|3|7|30|0）。
	// 新建时仍可以逐条选，默认值只决定「不选时按哪个算」。
	KeyMOLibraryShareDefaultExpireDays = "mo_library_share_default_expire_days"
	// KeyMOLibraryShareDefaultMaxDevices 新建分享的默认同时在线设备数上限。
	//
	// 默认 5。设备数按「最近 24 小时内出现过该分享的访客」去重计数，
	// 所以「关掉页面走人」的设备会自己让出名额，不用手动清理。
	KeyMOLibraryShareDefaultMaxDevices = "mo_library_share_default_max_devices"
	// KeyMOLibraryShareDefaultPassword 新建分享时的默认访问口令，留空表示默认不带口令。
	//
	// 留空是默认值，理由是「不给口令」和「给口令」一样安全 ——
	// 真正决定安全边界的是那条只有访客会话能拿到的令牌，
	// 口令只是多挡一层「链接转发到群里」的情况。
	// 想强制所有分享都带口令，可以在这个全局默认里填一个值。
	KeyMOLibraryShareDefaultPassword = "mo_library_share_default_password"
	// KeyMOPlayMonitorEnabled 播放监控总开关（默认关）。
	KeyMOPlayMonitorEnabled = "mo_play_monitor_enabled"
	// KeyMOPlayMonitorIdleSeconds 播放监控空闲判定（秒，默认 60）：
	// 多久没有后续取流请求就认为停播、从实时列表消失。
	KeyMOPlayMonitorIdleSeconds = "mo_play_monitor_idle_seconds"
	// KeyMOPlayMonitorSampleSeconds 流量累计间隔（秒，默认 5）。
	KeyMOPlayMonitorSampleSeconds = "mo_play_monitor_sample_seconds"
	// KeyMOPlayReportEnabled 观影报告总开关（默认关）。
	KeyMOPlayReportEnabled = "mo_play_report_enabled"
	// KeyMOPlayReportMinSeconds 忽略播放时长低于 N 秒的记录（默认 0，不过滤）。
	// 判定是「低于」——**等于阈值仍然保留**。
	KeyMOPlayReportMinSeconds = "mo_play_report_min_seconds"
	// KeyMOPlayReportGapMinutes 中断超过 N 分钟算新的一次播放（默认 30）。
	KeyMOPlayReportGapMinutes = "mo_play_report_gap_minutes"
	// KeyMORSSEnabled RSS 订阅源总开关（默认关）。
	//
	// 关着时**不启动轮询 worker**：这不是「同步会失败」的功能开关，
	// 而是「这条链路要不要占用网络与 115 离线下载额度」的开关。
	// 默认关的理由是 RSS 源的命中与否完全取决于用户自己贴的 feed，
	// 没人贴源的时候白跑轮询只是浪费。
	KeyMORSSEnabled = "mo_rss_enabled"
	// KeyMORSSPollIntervalMinutes 轮询间隔（分钟，默认 30）。
	//
	// 这是「多久去看一次有哪些新条目」，不是「停机多久之后要放弃追赶」。
	// 追赶窗口是另一个键（KeyMORSSCatchupGapHours）。
	KeyMORSSPollIntervalMinutes = "mo_rss_poll_interval_minutes"
	// KeyMORSSCatchupGapHours 停机超过 N 小时后放弃逐条补齐、改为「只取最新一页」。
	//
	// 默认 12，与 TG 频道位点的追赶窗口同值（internal/discover/dmodels/scrape.go
	// 的 DefaultChannelCatchupHours），但**这是 RSS 自己的窗口**：参考实现 侧 RSS 没有位点，
	// 也没有这个阈值，本仓是新增行为。
	//
	// 为什么要有：停机 3 天后启动，逐条补齐意味着把三天里的全部更新一次性塞进
	// 115 离线下载队列 —— 用户要的是「现在追到哪了」，不是「补三天前就该错过的片」。
	KeyMORSSCatchupGapHours = "mo_rss_catchup_gap_hours"
	// KeyMORSSStaleAfterMinutes 条目发布时间早于「now - N 分钟」就直接跳过。
	//
	// 默认 60。就算轮询间隔调小或某次同步跑了很久，也不该把明显过期的条目
	// 提交进离线下载。
	KeyMORSSStaleAfterMinutes = "mo_rss_stale_after_minutes"
	// KeyMORSSHTTPTimeoutSeconds 单个 feed 拉取超时（秒，默认 20）。
	//
	// 独立成一个键的理由：这是唯一一处「一个坏源能拖住整轮同步」的地方，
	// 用户遇到某个站打不开时需要能单独调短它。
	KeyMORSSHTTPTimeoutSeconds = "mo_rss_http_timeout_seconds"
	// KeyMORSSMaxFeedBytes 单个 feed 响应体上限（字节，默认 4194304=4MiB）。
	//
	// 超限**直接报错不静默截断**：截断出来的半个 XML 要么解析失败，
	// 要么悄悄丢掉尾部条目，而这两种情况用户都无从察觉。
	KeyMORSSMaxFeedBytes = "mo_rss_max_feed_bytes"
	// KeyMOMediaUpgradeSource 扫描源：local / emby / jellyfin。
	// Plex 不支持洗版（参考实现 侧即如此），本仓也没有 Plex 索引，故无此选项。
	KeyMOMediaUpgradeSource = "mo_media_upgrade_source"
	// KeyMOMediaUpgradeLibraryRoot 媒体库根目录（被比较的「现版」所在处）。
	KeyMOMediaUpgradeLibraryRoot = "mo_media_upgrade_library_root"
	// KeyMOMediaUpgradeCandidateRoots 候选目录（新版出现的地方），逗号分隔。
	KeyMOMediaUpgradeCandidateRoots = "mo_media_upgrade_candidate_roots"
	// KeyMOMediaUpgradeMaxRecordsPerSeries 单部剧单次扫描最多产出多少条可执行记录，0=不限。
	KeyMOMediaUpgradeMaxRecordsPerSeries = "mo_media_upgrade_max_records_per_series"
	// KeyMOMediaUpgradeLoserAction 败方动作：keep / delete / move。
	KeyMOMediaUpgradeLoserAction = "mo_media_upgrade_loser_action"
	// KeyMOMediaUpgradeMoveDir 败方动作为 move 时的目标目录。
	KeyMOMediaUpgradeMoveDir = "mo_media_upgrade_move_dir"
	// KeyMOMediaUpgradeGroupPriority 制作组优先级，逗号分隔，越靠前越优先。
	KeyMOMediaUpgradeGroupPriority = "mo_media_upgrade_group_priority"
	// KeyMOMediaUpgradeMinResolution 最低分辨率门槛，低于它的候选不参与洗版。
	KeyMOMediaUpgradeMinResolution = "mo_media_upgrade_min_resolution"
	// KeyMOMediaUpgradeMinChannels 最低声道门槛，低于它的候选不参与洗版。
	KeyMOMediaUpgradeMinChannels = "mo_media_upgrade_min_channels"
	// KeyMOMediaUpgradeRequireSubtitle 是否只认带字幕标记的候选。
	KeyMOMediaUpgradeRequireSubtitle = "mo_media_upgrade_require_subtitle"
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

// normalizePurityRatio 校验纯度阈值的输入：必须是 0~1 之间的小数，否则回落到
// 默认值。设置项是 TypeString（registry 目前只有 string/int/bool/select 四种类型，
// 没有浮点），所以合法性在这里兜住，读的时候再解析一次。
func normalizePurityRatio(raw string) string {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || v <= 0 || v > 1 {
		return "0.8"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
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
		// ---- T14 ----
		boolSpec(KeyMOScrapeNFOEnabled, "media_organize", "刮削落盘",
			"整理完成后自动生成 NFO 与海报。格式取社区通用写法（Emby/Jellyfin 都认），不是逆向出来的专有 schema。", "false"),
		selectSpec(KeyMOScrapeNFOTarget, "media_organize", "刮削落盘目标", "NFO 与海报写到哪里。", "local", []Option{
			{Value: "local", Label: "本地目录（整理目录旁的同名文件夹）"},
			{Value: "cloud", Label: "网盘（与媒体文件同目录）"},
		}),
		stringSpec(KeyMOScrapeUnrecognizedDir, "media_organize", "未识别兜底目录",
			"识别不出标题的文件往哪放。三种填法：以 / 开头的绝对路径直接落该目录；纯目录名落在整理目录下的一级分类里；留空则留在源目录只记一条识别失败。", "未识别"),
		boolSpec(KeyMOScrapeFollowExistingLocation, "media_organize", "未识别时沿用已有位置",
			"开启后先向 Emby 反查这个作品已经在库里的位置，查到就落那里。只有 Emby 支持且需要 Emby 在线；查不到则按「未识别兜底目录」处理。", "false"),
		selectSpec(KeyMOScrapeSkipAction, "media_organize", "未识别文件动作", "识别失败的文件留在源目录还是移到兜底目录。", "keep", []Option{
			{Value: "keep", Label: "留在源目录（只记一条识别失败）"},
			{Value: "move", Label: "移到未识别兜底目录"},
		}),
		selectSpec(KeyMOBackupTarget, "media_organize", "备份恢复目标", "备份里的配置与 STRM 目录恢复到本地还是网盘。", "local", []Option{
			{Value: "local", Label: "本地目录"},
			{Value: "cloud", Label: "网盘"},
		}),
		intSpec(KeyMOScrapeMaxCallsPerWindow, "media_organize", "风控·连续调用上限",
			"一段时间内累计的网盘接口调用次数超过这个数就暂停，0 表示不限。建议设成你所用网盘单日安全调用量的一小部分——风控的目的是别把账号用废。",
			"次", "0", 0, 1000000),
		intSpec(KeyMOScrapeCallWindowSeconds, "media_organize", "风控·调用统计窗口",
			"「连续调用次数」在多长的窗口内累计。窗口太短会把正常的批量扫描误判成连续调用。",
			"秒", "3600", 60, 86400),
		intSpec(KeyMOScrapeCallPauseSeconds, "media_organize", "风控·调用暂停时长",
			"调用次数触顶后暂停多久。最长 86400 秒（24 小时），填更大的值会被截到这个上限。",
			"秒", "3600", 60, 86400),
		intSpec(KeyMOScrapeMaxWorkMinutes, "media_organize", "风控·连续整理时长上限",
			"一轮连续整理超过这个时长就暂停，0 表示不限。",
			"分钟", "0", 0, 10080),
		intSpec(KeyMOScrapeWorkPauseMinutes, "media_organize", "风控·整理暂停时长",
			"整理时长触顶后暂停多久。最长 1440 分钟（24 小时），填更大的值会被截到这个上限。",
			"分钟", "60", 1, 1440),
		intSpec(KeyMOMinMediaSizeBytes, "media_organize", "媒体文件最小体积",
			"move 模式下小于这个体积的文件会被移到隔离目录而不是真删——孤儿目录清理要求目录里没有杂物，几百 KB 的 .nfo 或 .txt 会让目录永远不被判定为空。0 表示不启用。",
			"字节", "0", 0, 1073741824),
		stringSpec(KeyMOQuarantineDir, "media_organize", "小文件隔离目录",
			"低于「媒体文件最小体积」的文件被移到这里而不是删除。留空则用整理根目录下的「_隔离」子目录。文件随时可以搬回去。", ""),
		boolSpec(KeyMOSmallFileAcked, "media_organize", "已确认小文件会被移走",
			"打开「媒体文件最小体积」前需要先勾这一项表示知情。取消勾选即刻停止隔离，已有文件不受影响。", "false"),
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
		// 三个层级开关刻意可见（而不是跟着 KeyMOClassificationEnabled 一起 Hidden）：
		// 用户升级后分类目录会多出「年份段」「系列目录」两层，如果不知道怎么关，
		// 只能整体停用分类整理，那等于把新功能也一起扔了。
		boolSpec(KeyMOClassificationSecondaryEnabled, "media_organize",
			"二级维度目录",
			"按地区/类型等维度再分一层（华语电影、科幻奇幻）。关闭后三级目录与系列目录会一起失效 —— 它们的父目录就是二级目录。",
			"true"),
		boolSpec(KeyMOClassificationTertiaryEnabled, "media_organize",
			"三级目录（年份段）",
			"在二级目录下再按年份分段（2000-2009）。只在这一层按年份切。",
			"true"),
		boolSpec(KeyMOClassificationSeriesEnabled, "media_organize",
			"系列目录",
			"把同一个系列的作品收进同一个目录（流浪地球系列）。目录名由系列规则决定。",
			"true"),
		boolSpec(KeyMOClassificationPrimaryEnabled, "media_organize",
			"一级分类目录",
			"电影 / 电视剧 这一层。关掉它所有影片会直接落到分类根目录，地区、类型、年份段、系列目录都跟着失效。",
			"true"),
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
		boolSpec(KeyMOSubscriptionIdentityEnabled, "discover", "订阅转存前身份校验",
			"总开关。开启后，订阅搜到的候选必须先通过身份校验（标题/类型/年份/季集/纯度）才会发起转存；"+
				"拿不到任何可验证证据的候选（例如只有磁力或 ed2k 链接）一律不转存。这是 参考实现 的不变式，「未通过身份校验，一律不转存」。",
			"true"),
		{
			Key:         KeyMOSubscriptionIdentityPurityRatio,
			Type:        TypeString,
			Category:    "discover",
			Label:       "订阅身份校验纯度阈值",
			Description: "分享里属于主标题的文件占比低于该值时，判为混拼并跳过转存。取值 0~1，例如 0.8 表示 80%。⚠️ 默认值 0.8 是推测值（参考实现 的阈值编译在 .so 里读不到字面量），上线后请按实际误转存率校准。调高更安全、调低更激进。",
			Default:     "0.8",
			normalize:   normalizePurityRatio,
		},
		boolSpec(KeyMOSubscriptionIdentityAllowUnavailable, "discover", "⚠️ 身份校验：证据缺失时仍转存",
			"排查用的逃生阀，⚠️ 不建议开启。打开后，即使候选拿不到任何可验证证据（只有磁力/ed2k 链接、清单接口失败）也会照常转存，"+
				"这会破坏「未通过身份校验，一律不转存」的不变式，可能把错误内容写进网盘。仅在排查「为什么某个来源一条都转不了」时临时打开。",
			"false"),
		intSpec(KeyMOSubscriptionTransferProtectHours, "discover", "订阅转存保护期",
			"刚转存完的这段时间（小时）内不再动同一批内容，默认 3 小时。原因是网盘写入有延迟、文件清单可能还没刷新，"+
				"立刻重复转存容易产生重复目录；它同时是个限流器——媒体库索引不可用时，同一批内容最多也只每 N 小时重转一次。"+
				"设为 0 表示无保护期、转存完立刻可以再来（排查用，长期开启会导致反复转存）。",
			"3", "小时", 0, 24),

		// ---- 搜索连接器（T05）----
		stringSpec(KeyMOSubscriptionSearchSources, "discover", "订阅搜索源",
			"订阅检索默认走哪些搜索连接器，逗号分隔，按书写顺序串行检索、合并去重。"+
				"默认只有 tgto123 —— 与接入连接器之前完全一致。"+
				"⚠️ 写成未装配的 key 不会报错，该源会被静默跳过（运行日志里能看到原因），"+
				"所以填完记得看一眼订阅运行日志确认每个源都真的搜了。",
			KeyMOSubscriptionSearchSourcesDefault),
		intSpec(KeyMOConnectorTimeoutSeconds, "discover", "单个搜索源超时",
			"一个搜索源最多跑多久（秒）。超时只掐掉这一个源，其余源照常检索，"+
				"该源失败会记进订阅运行日志。默认 30 秒；调小会让慢速源更容易被掐断。",
			"30", "秒", 1, 300),
		intSpec(KeyMOConnectorSearchBudgetSeconds, "discover", "订阅检索总预算",
			"一条订阅检索所有搜索源的总时间上限（秒，默认 45）。"+
				"超预算后剩余的源**不会发起请求**，直接跳过并记日志 —— 串行检索下每源各等一次超时，"+
				"源多了总时长会线性膨胀，这个上限是总的闸门。应该 ≥ 单源超时的 1~2 倍；"+
				"设成与单源超时相等则等于只搜第一个源。",
			"45", "秒", 1, 1800),

		// ---- 订阅执行护栏（T06）----
		selectSpec(KeyMOSubscriptionExecutionMode, "discover", "订阅执行强度",
			"一条订阅**每轮**最多自动转存几条，以及两次转存之间的间隔。间隔带随机抖动，避免固定节奏被风控识别。\n"+
				"保守 1 条/45~65s，均衡 2 条/25~40s（默认），激进 3 条/10~18s。\n"+
				"被画质、体积、重复、身份校验筛掉的候选**不占次数**；真正发起后失败的**照常计数**。\n"+
				"默认「均衡」比改动前更保守：改动前一轮里所有命中的候选会连续转存，没有间隔。",
			"balanced", []Option{
				{Value: "conservative", Label: "保守（1 条 / 45~65s）"},
				{Value: "balanced", Label: "均衡（2 条 / 25~40s）"},
				{Value: "aggressive", Label: "激进（3 条 / 10~18s）"},
				{Value: "custom", Label: "自定义"},
			}),
		intSpec(KeyMOSubscriptionCustomAttempts, "discover", "自定义档·每轮条数",
			"仅在执行强度选「自定义」时生效：一条订阅每轮最多自动转存几条（1~10）。",
			"2", "条", 1, 10),
		intSpec(KeyMOSubscriptionCustomIntervalSec, "discover", "自定义档·间隔基准",
			"仅在「自定义」时生效：两次转存之间的间隔基准（秒）。实际间隔 = 基准 + 随机抖动。",
			"30", "秒", 1, 600),
		intSpec(KeyMOSubscriptionCustomJitterSec, "discover", "自定义档·随机抖动",
			"仅在「自定义」时生效：在间隔基准上叠加的随机上限（秒）。抖动是为了打散固定节奏；"+
				"调成 0 会让转存严格等间隔。",
			"10", "秒", 0, 600),
		stringSpec(KeyMOSubscriptionTimeWindows, "discover", "允许检索的时段",
			"形如 `00:00-08:00,23:00-06:00`，多个时段用逗号分隔；留空 = 不限。\n"+
				"**结束早于开始表示跨午夜**（`23:00-06:00` = 夜里 23 点到次日 6 点）。\n"+
				"时段外到点触发会**直接跳过、不补跑** —— 补跑会在恢复时段第一分钟把积压候选一起转存，"+
				"这种突发流量最容易触发网盘风控。\n"+
				"手动点「立即搜索」和新建订阅的首次搜索**不受时段限制**。",
			""),
		intSpec(KeyMOSubscriptionFinishedGraceDays, "discover", "完结宽限天数",
			"一部剧的**本季全部集数转存齐**后，再等多少天才停止检索（默认 7 天）。\n"+
				"宽限期内照常检索，但执行强度自动降到「保守」档 —— 常见的情况是刚转存完最后一集，"+
				"站方随后补出更优的压制组版本。\n"+
				"完结判据：本季已转存集号集合覆盖 1..总集数，且距**最后一集转存时刻**已超过这个天数。"+
				"总集数从资源标题的「全N集」推；**推不出来就不判完结**，宁可继续搜也不会误杀。\n"+
				"设为 0 表示转存齐后立即停止检索。",
			"7", "天", 0, 3650),
		stringSpec(KeyMOMediaParseResAliases, "media_organize", "分辨率别名表",
			"解析资源名里的分辨率时，额外认这些写法。格式 `别名=标准值`，多个用逗号分隔，例如 "+
				"`超高清=2160p,蓝光原盘=2160p,BD=1080p`。\n"+
				"**默认表里的取值是推测值**（参考实现 的原始词表在编译产物里读不出来），"+
				"按主流资源站命名习惯补齐；碰到没覆盖的写法在这里补一条即可，不用改代码发版。",
			""),
		stringSpec(KeyMOMediaParseHDRTexts, "media_organize", "HDR 识别文本（追加）",
			"识别 HDR 用的文本，逗号分隔。这张表是**追加**到内置表上，不是替换。"+
				"内置：hdr、hdr10、hdr10+、hlg。",
			""),
		stringSpec(KeyMOMediaParseDVTexts, "media_organize", "杜比视界识别文本（追加）",
			"识别 Dolby Vision 用的文本，逗号分隔，追加到内置表。内置：dolby vision、dovi、dv、杜比视界。\n"+
				"单个词的条目按**词边界**匹配，所以 DVDRip（DVD 压制）不会被误判成杜比视界。",
			""),
		stringSpec(KeyMOMediaParseSDRTexts, "media_organize", "SDR 识别文本（追加）",
			"识别 SDR 用的文本，逗号分隔，追加到内置表。内置：sdr、standard dynamic range。",
			""),

		// ---- RBAC（T08）----
		boolSpec(KeyMORBACEnabled, "system", "启用用户与权限",
			"把后台从「一个管理员账号」变成「多用户 + 用户组 + 权限」。\n\n"+
				"**默认关闭。关着的时候本系统不做任何权限判定**："+
				"不查用户表、不算权限、不裁导航，后台行为与开启前完全一致。\n\n"+
				"开启后：当前的管理员账号（配置里的 `admin_username`）**自动就是超级管理员**，"+
				"绕过一切权限；其余账号要由你建出来并加入用户组才有相应权限。\n\n"+
				"两件事永远是超管专属、不能下放：新增订阅、运行离线下载 —— 它们花的是站点账号的额度。",
			"false"),
		stringSpec(KeyMORBACDefaultUserGroup, "system", "新用户默认用户组",
			"新建用户时自动加入的组名（对应权限矩阵里的组）。留空表示不自动入组，"+
				"此时新用户登录后台后什么菜单都看不到。\n\n"+
				"填一个还没建的组名不会报错，只会让新用户暂时没有权限 —— "+
				"你建好组再把用户加进去即可。",
			""),

		// ---- 求片中心（T09）----
		boolSpec(KeyMOMediaRequestEnabled, "system", "启用求片中心",
			"把求片功能开到一个**独立端口**上（默认 7812），家人在手机上登录、搜片、提交求片。\n\n"+
				"**默认关闭。关闭时这个端口根本不监听** —— 不是「监听但拒绝访问」。\n\n"+
				"这个端口上只有登录页和求片页，任何管理接口都是 404：即使有人扫到这个端口，"+
				"也拿不到后台的任何一个接口。\n\n"+
				"**开启前请先启用用户与权限并建好家人账号**，否则求片站没有可登录的身份。",
			"false"),
		intSpec(KeyMOMediaRequestPort, "system", "求片站端口",
			"求片站独立监听的端口号，1024~65535。默认 7812（与 参考实现 一致）。\n\n"+
				"不要和管理台端口相同 —— 相同的话求片站会被管理台抢走，求片功能表现为「打开没反应」。",
			"7812", "端口", 1024, 65535),
		boolSpec(KeyMOMediaRequestRequireReview, "system", "求片需要审核",
			"开启（默认）：家人提交后进入待审列表，管理员在后台「求片中心」通过后才建立订阅。\n\n"+
				"关闭：提交即建立订阅，立刻生效。\n\n"+
				"关闭后，任何能登录求片站的人都能直接往订阅表里写条目 —— 家里只有你一个人用、"+
				"且不想多点一次鼠标时才建议关。",
			"true"),
		intSpec(KeyMOMediaRequestDailyLimit, "system", "每人每天求片上限",
			"每个用户每天能提交的求片条数，0 表示不限。默认 5。\n\n"+
				"存在的理由不是省配额，是防止一个人（或一段脚本）把订阅表刷爆。",
			"5", "条", 0, 1000),
		intSpec(KeyMOMediaRequestTagMaxPerUser, "system", "每人标签数上限",
			"每个用户对同一部作品最多保留多少个入库标签，0 表示不限。默认 20。\n\n"+
				"标签会写进入库文件名，超了会让文件名长到难以辨认。",
			"20", "个", 0, 200),
		intSpec(KeyMOMediaRequestTagMaxLength, "system", "单个标签字数上限",
			"单个入库标签最长多少字，0 表示不限。默认 100。",
			"100", "字", 0, 500),

		// ---- 免登录分享页（T10）----
		boolSpec(KeyMOLibraryShareEnabled, "system", "启用媒体库分享页",
			"把媒体库里选中的影片/剧集做成一条**免登录**的分享链接，发给不需要账号的人直接看。\n\n"+
				"**默认关闭。关闭时分享页、令牌接口、播放接口全部 404**，链接打不开。\n\n"+
				"安全设计：链接里的短码只是门牌号（谁拿到都能打开页面），"+
				"真正看片要凭一条**按访客签发、只存在访客浏览器里**的令牌，"+
				"令牌 24 小时失效，超出「同时在线设备数」会被拒绝。\n\n"+
				"开启后请先确认：你打算分享的这些内容确实可以给外部人看。",
			"false"),
		intSpec(KeyMOLibraryShareDefaultExpireDays, "system", "分享默认有效期",
			"新建分享时默认的有效期天数，0 表示永久。默认 7。\n\n"+
				"新建对话框里仍然可以逐条选择 1 / 3 / 7 / 30 天或永久，"+
				"这个值只决定「没选时按哪个算」。\n\n"+
				"永久分享只在「就是想长期挂着一条链接」时才用：一条永远不过期的链接，"+
				"被转发出去之后就只能靠「撤销」收回。",
			"7", "天", 0, 365),
		intSpec(KeyMOLibraryShareDefaultMaxDevices, "system", "分享默认设备数上限",
			"新建分享时默认的同时在线设备数上限，默认 5。\n\n"+
				"按「最近 24 小时内访问过这条分享的不同访客」计数，"+
				"所以关掉页面的设备会自己让出名额。\n\n"+
				"这是防「一条链接被丢进群里、几百号人一起看」的闸门，"+
				"填得太大就等于没有。",
			"5", "台", 1, 100),
		stringSpec(KeyMOLibraryShareDefaultPassword, "system", "分享默认访问口令",
			"新建分享时默认带上的访问口令，访客要先输对才能看。留空表示新建时默认不带口令。\n\n"+
				"口令只挡住「链接随手转发」这一层；它以哈希形式存储，不保存明文。\n\n"+
				"如果你希望每条分享都必须带口令，在这里填一个全局默认值即可。",
			""),

		// ---- 播放监控与观影报告（T11）----
		boolSpec(KeyMOPlayMonitorEnabled, "system", "启用播放监控",
			"记录「此刻谁在放哪部片、走了哪条链路」，并按 **码率 × 时长** 估算上行流量。\n\n"+
				"**默认关闭。关闭时监控既不建会话也不记流量**，实时列表永远是空的。\n\n"+
				"三种播放状态只有一种计费：\n"+
				"· **计费中** —— 外网播放、且字节流经过你自己的服务器（流代理）。这条才估算上行。\n"+
				"· **CDN 直连** —— 走 115 等网盘的直链重定向，字节流**完全不经过**你的服务器，**计 0**。\n"+
				"· **局域网** —— 内网播放，**不计费**。\n\n"+
				"显示的数字是**估算值**（码率 × 时长，约每 5 秒累计一次），不是精确的上行字节数；\n"+
				"面板里同时给出实测字节数供对照，两者偏离过大时会写日志告警。\n\n"+
				"开启即表示你接受「估算值可能与实际账单有出入」这一点。",
			"false"),
		intSpec(KeyMOPlayMonitorIdleSeconds, "system", "播放监控空闲判定（秒）",
			"多久没收到同一个会话的后续取流请求，就认为已经停播、把它从实时列表里移除。默认 60。\n\n"+
				"调小会让「暂停中」的片很快消失（但不会被记成新的一次播放）；\n"+
				"调大会让真正停播的片在列表里多挂一会儿。\n\n"+
				"停播后从列表消失的时长由它决定，默认约一分钟。",
			"60", "秒", 10, 3600),
		intSpec(KeyMOPlayMonitorSampleSeconds, "system", "流量累计间隔（秒）",
			"每隔多少秒把「码率 × 间隔」累进当日流量桶。默认 5。\n\n"+
				"这是估算精度与写入频率的取舍：间隔越短越贴近真实曲线，写库也越频繁。\n\n"+
				"一般不需要改。要改的话建议不低于 5 秒。",
			"5", "秒", 1, 300),
		boolSpec(KeyMOPlayReportEnabled, "system", "启用观影报告",
			"按周期（7 / 14 / 30 天）汇总播放情况，生成排行图并通过通知渠道推送。\n\n"+
				"**默认关闭。**\n\n"+
				"统计口径（这几条不是可以自定义的，是口径本身）：\n"+
				"· **统计从启用观影报告后开始累计，启用前的播放不会补算。**\n"+
				"· 播放次数 = 关掉播放器重开，或**中断超过半小时**算新的一次。\n"+
				"· 「忽略播放时长低于 N 秒」：**低于**阈值的记录不进入排行，**刚好等于**阈值的仍保留。\n"+
				"· 上行字节**只统计计费中的播放**；CDN 直连与局域网都是 0。\n\n"+
				"没有勾选通知渠道时会当场提示，不会假装已经发出去了。",
			"false"),
		intSpec(KeyMOPlayReportMinSeconds, "system", "忽略播放时长低于（秒）",
			"播放时长**低于**这个秒数的记录不进入观影报告排行。默认 0，即不过滤。\n\n"+
				"注意是「低于」：刚好等于这个秒数的记录**仍然保留**。\n\n"+
				"用来挡掉误触后秒退的播放（比如手滑点开又关掉）。",
			"0", "秒", 0, 86400),
		intSpec(KeyMOPlayReportGapMinutes, "system", "中断多久算新一次播放（分钟）",
			"同一用户同一部片，中断**超过**这么多分钟才算新的一次播放。默认 30。\n\n"+
				"也就是说：中断 20 分钟仍算同一次播放，中断 35 分钟算新的一次。\n\n"+
				"「关掉播放器重开」也是靠这个间隔来区分的 —— 间隔小于它的会被并进上一次。",
			"30", "分钟", 1, 1440),

		// ---- RSS 订阅源（T16）----
		boolSpec(KeyMORSSEnabled, "discover", "启用 RSS 订阅",
			"贴一个 RSS feed 地址，系统就会按固定间隔去拉新条目，"+
				"自动把磁力/ed2k 链接提交到 115 离线下载。\n\n"+
				"**默认关闭。** 关闭时轮询 worker 根本不启动，不占用网络，也不会消耗离线下载额度。\n\n"+
				"支持 Mikan、dmhy、nyaa 等常见 BT RSS。自研解析器的支持范围：\n"+
				"· RSS 2.0、Atom、RDF（RSS 1.0）\n"+
				"· **仅 UTF-8**（含带 BOM 的 UTF-8）—— GBK/Big5 等非 UTF-8 feed 会**明确报「不支持」**，不会静默失败\n"+
				"· 命名空间条目（`dc:creator`、`content:encoded`）\n"+
				"· `<enclosure>` 与描述文本里的 `magnet:` / `ed2k:` 链接\n\n"+
				"**不支持**：非 UTF-8 编码、需要登录或 Cookie 的站点、需要执行 JS 的动态页面。",
			"false"),
		intSpec(KeyMORSSPollIntervalMinutes, "discover", "RSS 轮询间隔（分钟）",
			"多久去看一次每个启用的 RSS 源有没有新条目。默认 30。\n\n"+
				"这是**看新条目的频率**，与「停机后追不追补」无关（那是另一个键）。\n\n"+
				"调小会更快发现更新，但每个源都会更频繁地被访问一次。",
			"30", "分钟", 1, 1440),
		intSpec(KeyMORSSCatchupGapHours, "discover", "停机多久后放弃逐条追赶（小时）",
			"距离上一次成功同步超过这么多小时，就**只取最新一页**，不再把停机期间的全部条目逐条补齐。\n\n"+
				"默认 12。\n\n"+
				"为什么不逐条补：停机三天后启动，逐条补齐意味着把三天里错过的更新一次性塞进离线下载队列，"+
				"大部分是用户已经不需要的旧番。**你要的是「现在追到哪了」，不是「补三天前就该错过的片」。**\n\n"+
				"⚠️ 这是本仓给 RSS 新增的行为 —— 参考实现 侧 RSS 没有位点、也没有这个阈值。",
			"12", "小时", 0, 8760),
		intSpec(KeyMORSSStaleAfterMinutes, "discover", "跳过多久以前的条目（分钟）",
			"条目的发布时间早于「现在 - N 分钟」就直接跳过，不提交离线下载。默认 60。\n\n"+
				"这是防止「一个发布时间格式解析错、显示成 1970 年的 feed」把整个历史全塞进队列。\n\n"+
				"留 0 表示不按发布时间过滤。",
			"60", "分钟", 0, 525600),
		intSpec(KeyMORSSHTTPTimeoutSeconds, "discover", "单个 feed 拉取超时（秒）",
			"拉一个 RSS feed 最多等多少秒，超时就当这个源这轮失败。默认 20。\n\n"+
				"**一个源超时不会影响其它源** —— 每个源独立超时、独立记状态。\n\n"+
				"遇到某个站打不开时，可以单独把它调短。",
			"20", "秒", 1, 300),
		intSpec(KeyMORSSMaxFeedBytes, "discover", "单个 feed 响应体上限（字节）",
			"拉回来的 feed 超过这个大小就直接报错。默认 4194304（4 MiB）。\n\n"+
				"**超限不截断** —— 截断出来的半个 XML 要么解析失败，要么悄悄丢掉尾部条目，"+
				"这两种情况用户都无从察觉。报错至少能在同步结果里看到。",
			"4194304", "字节", 65536, 67108864),

		// ---- 洗版（T07）----
		boolSpec(KeyMOMediaUpgradeEnabled, "discover", "启用洗版",
			"⚠️ **这是一个会删用户文件的功能，默认关闭，且不打算默认打开。**\n\n"+
				"洗版做的是：扫描媒体库，把每个版本槽位（分辨率/编码/制作组/音轨/字幕/容器）里"+
				"被新版本比下去的文件标出来，然后按你配的「败方动作」处理（保留/删除/移到别处）。\n\n"+
				"安全设计：扫描**只判定不删文件**，判定结果连同旧文件快照存进记录表；"+
				"执行时重新枚举一次旧文件，对不上就整条作废标「判定已过期」，绝不按过期结论删。\n"+
				"另外败方动作默认是「保留」—— 要删必须你自己显式改成「删除」。",
			"false"),
		selectSpec(KeyMOMediaUpgradeSource, "discover", "洗版扫描源",
			"从哪里读媒体库文件列表。\n"+
				"`local`：递归扫描本地目录，会**真实删除**本地文件。\n"+
				"`emby` / `jellyfin`：从 Emby 媒体索引（`emby_media_items`）读文件列表。\n"+
				"这两个源记的是网盘路径，执行时如果该路径在本机文件系统上不存在，会记「路径不可达」"+
				"并**跳过、不删** —— 按一条可能不存在的路径删文件有误删风险。\n"+
				"Plex 不支持洗版，本仓也没有 Plex 索引，故无此选项。",
			"local", []Option{
				{Value: "local", Label: "本地目录"},
				{Value: "emby", Label: "Emby 索引"},
				{Value: "jellyfin", Label: "Jellyfin 索引"},
			}),
		stringSpec(KeyMOMediaUpgradeLibraryRoot, "discover", "媒体库根目录",
			"被比较的「现版」文件所在处，例如 `/media/已整理`。留空则扫描无法进行。\n"+
				"`local` 源下这是扫描与执行两次枚举都使用的根目录 —— 提交前的快照复核就是"+
				"重新走一遍这里，比对文件集合、大小与修改时间。",
			""),
		stringSpec(KeyMOMediaUpgradeCandidateRoots, "discover", "候选目录",
			"新版出现的地方，逗号分隔，例如 `/media/待洗版`。留空 = 只用媒体库根目录内的文件做候选。\n\n"+
				"注意：**媒体库目录内的文件本身也会被当作候选**。所以你把一个 2160p 版本直接丢进"+
				"已整理目录，它同样会被识别成「新版」并与同槽位的旧版本比较。",
			""),
		intSpec(KeyMOMediaUpgradeMaxRecordsPerSeries, "discover", "单剧记录上限",
			"一部剧在一次扫描里最多产出多少条**可执行**记录（默认 0 = 不限）。\n\n"+
				"超限的判定**仍然入库**，只是状态标成 `skipped_limit` 不执行 —— 这样你在界面上"+
				"能看到「系统认为这里有 316 集值得洗」，而不是什么都看不到。\n"+
				"长剧（银魂 E001~E316 这类）建议设个几十到一百，避免一次扫描把整部剧洗掉。",
			"0", "条", 0, 10000),
		selectSpec(KeyMOMediaUpgradeLoserAction, "discover", "败方动作",
			"比较中的**输家**怎么处理。新版赢时输家是旧文件，新版输时根本不会走到执行。\n\n"+
				"`keep`（默认）：旧文件原样留着。最安全 —— 只是告诉你「这里有更好的版本」，动不改。\n"+
				"`delete`：删掉输家文件。**这是真的会删磁盘文件。**\n"+
				"`move`：把输家移到下方指定的目录，留个后悔药。\n\n"+
				"与 参考实现 的差别：参考实现 是「被替换的旧文件恒删」。这里默认 keep，"+
				"要删得显式改这项 —— 见变更说明里的偏差记录。",
			"keep", []Option{
				{Value: "keep", Label: "保留（默认，最安全）"},
				{Value: "delete", Label: "删除败方文件（真删）"},
				{Value: "move", Label: "移动到指定目录"},
			}),
		stringSpec(KeyMOMediaUpgradeMoveDir, "discover", "败方移动目标目录",
			"败方动作选「移动」时的目标目录。不填会直接报错，不会退回删除。",
			""),
		stringSpec(KeyMOMediaUpgradeGroupPriority, "discover", "制作组优先级",
			"同一分辨率同一编码时按哪个组优先，逗号分隔，越靠前越优先，例如 `Ocat,FRDS,CMCT`。\n\n"+
				"未列出的组排在后面；大小写不敏感。留空表示所有组等价。",
			""),
		intSpec(KeyMOMediaUpgradeMinResolution, "discover", "最低分辨率门槛",
			"低于这个分辨率的候选直接不参与洗版（不产生记录）。0 = 不限。\n"+
				"这是**防误洗**的第一道闸：把 1080p 的片库保护起来，不让低码流版本去挑高码流版本的毛病。",
			"0", "", 0, 4320),
		intSpec(KeyMOMediaUpgradeMinChannels, "discover", "最低声道门槛",
			"低于这个声道的候选直接不参与洗版。0 = 不限。\n\n"+
				"⚠️ 这项对齐的是**声道数**，不是音轨数。参考实现 的字段名叫 `min_audio_tracks`（音轨数），"+
				"但文件名里根本没有音轨信息 —— `Atmos` 是 8 声道、`DTS-HD MA` 也是 8 声道、"+
				"`AAC 2.0` 是 2 声道，音轨数无从得知。所以本项与 参考实现 同名字段**不等价**，别按音轨数理解。",
			"0", "声道", 0, 16),
		boolSpec(KeyMOMediaUpgradeRequireSubtitle, "discover", "只认带字幕标记的候选",
			"开启后，文件名里没有字幕标记的候选不参与洗版。默认关闭。\n\n"+
				"⚠️ **字幕标记是从文件名猜的**（字幕/中字/中英/双语/简繁/CHS/CHT/BIG5）。"+
				"参考实现 的槽位里「字幕」来自媒体服务器的轨道探针，本仓对本地文件没有探针，只能猜。\n"+
				"猜不出来的文件会被判成「无字幕」，于是与「带字幕」的文件分属不同槽位 —— 两个都保留。这是安全的一侧。",
			"false"),

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
		// fnos 这组以前有 5 个 key 却没有登记过分类：界面上它们拿不到分组标题，
		// 搜索索引也没法归位。宁可补一条登记，也不要把它们悄悄并进别的分组 ——
		// 并进去等于告诉用户「这属于系统设置」，而它其实是另一项功能的开关。
		{ID: "fnos", Label: "飞牛影视"},
	}
}

// AllSpecs 返回全部设置声明的副本（按声明顺序），供派生型功能使用 ——
// 目前是设置项搜索索引（后台 ⌘G 直达）。
//
// 为什么导出它而不是让调用方手写一份清单：手写清单必然会漏。
// 注册表里新增一个 key 而清单没更新，症状是「设置页上找得到、搜索里搜不到」，
// 而用户只会得出「这个功能没做」这一个结论。
//
// 返回的是副本而不是内部切片：Spec 里的 Min/Max 是指针、Options 是切片，
// 直接把内部切片交出去，调用方改一下就等于绕过 Service.Update 的校验改注册表。
func AllSpecs() []Spec {
	src := defaultSpecs()
	out := make([]Spec, 0, len(src))
	for _, sp := range src {
		if sp.Options != nil {
			sp.Options = append([]Option(nil), sp.Options...)
		}
		out = append(out, sp)
	}
	return out
}

// Categories 返回有序分组定义的副本。
//
// ⚠️ 它**不是**「实际用到的分组的全集」，只是一份手维护的登记列表。
// 注册表里的 Spec.Category 指向一个这里没登记的 ID 时，那组设置项在界面上
// 就没有标题可用 —— 所以 AllSpecs 的调用方不许假设它穷尽。
// 为什么不改成从注册表派生：派生的分组标题得有一套从 key 推名字的映射，
// 那套映射比手写这张表更容易错，而且错的时候没有编译期检查。
func Categories() []Category {
	return append([]Category(nil), categories()...)
}
