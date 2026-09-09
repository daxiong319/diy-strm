# tgto123 新版功能复刻规格（协议侦察成果 → diy-strm 实现规格）

> 2026-09-09 基于 walkingd/tgto123:latest（digest 0595f94c…，Web 端口 12366）黑盒侦察。
> 方法：登录后台会话 + 112 个 GET 端点全量探测（响应归档于 `tgto123-re/api_probe/`）
> + 明文前端 JS（script.js 1.4MB / media_discovery.js 388KB / visual_filter_page.js /
> emby_proxy_playback_records.js）的接口形状静态还原。
> 老版反编译源码（tgto123-re/src_plain/，956 函数）可直接参考的模块也已标注。

## 0. 服务拓扑变化（新版）

| 项 | 旧版(8.5.10) | 新版 |
|---|---|---|
| Web 后台端口 | 8094? | **12366**（主 Flask），8098 = Emby/飞牛反代口 |
| 影巢(HDHive) | hdhive 命名 | **改名 RE0**（接口 /api/re0/*，兼容旧 hdhive 值） |
| 资源搜索来源 | hdhive 单源 | **re0 + guanying(观影) + seedhub** 三源聚合 |
| 新增模块 | — | danmu, guanying_client, media_discovery_service, emby_virtual_library_mixin, emby_ranking_collection_catalog(.so), server_visual_filter, tg_visual_filter_*, guangya_gcid_transfer, gcid_cid_cache, bilibili_download, music_*（音乐整理）, seedhub_client, pi kakp_* 等 |

## 1. 影视发现板块（整体照搬 → diy-strm「发现」页）

diy-strm 已有 internal/discovery（explore/rankings/calendar/anime/favorites/settings）与
AppDiscover.vue，路由 /media-discovery/*。照搬以下缺口：

### 1.1 详情页（重点，diy-strm 缺）
- `GET /api/media/details/tmdb/{movie|tv}/{tmdbId}` → data 含：
  cast/crew(含 profile_url)、seasons、recommendations、videos、external_ids、
  genres、overview、score、provider(_label)、resource_sources、subscribable、
  presentation_mode、monitor_status。
- 素材图：`https://image.tmdb.org/t/p/{size}{path}`（w500 人像 / w1280 背景图 / original 海报）。

### 1.2 资源搜索（详情页内嵌「关联资源」）
- `POST /api/media/resources/search`
  payload：`{ title, aliases: [最多2个], tmdb_id, media_type: 'movie'|'tv', year, sources: ['re0'|'guanying'|'seedhub'] }`
  （前端按源分 3 个并发请求，每请求只带 1 个 source，最后 merge 去重 item_key）。
- 响应：`{ data: { items: [ResourceItem], errors: [ {source, code, error} ] } }`
- ResourceItem 字段（卡片渲染所需全集）：
  `item_key, source, provider, provider_label, title, slug, share_url,
  link_type('magnet'|'ed2k'|分享), size, episode{season_num,episode_num,end_episode_num,total_episode_num,is_complete,is_updated},
  is_unlocked, points_known, unlock_points, unlocked_users_known, unlocked_users_count,
  remark, validate_message, is_official, sharer(发布者),
  resource_spec_tags[] 或 resource_specs{resource_pix,web_source,resource_type,resource_effect,video_encode,audio_encode},
  subtitle_languages[], subtitle_types[], supported_targets[], target_provider,
  raw_metadata{remark,validate_message,...}`
- provider 过滤器：`all/115/123/guangya/magnet`；规格过滤器三组：
  resolution(resource_pix)/effect(resource_effect)/group(resource_team)。
- 未登录源返回 403 `{code:'RESOURCE_SOURCE_UNAVAILABLE', error:'该资源来源不可用'}`。
- 离线（磁力/ED2K）：`POST /api/media/resources/offline` `{link, provider, source}`
  （ed2k→[115,guangya]，magnet→[115,123,guangya]，按 providers.transfer.targets 配置）。
- 复制链接：`POST /api/media/resources/copy-link` `{source:'re0', provider, slug}`。
- 转存分享：`POST /api/media/resources/transfer`
  `{source, provider, slug, share_url, candidate_item_id}`；
  RE0 源提示积分（is_unlocked/points_known/unlock_points）。
- 目标目录配置：`PUT /api/media/settings` 的 `media_transfer_targets`
  `{115:{configured,folder_id,folder_name},123:{...},guangya:{...}}`。

### 1.3 榜单/日历/订阅（diy-strm 已有骨架，补形状）
- `GET /api/media/rankings/{provider}?...` data：
  items[{entity_key 'tmdb:movie:414419', origin, match_confidence, douban_rating,
  genres[], origin_country[], poster/backdrop_url, tmdb_id...}],
  available_media_types[], available_regions[], feed_status, is_partial,
  requires_hdhive_configuration, matcher_version。
- `GET /api/media/calendar` → 842 items（含 calendar_date/calendar_bucket/
  calendar_episode/subscribable/imdb_id/network 等），cache_ttl 21600s。
- 订阅：`GET/POST/PATCH/DELETE /api/media/subscriptions[/{id}][/run]`；
  payload：`{...item, target_provider, transfer_mode:'auto', interval_minutes:360,
  enabled, metadata, preferences{...settings, max_points}, rules:[{name,
  target_provider('115'|'123'|'guangya'), max_points, enabled, resolutions[],
  qualities[], languages[], release_groups[], message_keywords, must_contain,
  must_not_contain, prefer_dolby_vision}]}`。
- Emby 缺集：`/api/media/emby-missing/{libraries,scans,results,subscriptions,status,events}`，
  `POST scans {library_ids}`、`POST subscriptions {result_ids, scan_id, ...options}`。
  前置：`media_emby {enabled, server_url, api_key}` 存于 settings；
  未配置 → 409 `EMBY_MISSING_NOT_CONFIGURED`「请在RE0反代中配置独立的 Emby 服务器地址和 API Key」。
- 卡片入库态批量：`POST /api/media/emby/cards {items:[{tmdb_id, media_type}]}` →
  `{configured, items:[...], progress_queued_ids}`；剧集进度预热
  `/api/media/emby/tv-progress/preheat {tmdb_ids[≤48], priority}`。
- 基础配置 payload（PUT /api/media/settings）：
  `{media_transfer_targets, media_emby{enabled,server_url,api_key},
  guanying{enabled}, ranking_virtual_libraries{enabled, rankings[{provider,media_type}],
  collection_keys[], collection_keys_selection_version:2, target_rule_ids[],
  shenyi_global_view_order_rule_ids[]}}`。

## 2. RE0 改名（原影巢/HDHive）

tgto123 侧：仅 UI 文案改 RE0 + 端点前缀 /api/re0/*，**业务后端还是 hdhive.com
OpenAPI**（X-API-Key + OAuth code 换 token，路径 /api/open/resources/{type}/{tmdb_id}、
/api/open/shares/{slug}、/api/open/resources/unlock、/api/open/checkin、/api/open/me）。
diy-strm 无需改协议客户端，做兼容层即可：
- 后端：/api/re0/* 别名路由（新）+ 保留 /hive/*（旧）；
  `resource_source` 取值兼容 `hdhive`↔`re0`。
- 前端文案：影巢→RE0（AppCloudSubscription / AppMonitorHistory / HiveSymediaCallback /
  订阅设置等全部出现处）。
- RE0 授权（新形状）：`GET /api/re0/authorize`（浏览器开新窗）、`GET /api/re0/status` →
  `{authorized, user, sub_accounts[], authorization{state, authorized_at, expires_at,
  expires_in_seconds, refresh_expires_at, last_verified_at}}`；
  子账号：`GET /api/re0/subaccounts`、`POST /api/re0/subaccounts/{id}/authorize`、
  `DELETE /api/re0/subaccounts/{id}/authorization`、`PATCH /api/re0/subaccounts/{id} {enabled}`。
- RE0 反代（= 我们已有 hdhive proxy）：`GET /api/re0_proxy/config`、
  `POST /api/re0_proxy/config/save {listen_port, enabled, http_crypto_compat_enabled,
  emby_helper_enabled, emby_helper, transfer_targets}`、`POST .../config/test`、
  `.../config/toggle`、`POST .../emby_helper/test`、`POST .../entry_auth/clear {id}`。
- RE0 签到配置：`PUT /api/re0/checkin/config`（re0CheckinConfigPayload）。

## 3. 观影（guanying）接入

后端模块 guanying_client；设置存于 media settings.guanying：
`{enabled, configured, credentials_saved, session_saved, session_expires_at,
account_hint, last_checked_at, last_error, message, status}`。

- 登录：`POST /api/media/guanying/login {username, password, attempt_id?}` →
  - 需要验证码：`{data:{captcha_required:true, attempt_id}}`（HTTP 200）
  - 成功：`{data: settings...}`（凭据与会话服务端加密落盘；前端只保留本次内存）
  - 失败：400 GUANYING_USERNAME_INVALID（2-30 位校验）/ 409 GUANYING_LOGIN_FAILED
    （「账号或密码错误，你的IP地址还有N次尝试机会」——上游有 IP 限次）
- 点选式验证码（按字顺点击）：
  `POST /api/media/guanying/captcha {attempt_id}` →
  `{data:{attempt_id, text, image(dataURL/base64), type, width:350, height:200}}`；
  `POST /api/media/guanying/captcha/verify {attempt_id, points:[{x,y},...]}`（点数=字符数）。
- 会话恢复：`POST /api/media/guanying/relogin {attempt_id?}`（saved 凭据自动再登录）；
  测试：`POST /api/media/guanying/test`；清除：`DELETE /api/media/guanying/session`；
  当前会话：`GET /api/media/guanying/session`（200）/ 未配置 404 形状未验。
- 资源搜索：作为 resources/search 的 source='guanying'，返回的分享是上游公开分享
  （无 RE0 积分语义）；磁力/ED2K 走 offline。
- diy-strm 实现：internal/guanying 包（client + 会话加密存储，复用我们已有的
  本机加密凭据存法），controllers/media_guanying.go 五个端点，接入资源搜索聚合。

## 4. SeedHub（第三源）

seedhub_client 模块；前端：网盘分享结果只读展示，磁力/ED2K 可复制/离线。
capabilities.seedhub.enabled 开关。diy-strm 暂做占位开关 + 预留 source。

## 5. 弹幕（danmu）

- 模式：**对接外部 Misaka Danmaku 服务**（DANMAKU_API_URL + DANMAKU_API_KEY），
  不是自建弹幕库。
- 触发点：302 播放时自动「刷新下一集弹幕」（下载 danmaku → 按 TMDB id/季/集命名存储）。
- 老版源码可参考：src_plain/danmu/（download_danmaku/extract_tmdb_id/
  download_single_episode/extract_episode/extract_season/extract_work_title/is_tv_series）。
- diy-strm 实现：设置页新增 DANMAKU_API_URL/KEY；emby302 播放钩子
  （strm_redirect 后）异步调 Misaka API 刷新下一集；STRM 同目录写 danmaku 文件。

## 6. AI 识别（ai_media_parser + server_ai_media_parser）

- 配置端点：`GET /api/ai-media-parser/config` →
  `{config:{api_key, api_url, enabled:'0'|'1', model, prompt, timeout,
  organize_123_enabled, organize_115_enabled, organize_115_share_enabled,
  organize_guangya_enabled}}`（prompt 为完整内置默认提示词，已全文拿到）。
- 触发链：整理模块常规识别失败时调用一次 AI 补识别；每网盘可单独开关。
- 缓存：`POST /api/ai-media-parser/cache/clear`；连通性：`POST .../test-connection`。
- 内置能力（老版源码 ai_media_parser/ 47 函数）：OpenAI 兼容 chat/completions、
  exact/semantic 两级缓存（SQLite）、中英数字边界拆分、文件名预提取（季/集/分辨率/
  音视频编码/来源）后仅把残段交给 AI。
- diy-strm：internal/aiidentify 包 + controllers（config/test/cache-clear）+
  moviepilot 整理链失败分支挂 AI 兜底（已有 RecognizeMedia 位置）。

## 7. Emby 虚拟库 / 排行榜合集（emby_virtual_library_mixin + emby_ranking_collection_catalog + .so）

- 形态：在 **RE0/Emby 反代层**注入虚拟库（media_discovery.js 的
  ranking_virtual_libraries 配置：enabled + rankings[{provider,media_type}] +
  collection_keys[] + target_rule_ids[]，封面图 100+ 张内置
  static/emby_virtual_library_covers/*.png：豆瓣榜单/IMDb Top250/各大奖项/流媒体品牌）。
- 神医全局视图顺序：shenyi_global_view_order_rule_ids。
- 榜单合集目录 emby_ranking_collection_catalog（核心算法原生 .so 保护——黑盒侧：
  虚拟库即按榜单/合集生成 Emby 虚拟文件夹视图，配合 cover 生成器）。
- 配套：`POST /api/emby-cover-generator/{config,generate}`、
  `GET /api/emby-cover-generator/libraries`（封面生成器）。
- diy-strm 实现：作为「发现→榜单虚拟库」设置（勾选要生成的榜单）+ Emby 反代
  虚拟目录注入 + 封面图本地化（可直接用公开的封面 PNG 名单）。

## 8. 可视化筛选（visual_filter）

- 页面：/visual-filter（templates/visual_filter.html + visual_filter_page.js/css）。
- 端点：`GET /api/visual-filter/config?scene=ENV_FILTER`、
  `POST /api/visual-filter/config`（保存 payload）、
  `GET /api/visual-filter/tmdb/search?query=&type=`（海报匹配，
  响应含 image_base_url + results[{poster_path}]）。
- 数据模型：场景 4 个（ENV_FILTER=123 频道白名单 / ENV_FILTER_115 / ENV_FILTER_189 /
  ENV_FILTER_GUANGYA），每个场景：
  `{key,label,parse_mode('all'|'advanced'|'meta'),raw_regex,advanced_raw_regex,
  meta_raw_regex,rules[],all_mode,updated_at,parse_hint}`；
  rules[] 为可视化规则（media_name/title/preferred_name/media_type/poster_url…）。
- 作用：把正则白名单可视化为规则卡片 + TMDB 海报补全；TG 侧同步
  （tg_visual_filter_utils/tg_visual_filter_workflow）。
- diy-strm：设置→频道订阅加「白名单可视化」子页（场景=123/139/光鸭频道订阅），
  规则模型与 /scrape/tmdb-search 复用做海报补全。

## 9. 播放记录（playback records）

- 注入 JS（emby_proxy_playback_records.js）在 Emby/飞牛反代页面上跑，
  `GET /api/emby_proxy/playback_records?id={rule_id}&page=&page_size=30`。
- 记录写入：反代 mixin 在播放重定向时 record_playback_redirect（老版
  emby_proxy_playback_record_mixin 源码可参考：_init_playback_record_runtime /
  _build_playback_query_headers / record_playback_redirect / _build_playback_record）。
- diy-strm：emby302 已有播放链路——加 playback_records 表（rule_id, item 信息,
  user, 时间, 直链类型），反代根路径注入同款 JS + 分页查询端点。

## 10. Emby / 飞牛反代（feiniu）

- 反代规则模型（script.js）：`{id, proxy_type: 'emby'|'feiniu'(影视)|'feiniu_music',
  name, host, api_key(仅 emby), listen_port, enabled, path_mappings[]}`；
  feiniu_music 的 path_mappings = [{provider, local_path, cloud_path}]
  （至少一条具体网盘音乐子目录，不能根目录）。
- 端点：`GET /api/emby_proxy/config`、`POST /api/emby_proxy/config/save_one {rule}`、
  `POST /api/emby_proxy/config/toggle {id, enabled}`、`POST .../delete_one {id}`、
  `POST .../test`。
- 飞牛影视（feiniu）：老版 emby_proxy_service 已有（PROXY_TYPE_FEINIU、
  feiniu_item_playback_seed_cache、prewarm 并发 4/TTL 8s 等源码在 src_plain）；
  飞牛音乐（feiniu_music）+ emby_proxy_music_mixin 为新增。
- 飞牛媒体信息探测拦截：ENV_115_STRM_BLOCK_FEINIU_MEDIA_INFO_UA（Lavf/60.3.100
  UA → 403，仅非反代 /play115 与 /play115share）。
- diy-strm：emby302 现为单实例+单端口；改造为「反代规则列表」模型（emby/飞牛影视/
  飞牛音乐三类，多规则多端口监听），复用现有 302/strm 改写链 + 注入 JS。

## 11. 光鸭 GCID 秒传

- 配置：ENV_GUANGYA_GCID_UPLOAD_PID（GCID JSON 导入目标目录）、
  ENV_GUANGYA_PIKPAK_UPLOAD_PID（PikPak 分享→GCID JSON→光鸭导入）。
- 入口：`GET /api/guangya/gcid-export?...`（选光鸭目录→生成秒传 JSON→发 TG bot）；
  STRM 播放地址形态 `/playgy/{file_id}/{gcid}/{size}/{filename}`（GCID 已进直链）。
- 模块：guangya_gcid_transfer / gcid_cid_cache / guangya_generation_transport /
  guangya_auto_cleanup / pikpak_gcid_transfer / pikpak_drive_client。
- diy-strm：guangyapan 已有秒传基础；补 GCID 表(gcid↔cid/size) + 目录导出 JSON +
  JSON 导入秒传 + bot 命令。

## 12. 其余照搬候选（本批不做，备忘）

- Emby 看板（server_emby_dashboard：趋势/Top用户/live）、元数据清理器、
  115 STRM 失效清理器、PT 辅助（ptto115/ptto123/seedhub_client）、nullbr/pansou/
  butailing 聚合搜索、签到（qiandao189/re0_checkin）、usage_statistics、
  音乐整理全家桶（music_*，读取 tags/musicbrainz）、SSH 工具页、
  TG 定时发送（server_tg_scheduled_sender）、123 API 限流工具箱。
