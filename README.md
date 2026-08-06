# DIY-STRM (QMediaSync 融合版)

DIY-STRM 是一个媒体同步和刮削系统，用于管理 115 网盘、百度网盘、OpenList 等云存储与 Emby 媒体服务器之间的文件同步、STRM 生成和媒体刮削等流程。

本仓库在 [qicfan/qmediasync](https://github.com/qicfan/qmediasync)（v0.14.23）基础上，融合了以下二改项目的全部改进（v0.15.x）：

- [chen8945/QMediaSync](https://github.com/chen8945/QMediaSync)（v0.15.13）：STRM Webhook、目录监控 115 上传、断点续传分片上传、SSE 实时推送、Cookie 会话/CSRF/TOTP 两步验证、Emby 增量同步、日志轮转等
- [rong28694/qmediasync-fixed](https://github.com/rong28694/qmediasync-fixed)：季集解析支持 4 位集数（S01E0001~E9999）

## 主要新增特性

- **STRM Webhook**：`POST /api/strm/webhook`（API Key 鉴权），支持 file / batch_files / directory_scan 三种动作，外部程序可触发 STRM 生成
- **目录监控 115 上传**：fsnotify/polling/auto 监控模式，稳定性队列、断点续传、源文件清理
- **安全加固**：Cookie 会话 + CSRF、可撤销登录会话、TOTP 两步验证、登录设备管理、日志脱敏
- **SSE 实时推送**：替代 WebSocket，结构化事件 + 共享日志 tailer
- **Emby 增强**：增量同步、刷新任务合并、每日首次全量同步、Webhook 单条同步
- **UI 深色侧边栏**：参考 AutoFilm WebUI 风格定制

## Docker 镜像

本地构建：

```bash
docker build -t diy-strm:latest .
# 或
docker compose up -d
```

## 文档

完整文档见 [文档索引](docs/README.md)。

## 原项目地址

- 上游：[qicfan/qmediasync](https://github.com/qicfan/qmediasync)
- 前端：[qicfan/q115-strm-frontend](https://github.com/qicfan/q115-strm-frontend)
- Wiki：[qicfan/qmediasync/wiki](https://github.com/qicfan/qmediasync/wiki)
