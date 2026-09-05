# 媒体栈恢复指南（重装服务器后快速拉起）

服务器 `134.185.85.200`（aarch64/ARM64）。本模板覆盖 diy-strm 全链路：
**TG 转存（tgto123）→ PT 下载（MoviePilot v2 + qBittorrent）→ 云盘（alist/openlist/clouddrive2）→ diy-strm 上传整理（外置 postgres）→ STRM → Emby 播放（302 代理）**。

## 一、重装前：必须备份的目录（数据全在 bind mount 里）

```bash
tar -czf /tmp/media-stack-backup.tar.gz \
  /opt/diy-strm/config \                    # diy-strm 应用配置 + 内置数据库（单容器模式全在这）
  /media/moviepilot-v2/config \             # MoviePilot 配置
  /media/moviepilot-v2/BT_backup \          # MP 种子备份
  /media/mp-postgresql \                    # MP 数据库
  /media/redis/data \                       # MP Redis
  /media/qbittorrent/config \               # qBittorrent 配置（含做种任务）
  /media/QMediaSync/strm \                  # STRM 文件（可再生，备份仅为省时间）
  /media/embytest/config \                  # Emby 配置与元数据
  /media/alist/data \                       # Alist 配置与数据库
  /opt/1panel/apps/openlist/openlist/data \ # OpenList 配置
  /media/clouddrive2/Config \               # CloudDrive2 配置
  /media/tgto123/db                         # tgto123 数据库
```

> 若使用外部 PostgreSQL 模式，额外备份 `/home/diy-strm/postgres`（diy-strm 数据库）。
> 建议用 diy-strm 内置备份功能（`backup_config`/`backup_record` 表对应的后台备份）定期备份 diy-strm 配置与数据库。

## 二、重装后：恢复目录 + 拉起

```bash
# 1. 恢复目录结构（把备份解包回原路径，保持绝对路径不变）
tar -xzf media-stack-backup.tar.gz -C /

# 2. 放置模板与 env
mkdir -p /opt/media-stack && cd /opt/media-stack
# 上传 deploy/docker-compose.media-stack.yml 与 .env（照 media-stack.env.example 填真实值）

# 3. 启动（MP 的数据库与应用已用 depends_on 排序）
docker compose -f docker-compose.media-stack.yml --env-file .env up -d
```

## 二点五、diy-strm 数据库模式说明（单容器 vs 外置 PostgreSQL）

diy-strm 支持三种数据库模式（`config.yaml` 的 `db.engine`，环境变量 `DB_ENGINE` 可覆盖）：

| 模式 | 说明 | 适用 |
|---|---|---|
| `sqlite`（默认） | 单文件库，落在 `/app/config/qmediasync.db` | **推荐**：单容器即跑，备份只需 `/app/config` |
| `postgres` + `embedded` | 容器内嵌 PostgreSQL，数据在 `/app/config/postgres/data` | 需要 PG 特性且保持单容器 |
| `postgres` + `external` | 连独立 postgres 容器（当前 134 服务器在用） | 已有外部库/多实例共享 |

**新装机推荐 sqlite（什么都不用改，默认即是）**；想切到外部 PG 时，启用 compose 里注释的 `diy-strm-postgres` 服务，并在 `config.yaml` 设 `db.engine: postgres`、`db.postgresType: external`、`host: diy-strm-postgres`（或用 `DB_*` 环境变量覆盖）。模板默认按单容器（sqlite）编排。

## 三、启动后核对清单（按依赖顺序）

1. **qBittorrent**：`http://IP:8080`，恢复的配置含分类/保存路径 `/downloads`
2. **MoviePilot**：`http://IP:3000`，登录后检查 下载器(qB)、媒体存储路径映射（MP 侧 `/downloads` ↔ 宿主 `/media/qbittorrent/下载`）、站点 Cookie 是否还有效
3. **alist/openlist/clouddrive2**：分别 `5243 / 5244 / 19798`，检查云盘存储 token 是否仍有效（过期需重新授权）
4. **diy-strm**：`http://IP:12333`
   - MoviePilot 设置：地址 `http://IP:3000`（或容器名 `http://moviepilot:3000`，同在 media-net 网络）、API Token
   - 下载根目录：MP 侧 `/downloads`；本地视图根 `/downloads`（与容器挂载一致）
   - 上传账号/STRM 输出目录等均从恢复的 postgres/配置自动带出
5. **Emby**：`http://IP:8099`，媒体库路径 `/media`（容器内=宿主 `/media/QMediaSync/strm`）；diy-strm 的 Emby 302 代理地址 `http://IP:8099`
6. **tgto123**：host 网络，Web 端口见其配置；bot token 从 env 注入

## 四、路径对照表（容器内 ↔ 宿主机）

| 用途 | 容器内路径 | 宿主机路径 |
|---|---|---|
| PT 下载目录 | `/downloads`（MP/qb/diy-strm 一致） | `/media/qbittorrent/下载` |
| STRM 输出（diy-strm 容器） | `/media` | `/media/QMediaSync/strm` |
| STRM 读取（Emby 容器） | `/media` | `/media/QMediaSync/strm` |
| MP 媒体根 | `/media` | `/media` |

> 关键点：**diy-strm 与 Emby 必须把同一个宿主目录挂到相同的容器内路径**，STRM 里的路径才能两端一致。

## 五、与现有部署的差异说明

- 现服务器各容器分散在多个 compose 项目（`/media`、`/media/embytest`、`/media/alist`、1Panel 等）且网络不同（media_default/embytest_default/alist_default）。本模板**统一到 `media-net` 网络**，diy-strm 与 MoviePilot 可用容器名互通（如 MP 地址填 `http://moviepilot:3000`）。
- MP 的 postgres/redis 服务名保持 `postgresql`/`redis`，与 MP 官方 compose 环境变量一致。
- qBittorrent/clouddrive2/tgto123 保持 host 网络（与现状一致，qB 的下载器回连、CloudNAS fuse 挂载、tgto123 bot 都依赖）。
- openlist 原由 1Panel 管理；不用 1Panel 时把数据目录改到 `/media/openlist/data` 并恢复备份，存储配置需在 Web 里重新验证。
- Emby 用 `amilys/embyserver_arm64v8:4.8.10.0`（服务器是 ARM64，此为增强版带解码）；x86 服务器换成 `amilys/embyserver:4.8.10.0`。
- 独立工具（komari 监控、new-api、sub2api、rose、audiobookshelf、CLIProxyAPI、frps、1Panel 等）与媒体链路无关，不在本模板内，按需从各自 compose（`/home/docker/...`、`/opt/1panel/...`）单独恢复。
