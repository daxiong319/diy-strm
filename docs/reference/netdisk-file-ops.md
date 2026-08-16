# 网盘文件操作 API

> 职责：定义网盘文件浏览与操作接口的路径、字段、验证和响应契约，包括列表、创建目录、删除、重命名、移动、命名对齐和目录整理。
>
> 权威范围：本文档维护 `/api/path/*`、`/api/files/name-align/*` 与 `/api/organize/*` 的 HTTP 契约；文件列表缓存、STRM 生成和刮削整理见 [STRM 同步调度与任务记录](../architecture/sync-orchestration.md) 与 [上传与 STRM 处理](../architecture/upload-and-strm-processing.md)。
>
> 修改时机：修改上述路由、请求字段、验证、缓存失效或响应契约时必须更新本文档。
>
> 相关代码：`backend/internal/controllers/path.go`、`backend/internal/controllers/path_ops.go`、`backend/internal/controllers/name_align.go`、`backend/internal/controllers/batch_rename.go`、`backend/internal/renamerule/rule.go`、`backend/internal/controllers/net_file_batch.go`、`backend/internal/controllers/organize.go`、`backend/internal/controllers/crosstransfer.go`、`backend/internal/requests/operations.go`、`backend/internal/requests/batch_rename.go`、`frontend/src/components/AppFileManager.vue`、`frontend/src/components/NameAlignDialog.vue`、`frontend/src/components/BatchRenameDialog.vue`、`frontend/src/components/OrganizeDialog.vue`、`frontend/src/components/CrossTransferDialog.vue`。

文件操作接口位于受保护的 `/api` 路由组（JWT / Cookie 会话需通过 CSRF，API Key 可用 `X-API-Key` 或 `api_key` 查询参数）。认证边界见 [认证与浏览器会话](../architecture/authentication-sessions.md)。

## 接口总览

| 动作 | 方法与路径 | 说明 |
| --- | --- | --- |
| 目录列表 | `GET /api/path/list` | 获取目录列表（仅目录），按 `source_type` 分发。 |
| 文件列表 | `GET /api/path/files` | 分页文件列表，带视图缓存。 |
| 创建目录 | `POST /api/path/create` | 创建目录，成功后失效父目录缓存。 |
| 删除 | `DELETE /api/path` | 删除文件或目录（通用），成功后失效源目录与被删路径树缓存。 |
| 重命名 | `POST /api/path/rename` | 重命名文件或目录。 |
| 移动 | `POST /api/path/move` | 移动文件或目录到目标目录。 |
| 命名对齐预览 | `POST /api/files/name-align/preview` | 解析文件名并生成规范化建议名，不改动文件。 |
| 命名对齐应用 | `POST /api/files/name-align/apply` | 按预览结果批量重命名，逐条执行并汇总。 |
| 批量重命名预览 | `POST /api/files/batch-rename/preview` | 按通用规则（12 种）计算新名称并返回校验错误，不改动文件。 |
| 批量重命名应用 | `POST /api/files/batch-rename/apply` | 按预览结果批量重命名，写入历史记录（支持回滚）。 |
| 批量重命名历史 | `GET /api/files/batch-rename/history` | 当前用户的历史记录（用于回滚）。 |
| 批量重命名回滚 | `POST /api/files/batch-rename/rollback` | 按历史记录还原原名（单项失败不中断）。 |
| 常用组合列表 | `GET /api/files/batch-rename/presets` | 当前用户的常用组合。 |
| 常用组合保存 | `POST /api/files/batch-rename/presets` | 保存常用组合（同名覆盖）。 |
| 常用组合删除 | `DELETE /api/files/batch-rename/presets` | 删除常用组合（`{ id }`）。 |
| 目录整理预览 | `POST /api/organize/preview` | 扫描目录并规划整理动作（建目录 + 移动 + 重命名），不改动文件。 |
| 目录整理应用 | `POST /api/organize/apply` | 按预览结果批量建目录、移动并重命名，逐条执行并汇总。 |
| 跨盘秒传扫描 | `POST /api/crosstransfer/scan` | 递归扫描源目录文件并提取指纹（SHA1/MD5）。 |
| 跨盘秒传执行 | `POST /api/crosstransfer/execute` | 按指纹秒传到目标网盘，未命中自动入队中转上传。 |
| 光鸭开发者配置保存 | `POST /api/guangya/developer-setting` | 保存光鸭开发者 client_id / client_secret（每账号一份）。 |
| 光鸭开发者配置查询 | `GET /api/guangya/developer-setting` | 查询开发者配置（secret 脱敏）。 |
| 光鸭开发者配置删除 | `DELETE /api/guangya/developer-setting` | 删除开发者配置。 |
| 接收 TOKEN 添加 | `POST /api/guangya/receiver-tokens` | 添加小号秒传接收 TOKEN。 |
| 接收 TOKEN 列表 | `GET /api/guangya/receiver-tokens` | 按账号列出接收 TOKEN。 |
| 接收 TOKEN 删除 | `DELETE /api/guangya/receiver-tokens/:id` | 删除接收 TOKEN。 |
| 小号秒传任务创建 | `POST /api/guangya/small-transfer` | 创建光鸭小号秒传任务并异步执行。 |
| 小号秒传任务列表 | `GET /api/guangya/small-transfer` | 按账号列出秒传任务。 |
| 小号秒传任务删除 | `DELETE /api/guangya/small-transfer/:id` | 删除已完成/失败任务。 |

## 标识语义

文件/目录标识 `file_id` 与文件列表中的 `id` 一致，按 `source_type` 区分：

- `115`、`123`：文件 ID（字符串数字）。
- `baidupan`、`openlist`：完整路径（如 `/Media/剧集/某某.S01E01.mkv`）。

## 重命名

`POST /api/path/rename`

```json
{
  "account_id": 12,
  "parent_id": "12345",
  "file_id": "67890",
  "new_name": "新名称.mkv"
}
```

| 字段 | 必填 | 规则 |
| --- | --- | --- |
| `account_id` | 是 | 正整数，必须指向已有账号。 |
| `parent_id` | 否 | 源父目录标识，用于缓存失效。 |
| `file_id` | 是 | 非空且不为 `"0"`。 |
| `new_name` | 是 | 不能为空、`.`、`..`，不能包含路径分隔符和控制字符。 |

- `baidupan`、`openlist` 的 `file_id` 为完整路径，后端自动拆分为目录与旧名。
- `pan139` 走 `/file/update` 接口（`{fileId, name}`）；`guangyapan` 走 `/userres/v1/file/rename` 接口（`{fileId, newName}`）。
- 成功响应 `code=200`；失败返回 `code=500` 与错误信息。

## 移动

`POST /api/path/move`

```json
{
  "account_id": 12,
  "parent_id": "12345",
  "file_id": "67890",
  "target_parent_id": "11111"
}
```

| 字段 | 必填 | 规则 |
| --- | --- | --- |
| `account_id` | 是 | 正整数。 |
| `parent_id` | 否 | 源父目录标识，用于缓存失效。 |
| `file_id` | 是 | 非空且不为 `"0"`。 |
| `target_parent_id` | 是 | 目标目录标识，非空且不为 `"0"`，不能等于 `file_id`。 |

- `pan139` 走 `/file/batchMove` 接口（`{fileIds, toParentFileId}`）；`guangyapan` 走 `/userres/v1/file/move_file` 接口（异步任务，轮询 `get_task_status` 2=成功）。
- 成功后源父目录与目标目录缓存均失效。

## 命名对齐

命名对齐用于把杂乱文件名收束为媒体端通用命名：剧集归一为 `标题 S01E01.mkv`（季缺失按第一季），电影归一为 `标题 (年份).mkv`。

### 预览

`POST /api/files/name-align/preview`

```json
{
  "account_id": 12,
  "parent_id": "12345",
  "media_title": "Better Call Saul",
  "media_type": "tvshow",
  "year": 0,
  "items": [{ "file_id": "67890", "name": "better.call.saul.S01E01.1080p.mkv" }]
}
```

| 字段 | 必填 | 规则 |
| --- | --- | --- |
| `account_id` | 是 | 正整数。 |
| `parent_id` | 否 | 当前目录标识，应用成功后用于缓存失效。 |
| `media_title` | 否 | 指定标题（建议从 TMDB 搜索选择）；留空时使用文件名解析出的标题。 |
| `media_type` | 否 | `tvshow`（默认）或 `movie`。 |
| `year` | 否 | 电影年份，仅 `media_type=movie` 时参与命名。 |
| `items` | 是 | 1–500 条，每条 `file_id`、`name` 均非空。 |

文件名解析支持 `S01E01` / `s1e01`、`1x01`、`EP01`、`第01集` / `第1话` 形式；无法解析或无法生成新名时返回 `reason` 且 `changed=false`。

响应 `data` 为数组：

```json
[
  {
    "file_id": "67890",
    "old_name": "better.call.saul.S01E01.1080p.mkv",
    "new_name": "Better Call Saul S01E01.mkv",
    "changed": true
  }
]
```

### 应用

`POST /api/files/name-align/apply`

```json
{
  "account_id": 12,
  "parent_id": "12345",
  "items": [{ "file_id": "67890", "name": "旧名.mkv", "new_name": "新名.mkv" }]
}
```

- `items` 每条与预览约束一致；`new_name` 额外通过目录名校验（禁止分隔符）。
- 逐条执行，单条失败不中断；响应 `data` 为 `{ "success": [...], "failed": [...] }`。
- `success` 条目为 `{ file_id, old_name, new_name }`；`failed` 条目为 `{ file_id, name, reason }`。
- 成功后失效当前目录缓存。

## 批量重命名

批量重命名移植自 123 云盘批量重命名油猴脚本（`123-batch-rename.user.js`）的 RenameEngine，把通用规则引擎搬进服务端（`internal/renamerule` 包）：12 种规则、保留扩展名、冲突校验、历史回滚、常用组合。

### 规则

规则为 `rules` 数组，逐条依次应用。支持 12 种类型：

| 类型 | 字段 |
| --- | --- |
| `replace` 查找替换 | `find`、`replace`、`case_sensitive`（默认 false 不区分）、`first_only`（仅替换第一处） |
| `folder` 添加文件夹名 | `folder_name`（空时用请求 `folder_name`）、`separator`、`position`（`prefix`/`suffix`） |
| `regex` 正则重命名 | `pattern`、`replace`（替换模板，如 `$1`） |
| `setname` 名称模板 | `pattern`（支持 `{name}` 原名称、`{n}` 序号）、`start`、`digits` |
| `number` 添加序号 | `position`（`replace`/`prefix`/`suffix`）、`start`、`digits`、`prefix`、`suffix` |
| `separator` 添加分隔符 | `text`、`position`（`start`/`end`/`index`）、`index`（从 1 开始） |
| `add` 添加字符 | `text`、`position`、`index` |
| `delete` 删除字符 | `mode`（`text` 指定字符 / `range` 指定位置）、`text` 或 `start`+`length` |
| `move` 移动字符 | `start`、`length`、`to`（均从 1 开始） |
| `case` 大小写转换 | `mode`（`upper`/`lower`/`title`） |
| `space` 清理空格 | `mode`（`trim`/`collapse`/`all`） |
| `width` 全角半角转换 | `mode`（`half`/`full`） |

- 全部规则作用于「去扩展名的主名称」（`keep_ext=true` 且非目录时），扩展名最后拼回；`keep_ext=false` 时扩展名作为普通字符参与规则。
- 字符下标按 Unicode 码点计算（emoji 等按一个字符处理）。

### 预览

`POST /api/files/batch-rename/preview`

```json
{
  "account_id": 12,
  "parent_id": "12345",
  "folder_name": "待整理",
  "keep_ext": true,
  "rules": [{ "type": "replace", "find": "旧", "replace": "新" }],
  "items": [{ "file_id": "1", "name": "旧名.mkv", "type": 0, "parent_id": "12345" }],
  "existing_names": ["其它文件.mkv"]
}
```

| 字段 | 必填 | 规则 |
| --- | --- | --- |
| `account_id` | 是 | 正整数。 |
| `parent_id` | 否 | 当前目录标识。 |
| `folder_name` | 否 | 当前目录名，`folder` 规则未填 `folder_name` 时的默认值。 |
| `keep_ext` | 否 | 是否保留扩展名（默认 true）。 |
| `rules` | 是 | 1–20 条，类型必须合法；预览时校验正则、序号、模板等。 |
| `items` | 是 | 1–2000 条，每条 `file_id`、`name` 非空；`type` 0=文件 1=目录。 |
| `existing_names` | 否 | 当前目录除 `items` 外的已有名称，用于「同一目录已存在」冲突检测。 |

响应 `data`：

```json
{
  "items": [{ "target": { "file_id": "1", "name": "旧名.mkv", "new_name": "新名.mkv", "type": 0, "parent_id": "12345" }, "changed": true }],
  "errors": ["第 1 条规则 的正则表达式无效"],
  "changed_count": 1,
  "total_count": 1
}
```

`errors` 为空才允许应用；包含规则校验错误与目标冲突（空名、非法字符、超 255 字符、同目录重名、交换冲突、已存在）。

### 应用

`POST /api/files/batch-rename/apply`

```json
{
  "account_id": 12,
  "parent_id": "12345",
  "label": "批量重命名",
  "keep_ext": true,
  "rules": [{ "type": "replace", "find": "旧", "replace": "新" }],
  "items": [{ "file_id": "1", "name": "旧名.mkv", "new_name": "新名.mkv", "parent_id": "12345" }]
}
```

- `items` 为预览中 `changed=true` 的条目；`new_name` 通过目录名校验。
- 逐条执行，单条失败不中断；响应 `data` 为 `{ "success": [...], "failed": [...], "success_count", "fail_count" }`。
- 成功条目写入 `rename_histories`（按当前登录用户隔离，含规则快照与成功条目，用于回滚）；命中同名常用组合时累计 `use_count`。
- 成功后失效当前目录缓存。

### 历史与回滚

- `GET /api/files/batch-rename/history`：当前用户最近 80 条，`data` 为 `[{ id, name, rules, keep_ext, item_count, change_count, created_at }]`。
- `POST /api/files/batch-rename/rollback`：`{ "account_id": 12, "history_id": 7 }`。逐条还原原名，单条失败不中断；已还原条目从历史移除，全部还原则删除该历史。
- 历史按用户隔离（`rename_histories.user_id`）。

### 常用组合

- `GET /api/files/batch-rename/presets`：当前用户组合列表 `[{ id, name, rules, keep_ext, use_count }]`。
- `POST /api/files/batch-rename/presets`：`{ "name", "keep_ext", "rules" }`，同名覆盖（保留创建时间）；最长 64 字符。
- `DELETE /api/files/batch-rename/presets`：`{ "id": 3 }`。

## 目录整理

目录整理用于把散乱存放的媒体文件收束为 `电影/<标题> (年份)` 与 `剧集/<标题>/Season XX` 目录结构，每个文件执行「确保目标目录存在 → 移动 → 重命名为规范名」三步。

- 文件名解析复用命名对齐规则（规则实现位于 `internal/mediaparse` 包，目录整理 / 命名对齐 / MoviePilot 上传整理共用）：含 `S01E01` / `1x01` / `EP01` 等集数标记的判为剧集，含年份（`19xx` / `20xx`）的判为电影，其余跳过。
- `pan139` 支持（建目录走 `/file/create`、移动走 `/file/batchMove`、重命名走 `/file/update`）；`guangyapan` 支持（建目录走 `/file/create_dir`、移动走 `/file/move_file` 异步任务、重命名走 `/file/rename`）。
- 扫描上限 1000 项，最多递归 3 层（`depth` 默认 2）。

### MoviePilot 上传自动整理

MoviePilot 上传任务完成后自动对上传根目录执行同规则整理（`internal/moviepilot/organize.go`，扫描上限 500 项、递归 4 层），并额外提供两级兜底：

1. **AI 辅助识别**：正则解析失败（`unknown`）时，若刮削设置启用了 AI 识别（`enable_ai != off`），对文件名执行 AI 提取 + TMDB 校验（优先电影，其次剧集；剧集季集缺省 1）。每个整理任务最多尝试 20 个文件的 AI 识别，连续失败 3 次即停止后续尝试。AI 命中后按识别结果整理。
2. **识别失败落库**：AI 未启用 / 未命中或建目录、移动、重命名失败的视频文件记录到 `movie_pilot_failed_files`（同一任务下同名文件已有待处理记录时不重复插入），并计入任务 `error` 摘要。

整理成功后按成功目标目录逐个触发手动 STRM 同步（ID=0，按路径定位）；`strm_local_dir` 未配置时跳过 STRM 生成。

#### 下载完成检测

后台轮询（周期 `poll_interval` 分钟，默认 5 分钟）检测 MoviePilot 下载完成并创建上传任务（`internal/moviepilot/watcher.go`）：

- **下载列表**：轮询 `GET /api/v1/download/`，进度 100% 且非 `downloading` 的任务视为完成。
- **下载历史兜底**：MoviePilot 在下载完成并转移后会把任务从下载列表移除，故同时轮询 `GET /api/v1/history/download?page=1&count=100`（`moviepilot/client.go` 的 `ListDownloadHistory`），仅处理 `id` 大于上次游标的记录（内存游标 `lastHistoryID`），以 `download_hash` 与 `movie_pilot_upload_tasks.torrent_hash` 幂等去重。
- **本地路径匹配**：历史记录只含 MP 侧路径（如 `alist:/中国移动云盘/影视/待整理/日韩剧集/xxx`），实际文件位于 `local_view_root`（未配置时用 `download_root`）下同名目录，故取历史 `path` 最后一段在本地根下递归匹配目录（最多 3 层），匹配单个文件时取其所在目录；匹配到 0 个记录尝试时间（1 小时内不重试），匹配到多个跳过。
- **单轮上限**：每轮最多创建 20 个上传任务，避免批量历史任务瞬间涌入。
- **下载任务列表接口**：`GET /api/moviepilot/downloads` 合并下载列表与最近 20 条历史记录，并附带 `upload_status`（关联 `movie_pilot_upload_tasks.status`；已下载未捕获时为 `pending_capture`）。

#### 识别失败独立菜单接口

- `GET /api/moviepilot/failed-files?page&page_size&status`：分页查询（`status`：`pending` / `resolved` / `skipped`），返回 `{ list, total }`，`list` 项含 `task_title` 关联任务标题。
- `POST /api/moviepilot/failed-files/:id/identify`：AI 辅助识别该文件名，成功返回 `{ category, title, season, episode, year, tmdb_id }`。
- `POST /api/moviepilot/failed-files/:id/resolve`：请求体 `{ media_type, title, year, season }`；按文件所在源目录重新定位文件，执行建目录 → 移动 → 重命名，成功后记录状态 `resolved` 并回填媒体信息，同时按整理成功目录触发 STRM 同步；失败时保留 `pending` 并更新 `reason`。
- `POST /api/moviepilot/failed-files/:id/skip`：标记 `skipped`。

### 预览

`POST /api/organize/preview`

```json
{
  "account_id": 12,
  "path": "12345",
  "depth": 2
}
```

| 字段 | 必填 | 规则 |
| --- | --- | --- |
| `account_id` | 是 | 正整数。 |
| `path` | 是 | 源目录标识（与文件列表 `parent_id` 语义一致），同时作为整理根目录。 |
| `depth` | 否 | 扫描深度 1–3，默认 2。 |

响应 `data` 为：

```json
{
  "actions": [
    {
      "file_id": "67890",
      "old_name": "better.call.saul.S01E01.1080p.mkv",
      "new_name": "Better Call Saul S01E01.mkv",
      "category": "tv",
      "title": "Better Call Saul",
      "season": 1,
      "episode": 1,
      "year": 0,
      "target_rel_path": "剧集/Better Call Saul/Season 01",
      "supported": true
    }
  ],
  "groups": [
    {
      "category": "tv",
      "title": "Better Call Saul",
      "season": 1,
      "year": 0,
      "rel_path": "剧集/Better Call Saul/Season 01",
      "file_count": 10,
      "episode_min": 1,
      "episode_max": 10
    }
  ],
  "skipped": ["无法识别的文件.mkv"],
  "scanned": 100,
  "total": 10
}
```

- `actions` 为逐文件规划；`groups` 为按目标目录聚合的摘要；`skipped` 为无法归类跳过的文件名；`scanned` 为扫描到的文件/目录总数。
- 重命名规则：剧集 `标题 S01E01.ext`（季缺失按第一季，沿用解析出的年份到标题），电影 `标题 (年份).ext`（无年份则 `标题.ext`）。

### 应用

`POST /api/organize/apply`

```json
{
  "account_id": 12,
  "path": "12345",
  "items": [{ "file_id": "67890", "new_name": "Better Call Saul S01E01.mkv", "rel_path": "剧集/Better Call Saul/Season 01" }]
}
```

- `items` 最多 500 条，每条 `file_id`、`new_name`、`rel_path` 均非空。
- 每个文件按「确保目标目录存在 → 移动 → 重命名」逐条执行，单条失败不中断。
- 目标目录按 `rel_path` 相对整理根目录逐级创建，已存在时忽略错误继续。
- 响应 `data` 为 `{ "success": [...], "failed": [...] }`；`success` 条目为 `{ file_id, new_name, rel_path }`，`failed` 条目为 `{ file_id, name, reason }`。
- 成功后失效源目录缓存。

## 跨盘秒传

跨盘秒传用于把一个网盘账号目录下的文件批量传输到另一个网盘账号：优先按文件指纹在目标网盘秒传（不消耗流量），指纹未命中时自动降级为「中转上传」——从源网盘下载到服务器临时目录，再走上传队列上传到目标网盘。

- 相关代码：`backend/internal/controllers/crosstransfer.go`、`backend/internal/models/dbupload.go`（`downloadCrossTransferSource`、`tryLocalFingerprintRapid`、`UploadGuangYaPanFile`）、`backend/internal/guangyapan/`（上传与开发者接口）、`frontend/src/components/CrossTransferDialog.vue`、`frontend/src/components/GuangYaSmallTransferDialog.vue`。
- 秒传能力矩阵（按目标网盘）：

| 目标网盘 | 秒传指纹 | 限制 |
| --- | --- | --- |
| `115` | 源文件 SHA1 | 需源文件有 SHA1 |
| `123` | 源文件 MD5（Etag） | 需源文件有 MD5 |
| `baidupan` | 源文件 MD5 | 仅 ≤32MB 且需源文件有 MD5 |
| `openlist` | 无 | 只能中转 |
| `guangyapan` | GCID（分块 SHA1 再 SHA1） | flash 秒传未命中自动 OSS 上传 |
| `pan139` | 完整文件 SHA256 | 跨盘场景无本地文件，直接入队中转，上传阶段按 SHA256 自动秒传 |

- 源指纹来源：115 列表接口需开启 `show_sha1=1`（`v115open.FileListOptions.ShowSha1`）；123 列表的 `etag`、百度列表的云端 MD5 与 `fs_id`。
- 中转上传任务以 `source = "cross_transfer"` 入队，记录 `source_account_id`（源账号）与 `source_file_id`（源文件下载定位 ID：115 为 pickcode、百度为 `fs_id`、其余为文件 ID），下载完成后清理临时文件。
- 本地指纹秒传（上传队列兜底）：`dbupload.go` 的 `tryLocalFingerprintRapid` 会在普通上传前对**本地文件**计算指纹并再次尝试秒传（115 用 SHA1、123 用 MD5 `duplicate=2`、百度用 MD5 ≤32MB、139 用完整文件 SHA256），命中则跳过普通上传；秒传尝试失败时回退普通上传，不影响上传流程。
- 139 上传通道（`internal/pan139/upload.go`）：`UploadFile` 先 `/file/create`（`contentHash=SHA256`，`exist` 命中直接完成），未命中 `/file/getUploadUrl` 分批获取分片上传地址（首批发 100 片，超出部分按 100 片一批补齐），逐片 PUT（100MB 分片），最后 `/file/complete` 收尾。删除走 `/recyclebin/batchTrash` 异步任务，`WaitTaskDone` 轮询 `/task/get`（回退 `/hcy/task/get`）至 status=3（status=2 执行中）。
- 139 上传踩坑（2026-08-12 实测）：①`parentFileId` 必须传目录 ID（`RemotePathId`），传路径/名字报 `04000002` 请求参数不合法；②成功码是 `0000`，`checkUploadCode` 需兼容 `0`/`0000`；③分片 URL 为 AWS 预签名（签名覆盖 `content-length;host`），PUT 必须设置 `req.ContentLength`（`io.LimitReader` 作 body 时 Go 会走 chunked 导致 403 SignatureDoesNotMatch）；④`/file/create` 请求头需完整（含 `Caller`/`Mcloud-Sign` 签名等 personalHeaders 全套），头不全报 `04000003` 请求头不合法。

### 扫描

`POST /api/crosstransfer/scan`

```json
{ "account_id": 12, "path": "12345" }
```

- 递归扫描 `path` 下所有文件（最多 2000 个、深度 10 层），`path` 为空表示根目录。
- 响应 `data` 为 `{ "files": [...], "total": n, "truncated": bool }`；`files` 条目为 `{ source_file_id, download_id, rel_path, rel_dir, name, size, sha1, md5 }`。
- 115/123/百度源分别填充 `sha1`/`md5`（百度同时受 ≤32MB 限制）；OpenList 源两者皆空。

### 执行

`POST /api/crosstransfer/execute`

```json
{
  "source_account_id": 12,
  "target_account_id": 8,
  "source_path": "12345",
  "target_path": "67890",
  "conflict": "rename",
  "files": [{ "source_file_id": "a1", "download_id": "pc-xxx", "rel_path": "电影/A.mkv", "rel_dir": "电影", "name": "A.mkv", "size": 1024, "sha1": "...", "md5": "" }]
}
```

- `conflict`：`skip` / `rename` / `overwrite`，默认 `rename`；仅 123 秒传按此映射 `duplicate`（`rename`=1，其余=2），其余网盘冲突时秒传接口自行返回结果。
- 每个文件依次：确保目标目录存在（相对 `target_path` 按 `rel_dir` 逐级创建）→ 按目标网盘类型尝试秒传 → 未命中入队中转上传。
- `files` 最多 500 条，逐条执行不中断。
- 响应 `data` 为 `{ "results": [...], "rapid": n, "relay": n, "skipped": n, "failed": n }`；`results` 条目为 `{ rel_path, name, mode, success, file_id, error }`，`mode` 为 `rapid` / `relay` / `error`。

## 中国移动云盘分享转存

中国移动云盘（139）分享链接转存：把他人分享链接中的文件/目录直接保存到本账号指定目录，走 `share-kd-njs.yun.139.com` 分享域（无需 `mcloud-sign`，用账号名做 `account`）。

- 相关代码：`backend/internal/pan139/share.go`、`backend/internal/controllers/pan139_share.go`、`frontend/src/components/Pan139ShareDialog.vue`（文件管理工具栏「保存分享」，仅中国移动云盘账号显示，默认保存到当前目录）。
- 接口协议：`getOutLinkInfoV6` 查询分享内容（`bNum/eNum` 为 1 起始闭区间分页），`createOuterLinkBatchOprTask` 创建转存任务（返回 `taskID`）；转存任务无状态查询接口，提交成功后轮询目标目录列表确认文件出现（`WaitShareFilesVisible`，最多 20 秒）。
- 分享项 `path` 字段格式：`parentID/fileID`（目录 `catalogID`、文件 `contentID`），转存时原样提交给 `contentInfoList`（文件）与 `catalogInfoList`（目录）。

### 查询分享

`POST /api/pan139/share/info`

```json
{ "account_id": 12, "link_id": "xxx", "passwd": "", "ca_id": "root" }
```

- `ca_id` 为分享内目录 ID（根目录 `root`）。
- 响应 `data` 为 `{ "nod_num": n, "link_name": "...", "expire_time": "...", "folder_list": [{ "catalog_id", "ca_name", "path" }], "file_list": [{ "content_id", "co_name", "co_size", "path" }] }`。

### 转存

`POST /api/pan139/share/save`

```json
{
  "account_id": 12,
  "link_id": "xxx",
  "passwd": "",
  "target_catalog_id": "12345",
  "file_paths": ["root/abc123"],
  "dir_paths": ["root/def456"],
  "wait_visible": true
}
```

- `target_catalog_id` 为本账号目标目录 ID（根目录 `root`）。
- `wait_visible=true` 时轮询目标目录等待转存文件出现（最多 20 秒），超时返回 `code=500` 但任务仍在后台执行；响应 `data` 含 `{ "task_id", "visible", "missing" }`。

## 光鸭云盘秒传

光鸭目标秒传共两条通道：**flash 秒传**（目标为光鸭账号的普通跨盘/上传流程内自动尝试）与**开发者小号秒传**（通过官方开发者接口把当前开发者账号的文件秒传到小号接收 TOKEN）。

### 光鸭 flash 秒传

- 相关代码：`backend/internal/guangyapan/upload.go`。
- 流程：`get_res_center_token`（`capacity:2`，附 `fileSize`/`md5`，code `156` 表示小文件秒传命中）→ 未命中时 `check_can_flash_upload`（需 `gcid`）→ 命中则轮询 `get_info_by_task_id`（`147`=处理中）取 `fileId` → 未命中走 OSS 上传（阿里云 STS 凭证：小文件 PutObject、大文件 multipart），完成后轮询任务取 `fileId`。
- GCID 算法：按文件大小分块（≤128MB→256KB、≤256MB→512KB、≤512MB→1MB、更大→2MB），各块 SHA1 后整体再 SHA1，大写十六进制。
- 跨盘秒传目标为光鸭时，中转入队后 `UploadGuangYaPanFile` 内部自动先尝试 flash 秒传，未命中再 OSS 上传。
- 上传队列普通上传（目录监控/手动上传等）到光鸭账号也走同一通道。

### 光鸭开发者小号秒传

- 相关代码：`backend/internal/guangyapan/developer.go`（开发者客户端与签名）、`backend/internal/controllers/guangya_transfer.go`（接口与异步执行）、`backend/internal/models/guangya_developer.go`（表：`guangya_developer_settings`、`guangya_receiver_tokens`、`guangya_transfer_tasks`，数据库版本 `61`）。
- 前置：源光鸭账号需配置开发者 `client_id`/`client_secret`（在光鸭开发者平台申请，请求签名后才被接受），并添加一个**小号**光鸭账号分享的接收 TOKEN（接收 TOKEN 在小号上生成并授权目录）。
- 请求签名：请求头携带 `client_id`、`nonce`（16–32 位）、`timestamp`（秒级）、`sign`；`sign = SHA512(MD5("client_id=..&client_secret=..&nonce=..&timestamp=.."))` 十六进制；业务码 `18020` 凭据无效、`18021` 签名失败、`18022` 签名过期、`18023` nonce 重用、`18010`/`18013` 自动重试。
- 执行流程：`upload_by_fileid`（`{token_id, file_ids}`，≤20 项）→ 业务码 `18014` 表示文件已传过（幂等成功，跳过）→ `18011` 表示需预审：先 `pre_upload` 提交预审，轮询 `pre_upload_status`（`3`=通过继续秒传、`4`=未通过）→ 通过后再 `upload_by_fileid` → 轮询 `upload_status` 至 `success`/`failed`。
- 任务异步执行，同一账号+接收 TOKEN 同时只允许一个任务；任务状态 `running` / `auditing` / `success` / `failed`。
- 前端入口：文件管理工具栏「小号秒传」（`GuangYaSmallTransferDialog.vue`），在对话框内配置开发者凭据、管理接收 TOKEN、选择目录文件（复用 `/crosstransfer/scan` 加载文件列表）并创建任务。

### 开发者配置接口

`POST /api/guangya/developer-setting`

```json
{ "account_id": 12, "client_id": "xxx", "client_secret": "xxx" }
```

- 查询 `GET /api/guangya/developer-setting?account_id=12` 返回 `{ configured, client_id, secret_hint }`（secret 脱敏）；删除 `DELETE /api/guangya/developer-setting?account_id=12`。

### 接收 TOKEN 接口

`POST /api/guangya/receiver-tokens`

```json
{ "account_id": 12, "token_id": "xxxx", "remark": "小号A" }
```

- 列表 `GET /api/guangya/receiver-tokens?account_id=12`；删除 `DELETE /api/guangya/receiver-tokens/:id`。

### 小号秒传任务接口

`POST /api/guangya/small-transfer`

```json
{ "account_id": 12, "receiver_token_id": 1, "file_ids": ["f1", "f2"] }
```

- `file_ids` 为光鸭源账号内文件 ID（1–20 项），取自语列表接口或 `/crosstransfer/scan` 的 `source_file_id`。
- 创建即异步执行，响应 `{ task_id }`；列表 `GET /api/guangya/small-transfer?account_id=12` 返回任务数组（`status`、`total_count`、`success_count`、`skipped_count`、`failed_count`、`error_message` 等）；删除 `DELETE /api/guangya/small-transfer/:id`（仅限已完成/失败任务）。
