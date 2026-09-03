# DIY-STRM (QMediaSync 融合版)

[![Docker Hub](https://img.shields.io/docker/pulls/afengj/diy-strm?style=flat-square)](https://hub.docker.com/r/afengj/diy-strm)
[![GitHub Release](https://img.shields.io/github/v/release/daxiong319/diy-strm?style=flat-square)](https://github.com/daxiong319/diy-strm/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/daxiong319/diy-strm?style=flat-square)](https://goreportcard.com/report/github.com/daxiong319/diy-strm)

DIY-STRM 是一个媒体同步与刮削系统，管理 115 云盘、123 云盘、移动云盘、百度网盘、OpenList、光翼等云存储与 Emby 媒体服务器之间的文件同步、STRM 生成和媒体刮削等流程。

本仓库在 [qicfan/qmediasync](https://github.com/qicfan/qmediasync)（v0.14.23）基础上，融合了以下二改项目的全部改进（v0.15.x）：

- [chen8945/QMediaSync](https://github.com/chen8945/QMediaSync)（v0.15.13）：STRM Webhook、目录监控 115 上传、断点续传分片上传、SSE 实时推送、Cookie 会话/CSRF/TOTP 两步验证、Emby 增量同步、日志轮转等
- [rong28694/qmediasync-fixed](https://github.com/rong28694/qmediasync-fixed)：季集解析支持 4 位集数（S01E0001~E9999）

## 🚀 快速部署

无需外部数据库，单容器开箱即用（内嵌 PostgreSQL）：

```bash
# 零配置启动（嵌入式 PG，单容器无外部依赖）
mkdir -p config media
docker run -d \
  --name diy-strm \
  --restart unless-stopped \
  -p 12333:12333 \
  -v "$(pwd)/config:/app/config" \
  -v "$(pwd)/media:/media" \
  afengj/diy-strm:latest
```

启动后访问 `http://服务器IP:12333` 进入 Web 管理界面。所有运行数据持久化在 `./config`，容器重建不丢失。

> **单端口架构**：Web 管理界面与 Emby 302 代理共用 `12333` 一个端口。Emby 播放器/反代地址填 `http://服务器IP:12333` 即可；将证书文件放入 `config/server.crt` 与 `config/server.key` 后重启容器，同一端口同时支持 HTTPS。

### 通过 .env 自定义配置（可选）

复制 `.env.example` 为 `config/.env`，按需修改后重启容器：

```bash
cp .env.example config/.env
# 编辑 config/.env 修改配置项

docker run -d \
  --name diy-strm \
  --restart unless-stopped \
  -p 12333:12333 \
  -v "$(pwd)/config:/app/config" \
  -v "$(pwd)/media:/media" \
  --env-file ./config/.env \
  afengj/diy-strm:latest
```

环境变量覆盖 `config.yaml` 中的所有配置项，优先级：`.env` / 环境变量 > `config.yaml` > 内置默认值。完整变量清单见 [.env.example](.env.example)。

### 使用 docker compose

```yaml
services:
  diy-strm:
    image: afengj/diy-strm:latest
    container_name: diy-strm
    restart: unless-stopped
    ports:
      - "12333:12333"  # Web 管理界面 + Emby 302 代理 (单端口; 配置证书后同端口支持 https)
    volumes:
      - ./config:/app/config
      - ./media:/media
    # env_file:  # 可选，.env 自定义配置
    #   - ./config/.env
```

## ✨ 主要特性

- **STRM Webhook**：`POST /api/strm/webhook`（API Key 鉴权），支持 file / batch_files / directory_scan 三种动作，外部程序可触发 STRM 生成
- **目录监控 115 上传**：fsnotify/polling/auto 监控模式，稳定性队列、断点续传、源文件清理
- **安全加固**：Cookie 会话 + CSRF、可撤销登录会话、TOTP 两步验证、登录设备管理、日志脱敏
- **SSE 实时推送**：替代 WebSocket，结构化事件 + 共享日志 tailer
- **Emby 增强**：增量同步、刷新任务合并、每日首次全量同步、Webhook 单条同步
- **UI 深色侧边栏**：参考 AutoFilm WebUI 风格定制

## 🎬 影巢（HDHive）使用说明

### 订阅默认参数模板（影巢设置 → 订阅默认参数模板）

新建影片级订阅时，弹窗里**留空的项**会自动套用默认模板（电影 / 电视剧各一套），本次填写的项以本次为准。模板是一段 JSON，点「填入默认」可先载入示例再改：

| 字段 | 含义 | 示例 |
|---|---|---|
| `resolution` | 分辨率限制（空=不限） | `"1080p"` / `"4k"` |
| `effect` | 特效版本（空=不限） | `"默认"` / `"杜比视界"` |
| `search_sources` | 搜索渠道列表 | `["symedia","nanshare","pansou","official"]` |
| `include_regex` | 标题包含正则（命中才转存） | `""` |
| `exclude_regex` | 标题排除正则（命中则跳过） | `""` |
| `target_path` | 网盘内默认保存目录 | `"/影视/待整理"` |
| `media_server` | 媒体库实例（空=全部） | `""` |

示例（电影模板，1080p 起、全渠道搜索）：

```json
{
  "resolution": "1080p",
  "effect": "默认",
  "search_sources": ["symedia", "nanshare", "pansou", "official"],
  "include_regex": "",
  "exclude_regex": "",
  "target_path": "/影视/待整理",
  "media_server": ""
}
```

填写后点击保存即生效；以后新建订阅时弹窗中留空的分辨率 / 渠道 / 目录等会自动套用。

### 订阅封面

- 新订阅创建时会自动带上 TMDb 海报；历史订阅无封面时，可在「资源订阅」页点右上角 **补全封面** 按 TMDb 批量回填（订阅列表中刷新时也会自动异步补全缺失海报）。
- 海报图片默认使用 `image.tmdb.org`，国内网络可能加载缓慢或失败 —— 可在「系统设置 → 刮削设置」的 **TMDb 图片地址** 换成可达的图片镜像域名，刷新页面即可生效。

### 频道订阅回溯搜索（123 / 光鸭 / 移动云盘）

频道订阅默认是**增量追新**：只匹配订阅之后发布的帖子（处理过的帖子不会重复查找）。如果某部影片的资源帖发布在订阅之前，会搜不到。

此类场景请在该订阅中开启 **「回溯搜索」** 开关：执行时忽略频道游标，从频道最近历史帖里全文匹配并转存；已转存过的影片 / 链接自动去重，重复执行安全，且回溯不会推进频道游标、不影响其它订阅的追新。注意 t.me 预览页可见的历史帖数量有限，太老的帖子无法回溯。

## 📖 文档

完整文档见 [docs/README.md](docs/README.md)，包括：

- [部署与持久化](docs/operations/deployment.md) — Docker 零配置、.env 配置、外部数据库、媒体目录挂载
- [配置、密钥与日志](docs/operations/configuration.md) — 环境变量、第三方密钥、日志行为
- [数据库运维](docs/operations/database.md) — 初始化、备份、恢复、清库
- [反向代理与 SSE](docs/operations/reverse-proxy.md) — Nginx/Caddy 反代配置
- [认证与浏览器会话](docs/architecture/authentication-sessions.md) — 登录、API Key、两步验证

## 🏗️ 本地构建

```bash
docker build -t diy-strm:latest .
# 或直接
docker compose up -d
```

## 📚 原项目地址

- 上游：[qicfan/qmediasync](https://github.com/qicfan/qmediasync)
- 前端：[qicfan/q115-strm-frontend](https://github.com/qicfan/q115-strm-frontend)
- Wiki：[qicfan/qmediasync/wiki](https://github.com/qicfan/qmediasync/wiki)
