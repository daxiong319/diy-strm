# 网盘文件操作 API

> 职责：定义网盘文件浏览与操作接口的路径、字段、验证和响应契约，包括列表、创建目录、删除、重命名、移动、命名对齐和目录整理。
>
> 权威范围：本文档维护 `/api/path/*`、`/api/files/name-align/*` 与 `/api/organize/*` 的 HTTP 契约；文件列表缓存、STRM 生成和刮削整理见 [STRM 同步调度与任务记录](../architecture/sync-orchestration.md) 与 [上传与 STRM 处理](../architecture/upload-and-strm-processing.md)。
>
> 修改时机：修改上述路由、请求字段、验证、缓存失效或响应契约时必须更新本文档。
>
> 相关代码：`backend/internal/controllers/path.go`、`backend/internal/controllers/path_ops.go`、`backend/internal/controllers/name_align.go`、`backend/internal/controllers/net_file_batch.go`、`backend/internal/controllers/organize.go`、`backend/internal/requests/operations.go`、`frontend/src/components/AppFileManager.vue`、`frontend/src/components/NameAlignDialog.vue`、`frontend/src/components/OrganizeDialog.vue`。

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
| 目录整理预览 | `POST /api/organize/preview` | 扫描目录并规划整理动作（建目录 + 移动 + 重命名），不改动文件。 |
| 目录整理应用 | `POST /api/organize/apply` | 按预览结果批量建目录、移动并重命名，逐条执行并汇总。 |

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
- `guangyapan`、`pan139` 暂不支持重命名，返回明确错误。
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

- `guangyapan`、`pan139` 暂不支持移动。
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

## 目录整理

目录整理用于把散乱存放的媒体文件收束为 `电影/<标题> (年份)` 与 `剧集/<标题>/Season XX` 目录结构，每个文件执行「确保目标目录存在 → 移动 → 重命名为规范名」三步。

- 文件名解析复用命名对齐规则：含 `S01E01` / `1x01` / `EP01` 等集数标记的判为剧集，含年份（`19xx` / `20xx`）的判为电影，其余跳过。
- `guangyapan`、`pan139` 暂不支持目录整理（不支持移动），预览与应用均返回明确错误。
- 扫描上限 1000 项，最多递归 3 层（`depth` 默认 2）。

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
