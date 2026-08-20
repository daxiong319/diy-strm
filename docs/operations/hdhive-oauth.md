# 影巢（HDHive）OAuth 授权与每日签到

diy-strm 内置影巢（HDHive）资源订阅能力，支持两种接入方式，可同时使用：

| 方式 | 认证 | 用途 | 入口 |
|---|---|---|---|
| Open API（API Key） | 影巢开放平台申请的 `X-API-Key` | 按 TMDB 订阅资源：查询 → 规格筛选 → 解锁 → 转存 | [影巢设置](/cloud-hdhive/settings) |
| OAuth 授权（推荐） | TgtoDrive 影巢授权通道（install_id + HMAC-SHA256 签名） | OAuth 授权、每日签到（普通/赌狗）、用户快照、子账号管理 | [授权签到](/cloud-hdhive/oauth) |

## OAuth 授权

diy-strm 的 OAuth 授权走 **TgtoDrive 影巢代理服务**（`https://hdhive-open.tgtodrive.top`，与 tgto123 同款通道），无需自行申请 API Key：

1. 打开「影巢」菜单 →「授权签到」页面。
2. 点击「前往授权」（子账号点击行内「授权」），浏览器将打开影巢授权页。
3. 使用影巢账号完成授权后，回到页面点击「刷新状态」，即可看到账号昵称、等级、积分、周免费额度、分享数等信息。

> 未授权时 `me` / `token_status` 接口返回 `REAUTH_REQUIRED`，前端会自动附带可点击的授权链接。

## 每日签到

签到支持两种模式，可在页面切换，定时任务默认使用环境变量 `ENV_HDHIVE_CHECKIN_TYPE` 指定的模式（`1/true/yes/on/gamble/gambler` 为赌狗，其余为普通）：

| 模式 | API 请求 | 说明 |
|---|---|---|
| 普通签到 | `POST /api/checkin`（无 body） | 常规签到得固定积分 |
| 赌狗签到 | `POST /api/checkin`（`{"is_gambler": true}`） | 收益更高但有失败风险 |

### 手动签到

- 主账号：授权签到页选择模式后点「立即签到」。
- 单个子账号：子账号列表行内「签到」。
- 全部账号：页面顶部「全部签到」（主账号 + 全部启用中的子账号）。

签到结果（时间、成功与否、服务端返回消息）会保存在对应账号记录中，并在页面展示。

### 自动签到

系统每天 **08:00（Asia/Shanghai）** 自动执行一次全场签到（主账号 + 全部启用中的子账号），无需人工干预。日志中可看到 `影巢定时签到` 相关记录。

## 子账号管理

子账号即独立的影巢账号，每个子账号拥有独立的 `install_id` 和独立的 OAuth 授权：

- **新增**：设置页点击「新增子账号」（标签留空自动命名为「小号 N」）。
- **授权**：新子账号需逐个「授权」完成 OAuth 流程。
- **状态**：行内「刷新」调用 `me` + `token_status` 更新授权状态与用户信息。
- **签到**：行内「签到」或顶部「全部签到」。
- **启停**：关闭「启用」开关后，自动签到会跳过该子账号。
- **删除**：主账号不允许删除，仅子账号可删除（删除后不可恢复）。

## 技术实现

- 签名算法：`HMAC-SHA256`，canonical 串为 7 行（method、规范路径、规范查询串、install_id、时间戳、nonce、body SHA-256），请求头 `X-Install-Id` / `X-Timestamp` / `X-Nonce` / `X-Signature`。
- 共享密钥：内置默认密钥（来自 tgto123 兼容通道），可通过环境变量 `HDHIVE_APP_SHARED_SECRET` 覆盖。
- 接口地址：默认 `https://hdhive-open.tgtodrive.top`，可通过环境变量 `HDHIVE_USER_SERVER_BASE_URL` 覆盖。
- 授权 URL：`GET /auth/start?install_id=...&ts=...&nonce=...&sig=...`（带签名）。

### 相关环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `HDHIVE_USER_SERVER_BASE_URL` | `https://hdhive-open.tgtodrive.top` | 影巢 OAuth API 地址 |
| `HDHIVE_APP_SHARED_SECRET` | 内置默认密钥 | HMAC 签名共享密钥 |
| `ENV_HDHIVE_CHECKIN_TYPE` | `0` | 自动签到模式：赌狗（`1/true/yes/on/gamble/gambler`）/ 普通（其余） |

### 数据库

- 表 `hive_oauth_accounts` 存储主账号与子账号（含 install_id、授权状态、用户快照、签到记录）。
- 数据库迁移版本 `70` 自动创建该表（既有库升级时自动补齐）。

## 相关 API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/cloud/hive/oauth/status` | 主账号授权状态 + 用户快照 + 授权 URL（未授权时） |
| POST | `/api/cloud/hive/oauth/refresh` | 刷新主账号授权状态 |
| POST | `/api/cloud/hive/oauth/auth-url` | 生成主账号授权 URL |
| POST | `/api/cloud/hive/oauth/checkin` | 主账号签到（`{mode: daily\|gamble}`） |
| POST | `/api/cloud/hive/oauth/checkin-all` | 主账号 + 全部启用子账号签到 |
| GET | `/api/cloud/hive/sub-accounts` | 子账号列表 |
| POST | `/api/cloud/hive/sub-accounts` | 新增子账号（`{label}`） |
| PUT | `/api/cloud/hive/sub-accounts/:id` | 更新子账号（`{label, enabled}`） |
| DELETE | `/api/cloud/hive/sub-accounts/:id` | 删除子账号 |
| POST | `/api/cloud/hive/sub-accounts/:id/auth-url` | 子账号授权 URL |
| POST | `/api/cloud/hive/sub-accounts/:id/refresh` | 刷新子账号状态 |
| POST | `/api/cloud/hive/sub-accounts/:id/checkin` | 子账号签到 |

## 常见问题

**授权后仍提示 REAUTH_REQUIRED**：授权是异步完成的，请稍候 5~10 秒后点「刷新状态」；若仍失败，重新生成授权链接并确认在浏览器中完成了授权。

**签到提示「未授权」**：该账号（主账号或子账号）未完成 OAuth 授权，先到授权签到页完成授权再签到。

**赌狗签到失败**：赌狗模式有概率失败属正常现象，可切换回普通模式保证稳定获取积分。