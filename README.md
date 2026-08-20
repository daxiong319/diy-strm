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
  -p 8095:8095 \
  -v "$(pwd)/config:/app/config" \
  -v "$(pwd)/media:/media" \
  afengj/diy-strm:latest
```

启动后访问 `http://服务器IP:12333` 进入 Web 管理界面。所有运行数据持久化在 `./config`，容器重建不丢失。

### 通过 .env 自定义配置（可选）

复制 `.env.example` 为 `config/.env`，按需修改后重启容器：

```bash
cp .env.example config/.env
# 编辑 config/.env 修改配置项

docker run -d \
  --name diy-strm \
  --restart unless-stopped \
  -p 12333:12333 \
  -p 8095:8095 \
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
      - "12333:12333"  # Web 管理界面
      - "8095:8095"    # Emby 302 代理
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
