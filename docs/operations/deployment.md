# 部署与持久化

> 职责：说明 QMediaSync 的 Docker、发布二进制和飞牛应用部署方式，以及运行数据的持久化边界。
>
> 权威范围：本文档维护运行环境、镜像、挂载目录、端口暴露和部署身份；应用配置、密钥与日志见 [配置、密钥与日志](configuration.md)，数据库维护见 [数据库运维](database.md)，反向代理见 [反向代理与 SSE](reverse-proxy.md)。
>
> 修改时机：修改 Dockerfile、容器入口脚本、二进制运行目录、飞牛安装流程、运行端口或持久化目录时必须更新本文档。
>
> 相关代码：`docker/`、`backend/main.go`、`backend/FNOS/`、`scripts/install/linux-init.sh`、`.github/workflows/release.yaml`。

## 部署选择与持久化边界

| 方式 | 适用场景 | 运行数据位置 |
| --- | --- | --- |
| Docker 镜像 | Linux 主机、NAS 或容器平台 | 容器内 `/app/config`，必须挂载到宿主机 |
| 发布二进制 | Windows 或直接管理 Linux 进程 | 可执行文件同级的 `config/` |
| 飞牛 FPK | 飞牛系统 | 平台的应用共享目录下 `config/`，由安装向导和 `TRIM_*` 环境变量管理 |

配置文件、SQLite 数据库、内嵌 PostgreSQL 数据、备份、日志、本机加密密钥和用户设置都依赖配置目录。升级、迁移或重建容器前必须备份并保留该目录；不能只保留可执行文件或镜像层。

默认端口为 `12333`，Web 管理界面与 Emby 302 代理共用该端口（单端口架构）。Docker 和发布二进制部署中，主程序只有在运行目录 `config/server.crt` 和 `config/server.key` 都存在时才在同一端口上同时提供 HTTP 与 HTTPS 服务（按连接自动分诊，无需额外端口）；无证书时仅提供 HTTP。端口、证书和代理层细节分别见 [配置、密钥与日志](configuration.md) 与 [反向代理与 SSE](reverse-proxy.md)。

## Docker

正式发布镜像为 `afengj/diy-strm:latest`（DockerHub）与 `ghcr.io/daxiong319/qmediasync:latest`（GHCR），同时提供 `linux/amd64` 和 `linux/arm64`。固定版本使用 `afengj/diy-strm:<tag>` / `ghcr.io/daxiong319/qmediasync:<tag>`；`beta` 和功能分支镜像的生成规则见 [发布流程](release.md)。

默认使用内嵌 PostgreSQL（embedded 模式），单容器即可运行，无需外部数据库依赖。以下示例把全部运行状态保存到宿主机的 `./config`，并按需给应用挂载媒体目录：

```bash
mkdir -p config media

# 方式一：零配置（默认内嵌 PostgreSQL，开箱即用）
docker run -d \
  --name qmediasync \
  --restart unless-stopped \
  -p 12333:12333 \
  -v "$(pwd)/config:/app/config" \
  -v "$(pwd)/media:/media" \
  ghcr.io/daxiong319/qmediasync:latest
```

```bash
# 方式二：config/.env 配置（仿 tgto123 风格，纯 .env 驱动）
# 复制 .env.example 为 config/.env 后修改，启动时自动生成 config.yaml（可选，纯 .env 驱动）
cp .env.example config/.env
docker run -d \
  --name qmediasync \
  --restart unless-stopped \
  -p 12333:12333 \
  -v "$(pwd)/config:/app/config" \
  -v "$(pwd)/media:/media" \
  ghcr.io/daxiong319/qmediasync:latest
```

DockerHub 用户也可以把镜像名替换为 `afengj/diy-strm:latest`，两者构建自同一源码。首次运行没有 `config/config.yaml` 时，程序直接用默认配置（内嵌 PostgreSQL）加环境变量生成配置并启动，无需访问 Web；需要旧式配置向导时设置环境变量 `QMS_SETUP_WIZARD=1`。使用内置 HTTPS 时，将证书文件放入已挂载的 `config/` 目录（`server.crt` / `server.key`）即可，HTTP 与 HTTPS 共用 `12333` 端口，无需额外端口映射。

容器入口脚本以 root 完成初始目录检查；可选环境变量 `GUID`、`GPID` 为数值 UID/GID。设置后脚本会在容器内创建对应用户或组（如不存在），并在值变化时递归修正 `/app/config` 的所有者，再以 `GUID` 运行主进程。例如：

```bash
docker run -d \
  --name qmediasync \
  --restart unless-stopped \
  -e GUID="$(id -u)" \
  -e GPID="$(id -g)" \
  -p 12333:12333 \
  -v "$(pwd)/config:/app/config" \
  -v "/srv/media:/media" \
  ghcr.io/daxiong319/qmediasync:latest
```

该所有权修正不覆盖 `/media`；宿主机媒体目录的读写权限仍由部署者自行保证。不要用 `--user` 替代上述入口逻辑，否则入口无法创建用户或修正持久化目录的权限。

从当前源码构建镜像使用根目录 `Dockerfile`（前端 pnpm 构建 + 后端 Go 编译，产物与 CI 一致）：

```bash
docker build -t qmediasync .
```

`docker/source.Dockerfile` 与 `docker/source.local.Dockerfile` 是上游旧目录布局的遗留文件，当前仓库不使用；`docker/source.local.Dockerfile` 仅用于本地网络环境替换构建镜像源。

## 发布二进制与 systemd

发布包解压后，从包含 `QMediaSync` 和 `web_statics/` 的目录启动程序。Linux 与 Windows 都把运行配置保存在可执行文件同级的 `config/`；因此替换程序和静态资源时不得覆盖该目录。

`scripts/install/linux-init.sh` 是 Linux 上的外部 PostgreSQL 与 systemd 辅助脚本：它可安装或初始化 PostgreSQL、创建数据库和用户、写入 `/etc/qmediasync/postgres.env`，并用 `-i` 创建 `qmediasync.service`。脚本要求在发布二进制所在目录运行，并要求 root 与 systemd；它不是 Docker 或飞牛的安装入口。

```bash
sudo scripts/install/linux-init.sh -i
systemctl status qmediasync
```

脚本创建的服务从当前目录执行 `QMediaSync`，并从 `/etc/qmediasync/postgres.env` 读取旧式 PostgreSQL 环境变量。新实例默认使用内嵌 PostgreSQL 零配置启动，也可通过 `config/config.yaml` 确认数据库模式和连接信息；配置文件是当前运行时的权威来源。

## 飞牛 FPK

飞牛 FPK 由发布流程生成；应用安装向导负责选择 SQLite 或外部 PostgreSQL 并写入配置。飞牛运行时由平台注入 `TRIM_APPDEST`、`TRIM_PKGETC`、`TRIM_DATA_SHARE_PATHS` 等路径变量，程序会将实际配置目录迁移或定位到共享数据目录下的 `config/`。

不要把 Docker 的 `/app/config` 路径、`GUID`/`GPID` 约定或裸机 systemd 服务直接套用到飞牛安装；在飞牛文件管理器中保留应用共享目录下的 `config/`，再按 [数据库运维](database.md) 执行备份和恢复。
