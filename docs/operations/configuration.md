# 配置、密钥与日志

> 职责：说明 QMediaSync 的运行配置、第三方密钥、日志和与配置相关的运行时限制。
>
> 权威范围：本文档维护配置文件、端口、密钥优先级、日志和运行参数；浏览器认证见 [认证会话](../architecture/authentication-sessions.md)，反向代理见 [反向代理](reverse-proxy.md)。
>
> 修改时机：修改配置字段、默认值、密钥来源、日志行为、Emby 302 TLS 选项或运行时监控指标时必须更新本文档和 `docs/examples/config.yaml`。
>
> 相关代码：`backend/internal/helpers/config.go`、`backend/internal/helpers/logger.go`、`backend/main.go`、`backend/emby302.yaml`、`docs/examples/config.yaml`。

## 配置文件与默认端口

- 主配置为 `config/config.yaml`，兼容旧 `config.yml`。首次启动缺少主配置时不再进入配置向导，直接用默认值（内嵌 PostgreSQL）加环境变量生成配置并启动；仅当显式设置 `QMS_SETUP_WIZARD=1` 时才启动旧配置向导（可选择 SQLite 或外部 PostgreSQL）。
- 默认端口：`12333`，Web 管理界面与 Emby 302 代理共用（单端口架构）。无证书时仅提供 HTTP；存在 `config/server.crt` 与 `config/server.key` 时，同一端口同时提供 HTTP 与 HTTPS。
- 完整字段示例见 [config.yaml](../examples/config.yaml)。示例仅说明字段，运行时以 `config/config.yaml` 为准。
- 代码默认数据库配置为 `postgres + embedded`。Docker 镜像安装 `postgresql15`；裸二进制和本地开发环境不携带 PostgreSQL 二进制，使用 PostgreSQL 时应安装 PostgreSQL 15 及以上、配置外部数据库，或自行保证内嵌模式依赖的命令可用。
- 数据库引擎、备份恢复和修复操作见 [数据库运维](database.md)；表、版本和迁移语义见 [数据库 schema 与迁移](../reference/database-schema.md)。

## 环境变量覆盖（.env / Docker 环境变量）

配置支持「YAML 基线 + 环境变量覆盖」：启动时先读取 `config/config.yaml`，再以进程环境变量覆盖同名配置项；环境变量非空时生效，未设置或留空则保留 YAML 值。`config/.env`（`KEY=VALUE` 格式，`#` 注释）在启动时自动加载并注入进程环境，Docker 部署时也可用 `env_file` / `-e` 提供相同变量。优先级：环境变量 / `config/.env` > `config/config.yaml` > 内置默认值。

完整变量清单见仓库根目录 [.env.example](../../.env.example)。常用项：

| 环境变量 | 对应 YAML 字段 | 说明 |
| --- | --- | --- |
| `DB_ENGINE` | `db.engine` | `postgres` 或 `sqlite` |
| `DB_SQLITE_FILE` | `db.sqliteFile` | SQLite 文件名 |
| `DB_POSTGRES_TYPE` | `db.postgresType` | `embedded`（默认）或 `external` |
| `DB_HOST` / `DB_PORT` | `db.postgresConfig.host/port` | 外部 PostgreSQL 连接地址 |
| `DB_USER` / `DB_PASSWORD` | `db.postgresConfig.user/password` | 数据库账号 |
| `DB_NAME` | `db.postgresConfig.database` | 数据库名 |
| `DB_SSLMODE` | `db.postgresConfig.ssl` | `require` 等开启 SSL，`disable` 关闭 |
| `DB_MAX_OPEN_CONNS` / `DB_MAX_IDLE_CONNS` | `db.postgresConfig.maxOpenConns/maxIdleConns` | 连接池大小 |
| `HTTP_HOST` | `httpHost` | 监听地址（HTTPS 与之共用，无需单独配置） |
| `JWT_SECRET` | `jwtSecret` | 为空时仍自动生成并写回 YAML |
| `CACHE_SIZE` | `cacheSize` | 缓存大小（字节） |
| `TRUSTED_ORIGINS` | `trustedOrigins` | 逗号分隔的跨源来源列表 |
| `STRM_VIDEO_EXT` / `STRM_META_EXT` | `strm.videoExt` / `strm.metaExt` | 逗号分隔的扩展名列表 |
| `STRM_MIN_VIDEO_SIZE` | `strm.minVideoSize` | 最小视频大小（MB） |
| `STRM_CRON` | `strm.cron` | STRM 定时 cron |
| `LOG_LEVEL` / `LOG_MAX_SIZE_MB` / `LOG_MAX_BACKUPS` / `LOG_MAX_AGE_DAYS` | `log.level` 等 | 日志配置 |
| `AUTH_SERVER` / `NEW_AUTH_SERVER` | `authServer` / `newAuthServer` | 认证服务地址 |
| `BAIDUPAN_APP_ID` | `baiDuPanAppId` | 百度网盘应用标识 |
| `EMBY302_INSECURE_SKIP_VERIFY` | `emby302.insecure_skip_verify` | `true` / `1` 开启跳过证书校验 |

首次启动时程序直接以默认值加环境变量生成 `config/config.yaml` 并启动（内嵌 PostgreSQL 单容器零配置）；需要旧式 Web 配置向导时设置 `QMS_SETUP_WIZARD=1`。

## 115 运行参数

- 首页「115 接口监控」的请求数、QPS、QPM、QPH、平均响应时间和限流次数来自 `request_stats` 表，重启后仍按时间窗口聚合展示。
- 当前是否限流、等待时间和剩余时间来自进程内 115 请求队列管理器。限流暂停时长为 1 分钟，重启后恢复为未限流。
- 秒传等待策略保存于 `settings`，由 `upload_rapid_wait_interval_seconds`、`upload_rapid_wait_timeout_seconds`、`upload_rapid_wait_min_size`、`upload_rapid_wait_force_size` 和 `upload_rapid_wait_skip_upload` 控制。间隔只控制重试频率，超时字段才是最大等待上限。
- 115 直链缓存有效性检查保存于 `settings`，默认开启，默认总超时为 3 秒、范围为 1 到 9 秒。它只影响缓存 URL 的 HEAD 检查；百度网盘和 OpenList 不使用这套机制。
- 上传协议、目录监控、断点续传、远端已存在和 STRM 后处理的状态边界见 [上传与 STRM 处理](../architecture/upload-and-strm-processing.md)。

## Emby 302 出站 HTTPS

Emby 302 代理访问 Emby、OpenList、m3u8 和下载资源时默认校验证书，并复用共享 HTTP client 的空闲连接。仅在受控内网自签名证书或临时排障场景下，才设置：

```yaml
emby302:
  insecure_skip_verify: true
```

启用 `emby302.insecure_skip_verify` 后，出站 HTTPS 请求会接受无法验证的证书，程序会写入风险提示日志。该模式存在中间人攻击风险，不适合公网或长期生产环境。

## 日志行为与脱敏

日志路径由 `config/config.yaml` 的 `log` 配置决定，默认相对于配置目录：

| 配置项 | 默认值 | 用途 |
| --- | --- | --- |
| `log.level` | `info` | 可选 `debug`、`info`、`warn`、`error` |
| `log.maxSizeMB` | `10` | 单个轮转日志最大大小，单位 MB，范围 1 到 1024 |
| `log.maxBackups` | `3` | 每个日志的轮转备份数，范围 1 到 100 |
| `log.maxAgeDays` | `7` | 轮转备份最长保留天数，范围 1 到 365 |
| `log.app` | `logs/app.log` | 主程序日志 |
| `log.v115` | `logs/115.log` | 115 请求和队列日志 |
| `log.openList` | `logs/openList.log` | OpenList 日志 |
| `log.tmdb` | `logs/tmdb.log` | TMDB 日志 |
| `log.baiduPan` | `logs/baidupan.log` | 百度网盘日志 |
| `log.web` | `logs/web.log` | 预留 Web 日志配置 |
| `log.syncLogDir` | `logs/sync` | 同步任务独立日志目录 |

历史 `log.file` 仍可读取；当 `log.app` 为空且 `log.file` 有值时使用旧路径，新保存统一写 `log.app`。全局日志按写入触发轮转并压缩旧文件；同步任务日志不轮转，随同步记录清理删除。`QLogger` 在写入前脱敏 `api_key`、Token、Cookie、密码、STS 密钥等常见敏感字段，脱敏值统一显示为 `******`。

`QMS_UNSAFE_SENSITIVE_LOG=1` 只在本地调试时临时启用 `SensitiveDebug` 日志；它可能写出 API Key、Token、Cookie 或密码，不能在生产环境长期使用或分享相关日志。`backend/emby302.yaml` 默认关闭 ANSI 颜色，避免控制字符进入日志。

## 第三方密钥与本机敏感数据

- 115 开放平台 APP ID、TMDB API Key / Access Token、OpenAI 兼容 API Key 和 fanart.tv API Key 可以在 Web 设置中配置。
- 默认密钥也可由 `backend/main.go` 的变量、ldflags 或环境变量 / `config/.env` 注入。`FANART_API_KEY`、`TMDB_API_KEY`、`TMDB_ACCESS_TOKEN` 和 `SC_API_KEY` 的优先级是 Web UI > 环境变量 / `config/.env` > ldflags；`config/.env` 覆盖真实环境变量。
- 两步验证等本机敏感数据使用首次启动自动生成的 `config/encryption.key`。`jwtSecret` 为空或仍为公开默认值时会生成 32 字节随机密钥并写回配置；修改它会使现有登录 Cookie 失效。
- OAuth 中转使用 `OAUTH_RELAY_ENCRYPTION_KEY`，可由 `main.OAuthRelayEncryptionKey` ldflags 或环境变量 / `config/.env` 注入，环境变量优先。

浏览器 Cookie、CSRF、初始化管理员、API Key 和可信来源等安全契约见 [认证会话](../architecture/authentication-sessions.md)。
