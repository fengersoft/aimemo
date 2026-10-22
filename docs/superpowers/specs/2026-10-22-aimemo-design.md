# aimemo 设计文档

日期：2026-10-22
状态：已确认

## 1. 目标

提供一个**供 AI 通过命令行调用**的记忆存储程序。AI 在工作过程中可以：

- 自行决定分类（category），无需预先配置
- 记录会话摘要（summary）或重要信息（important）
- 按分类、作用域、标签、重要度、时间范围检索记忆
- 以 SQLite 作为持久化存储，所有数据表带创建时间

## 2. 非目标（本期不做）

- 不做语义向量检索 / embedding（本期为字面全文检索）
- 不做多用户与权限
- 不做 HTTP 服务端与 MCP server
- 不做记忆自动过期 / 衰减
- 不做 Web UI

## 3. 关键设计决策与依据

| 决策 | 依据 |
|---|---|
| CLI 而非 MCP / HTTP | 任何具备 shell 能力的 AI agent 均可调用；交付单一可执行文件 |
| 纯 Go SQLite 驱动 `modernc.org/sqlite` | 目标环境 `CGO_ENABLED=0`，无需 C 工具链即可交叉编译为单文件二进制 |
| SQLite FTS5 + `unicode61` 分词 + CJK 字符双侧补空格 | 实测：`unicode61` 默认把整句中文当作**一个** token，搜索"数据"无法命中"使用数据库"；补空格后中文子串精确匹配 |
| 查询串自行构建 FTS5 表达式，绝不原样传入 | 实测原样传入 `x" AND y` 触发 `SQL logic error: unterminated string` |
| 默认排序为 `importance DESC, created_at DESC` | 实测 bm25 对中文内容区分度极低（多行 rank 均约 0），因单字 CJK token 的 IDF 接近 0 |
| 独立 FTS5 表（非 external content） | FTS 索引存的是补空格后的文本，与 `memories.content` 原文不同；external content 模式要求二者字面一致，故不适用 |
| 标签用独立关联表 | 支持按标签过滤与索引，且不污染 `memories` 主表 |

### 3.1 实测验证记录

在 `modernc.org/sqlite`（SQLite 3.46.0）上的实测结论：

1. FTS5、trigram、unicode61 三种分词器均可创建虚拟表。
2. `unicode61` **不做** CJK 变换时，查询 `"数据"` 命中 0 条（整句中文为单 token），`"数据库"` 仅在完全等值时命中。
3. CJK 变换必须**双侧**补空格。仅在 CJK 字符后补空格时，`Redis作为` 会退化为单个 token `Redis作`，查询 `redis` 命中 0 条；双侧补空格后命中 1 条。
4. FTS5 phrase 查询（`"数 据 库"`）实现**位置连续**的精确子串匹配，符合预期。
5. bm25 在中文小语料下无法区分相关性。
6. 查询串经程序化构建（引号转义为 `""`）后，未出现任何 FTS5 语法错误。
7. 1000 条记录批量写入正常。

## 4. 架构

```
aimemo/
├── main.go                    入口：子命令分发、全局 flag 解析
├── internal/
│   ├── store/                 存储层（不感知 CLI/JSON）
│   │   ├── store.go           Open / Close / schema 迁移 / 事务辅助
│   │   ├── schema.go          DDL 常量与 schema_version
│   │   ├── memory.go          记忆 CRUD、列表、搜索
│   │   ├── category.go        类别 CRUD 与统计
│   │   └── fts.go             CJK 变换、FTS 查询构建、索引同步
│   └── cli/                   命令层
│       ├── root.go            子命令分发、输出信封、错误码、退出码
│       ├── add.go
│       ├── list.go
│       ├── search.go
│       ├── get.go
│       ├── update.go
│       ├── delete.go
│       ├── categories.go
│       ├── stats.go
│       └── usage.go
└── tests/                     端到端测试
```

分层约束：`internal/store` 不 import `internal/cli`；`internal/cli` 不写 SQL。所有 SQL 集中在 store 层。

### 4.1 依赖

- `modernc.org/sqlite` —— 纯 Go SQLite（含 FTS5）
- 标准库 `flag`、`encoding/json`、`database/sql`、`time`、`strings`、`unicode`

无其他第三方依赖。

## 5. 数据模型

### 5.1 categories

```sql
CREATE TABLE categories (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);
```

`COLLATE NOCASE` 使 `"技术选型"` 与 `"技术选型"`（含大小写差异的英文）视为同一类别，避免 AI 造出近义重复类别。

### 5.2 memories

```sql
CREATE TABLE memories (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    category_id INTEGER NOT NULL REFERENCES categories(id),
    type        TEXT    NOT NULL DEFAULT 'note'
                        CHECK (type IN ('summary','important','note')),
    importance  INTEGER NOT NULL DEFAULT 3
                        CHECK (importance BETWEEN 1 AND 5),
    title       TEXT    NOT NULL DEFAULT '',
    content     TEXT    NOT NULL,
    scope       TEXT    NOT NULL DEFAULT '',
    source      TEXT    NOT NULL DEFAULT '',
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL
);
```

`type` 三值：`summary`（会话/任务摘要）、`important`（重要信息）、`note`（一般记录，默认值）。

### 5.3 memory_tags

```sql
CREATE TABLE memory_tags (
    memory_id  INTEGER NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
    tag        TEXT    NOT NULL,
    created_at TEXT    NOT NULL,
    PRIMARY KEY (memory_id, tag)
);
```

### 5.4 memories_fts

```sql
CREATE VIRTUAL TABLE memories_fts USING fts5(
    title, content, tags,
    tokenize='unicode61 remove_diacritics 2'
);
```

独立 FTS 表，`rowid` 与 `memories.id` 对齐。写入、更新、删除由 store 层显式同步，不使用触发器（因 CJK 变换需在 Go 侧完成）。

### 5.5 索引

```sql
CREATE INDEX idx_memories_category ON memories(category_id);
CREATE INDEX idx_memories_created  ON memories(created_at DESC);
CREATE INDEX idx_memories_scope    ON memories(scope);
CREATE INDEX idx_memories_type     ON memories(type);
CREATE INDEX idx_memories_importance ON memories(importance DESC);
CREATE INDEX idx_memory_tags_tag   ON memory_tags(tag);
```

### 5.6 时间戳规范

- 全部由 Go 生成，格式 `2006-01-02T15:04:05.000000Z`（UTC，微秒精度，RFC3339 变体）
- 字典序即时间序，可直接用于 `ORDER BY` 与范围比较
- **严格单调递增**：宿主机时钟可能几毫秒才前进一次，`Now()` 在时钟未前进时把上一次的值加 1 微秒，保证连续两次写入的时间戳不同
- `created_at` 建表即写入，不可修改；`updated_at` 每次写入/更新刷新
- **所有数据表均含 `created_at`**

## 6. CLI 契约

### 6.1 全局 flag

| flag | 默认值 | 说明 |
|---|---|---|
| `--db PATH` | `$AIMEMO_DB`，否则 `~/.aimemo/aimemo.db` | 数据库文件路径 |
| `--format json\|text` | `json` | 输出格式 |

数据库目录不存在时自动创建。

### 6.2 输出信封

成功（stdout，退出码 `0`）：

```json
{"ok":true,"data":{"id":1,"category":"技术选型","type":"important","importance":4,"title":"选型结论","content":"...","scope":"proj-x","source":"","tags":["db","选型"],"created_at":"2026-10-22T09:05:00.123456Z","updated_at":"2026-10-22T09:05:00.123456Z"}}
```

失败（stderr，退出码 `1`；用法错误为 `2`）：

```json
{"ok":false,"error":{"code":"not_found","message":"memory 42 not found"}}
```

错误码：`invalid_args`、`not_found`、`conflict`、`db_error`、`io_error`。

`--format text` 输出人类可读文本，不带信封。

### 6.3 命令

#### `add`

| flag | 必填 | 默认 | 说明 |
|---|---|---|---|
| `--category` | 是 | — | 类别名，不存在则自动创建 |
| `--content` | 是 | — | 正文；值为 `-` 时从 stdin 读取 |
| `--title` | 否 | `""` | 标题 |
| `--type` | 否 | `note` | `summary` / `important` / `note` |
| `--importance` | 否 | `3` | 1–5 整数 |
| `--tag` | 否 | — | 可重复，最多 20 个 |
| `--scope` | 否 | `""` | 作用域（项目名 / 会话名） |
| `--source` | 否 | `""` | 出处 |

返回：新建记录的完整 JSON。

#### `list`

结构化列举，无关键词。

过滤参数：`--category`（可重复，同类别间 OR）、`--scope`、`--type`（可重复）、`--tag`（可重复，同标签间 OR）、`--min-importance N`、`--since`、`--until`、`--limit`（默认 20，上限 500）、`--offset`、`--sort`（`created_at` 默认 / `importance`）。

`--since` / `--until` 接受 RFC3339 或 `YYYY-MM-DD`。

返回 `{"items":[...],"total":N,"limit":L,"offset":O}`。

字段约定（`list` 与 `search` 区分，避免 AI 误以为拿到了全文）：

- `list` 每条**不含** `content` 全文，只含 `content_preview`（正文前 200 字符）
- `search` 每条**含**完整 `content`，另含 `match_context`
- `get` 每条含完整 `content`，无预览字段

#### `search`

`aimemo search <QUERY>`，接受全部 `list` 过滤参数，另有 `--loose`。

- `--loose`：CJK 片段拆为单字 token 以 AND 连接（宽松召回）；默认使用 phrase 精确子串匹配。
- `--sort` 取值：`importance`（默认）、`created_at`、`relevance`。默认排序为 `importance DESC, created_at DESC`；`relevance` 使用 bm25（对中文区分度低，仅供英文内容使用）；`created_at` 按时间倒序。

返回 `{"items":[...],"total":N,"query":"..."}`。每条含 `match_context`：命中位置前后各 120 字符的原文窗口（由 Go 侧在原始文本上计算，不使用 FTS `snippet()`，避免输出补空格后的文本）。

#### `get`

`aimemo get <ID>` → 记录完整 JSON（含完整 `content`）。

#### `update`

`aimemo update <ID>`，接受 `add` 的全部字段作为可选覆盖（仅传入的字段被更新），另加 `--add-tag`（可重复）、`--remove-tag`（可重复）。

`--type` / `--importance` / `--title` / `--content` / `--category` / `--scope` / `--source` 采用"显式传入才覆盖"语义：通过 `flag.Visit` 检测哪些 flag 被显式设置，未设置的一律保留原值。

#### `delete`

`aimemo delete <ID>` → `{"deleted":N}`。不存在则 `not_found`。

#### `categories`

`aimemo categories [--sort name|count|created_at]` → 类别列表，含 `name`、`memory_count`、`created_at`、`updated_at`。

#### `stats`

`aimemo stats` → 总条目数、按 category / type / scope 的分布、平均重要度。

#### `usage`

`aimemo usage` → 输出给 AI 使用的紧凑指南（JSON），列出全部命令、参数与典型调用示例。供 AI 在会话开始时一次性读取。

### 6.4 校验与限制

| 项 | 限制 | 超限行为 |
|---|---|---|
| `content` | ≤ 100000 字符 | `invalid_args` |
| `title` | ≤ 500 字符 | `invalid_args` |
| `category` | ≤ 100 字符 | `invalid_args` |
| `tag` | ≤ 50 字符，最多 20 个 | `invalid_args` |
| `scope` / `source` | ≤ 200 字符 | `invalid_args` |
| `importance` | 1–5 整数 | `invalid_args` |
| `type` | `summary` / `important` / `note` | `invalid_args` |
| `limit` | 1–500 | `invalid_args` |
| `id` | 正整数 | `invalid_args` |

`content` 为空（去空白后）视为 `invalid_args`。

## 7. 搜索实现

### 7.1 CJK 变换

索引端与查询端共用同一函数：

```
transform(s):
  对 s 中每个满足 (r > 0x2E80 && unicode.IsLetter(r)) 的 rune：
    输出 " " + r + " "
  其余 rune 原样输出
  结果去除首尾空白
```

示例：

```
"使用SQLite数据库存储摘要和重要信息"
→ "使  用  SQLite  数  据  库  存  储  摘  要  和  重  要  信  息"
```

双侧补空格是必需的：仅在 CJK 后补空格会使 `Redis作为` 合并为单 token，导致 `redis` 检索失效（实测命中 0 → 修复后命中 1）。

### 7.2 查询表达式构建

1. 查询串按空白切分为 term
2. 每个 term 再按 CJK / 非 CJK 切分为片段
3. CJK 片段 → FTS5 phrase：`"数 据 库"`（内部空格保留，位置连续 ⇒ 精确子串）
4. 非 CJK 片段 → FTS5 term 或 phrase；所有引号在写入 MATCH 前转义为 `""`
5. 各 term 之间用 `AND` 连接
6. `--loose` 时，步骤 3 的 phrase 改为将 CJK 片段逐字拆成独立 token，用 `AND` 连接

生成结果形如：`"SQLite" AND "数 据 库"`。

原始查询串**绝不**直接传给 FTS5 的 MATCH 运算符。

语义要点：

- **片段内是精确子串**：单个 CJK 片段 `"数 据 库"` 只命中字符连续的那一段，不会命中"数据在别处，还有库"
- **片段必须落在同一列内**：一个 phrase 必须在 title / content / tags 中的某一列内连续，跨列不成立
- **片段间是 AND**：含拉丁文或标点的查询会被拆成多个片段，要求全部命中；越宽松召回越高
- 查询长度上限 `MaxQueryRunes = 256`，超出返回 `invalid_args`
- NUL 字节在**索引侧与查询侧同时剔除**：FTS5 按 C 字符串解析引号内文本，NUL 会截断引号并报 `unterminated string`；两侧必须一致，否则同一份文档与同样的查询会互相不匹配

### 7.3 索引同步

- 新增记忆：`INSERT INTO memories_fts(rowid, title, content, tags)`，内容为变换后文本，tags 为空格连接后的变换文本
- 更新记忆：先 `DELETE FROM memories_fts WHERE rowid = ?`，再插入新行
- 删除记忆：同样 `DELETE FROM memories_fts WHERE rowid = ?`
- 三者均与主表写入处于**同一事务**

### 7.4 命中上下文

在 Go 侧对原始 `content` 计算命中窗口：取查询片段中**在原文里命中的最长片段**，以它在原文中的起始位置为中心，前后各取 120 字符；原文不足时按实际长度截取，超出部分以 `…` 标示。片段匹配采用与 FTS 一致的语义（中文按连续子串，英文按词边界，不区分大小写）。

## 8. 错误处理

退出码与错误码的对应关系：

| 退出码 | 错误码 | 场景 |
|---|---|---|
| `2` | `invalid_args` | 参数缺失 / 非法 / 越界；未识别的子命令；`id` 非正整数 |
| `1` | `not_found` | `update` / `delete` / `get` 的目标不存在 |
| `1` | `db_error` | SQLite 写入或查询失败 |
| `1` | `io_error` | 文件、stdin 读写失败 |
| `1` | `conflict` | 写入违反唯一约束（无法预先检测的罕见情况） |

其他规则：

- 所有写操作用事务包裹，出错回滚
- SQLite 错误统一包装为 `db_error`，并保留原始错误文本
- 重复 tag 在写入前就被检测出来，返回 `invalid_args`（而不是落到 SQLite 的唯一约束错误上）
- 类别名冲突（`COLLATE NOCASE` 下唯一）→ 复用已有类别，不报错；`update` 改类别到已存在的名称 → 复用
- `get` 目标不存在同样返回 `not_found`
- 数据库文件所在目录不存在时自动创建；无法创建则返回 `io_error`

## 9. 测试策略

端到端测试（`tests/`），每条命令均覆盖，覆盖以下关键场景：

1. `add` 自动创建类别，重复类别名复用同一类别（含大小写差异）
2. `add` 后 `get` 能取回完整内容与全部字段
3. `add` → `list` 各类过滤组合正确
4. **中文子串检索**：`存储 "SQLite数据库" 的方案` 写入后，`search 数据库` 能命中
5. **中英混排检索**：`使用Redis作为缓存` 写入后，`search redis` 能命中
6. **中文精确子串**：查询 `数据库` 不应命中仅含 `数据`的记录
7. `search` 结果含 `match_context` 且为未补空格的原文
8. `update` 仅覆盖显式传入的字段；`--add-tag` / `--remove-tag` 生效
9. `delete` 后 `get` 返回 `not_found`
10. `usage` 输出包含全部命令名
11. 非法参数（`importance=9`、非法 `type`、超长 content）返回 `invalid_args`
12. `categories` 返回的 `memory_count` 与实际写入数一致

## 10. 交付物

- 源码：`main.go` + `internal/store` + `internal/cli`
- 可执行文件：`aimemo.exe`（`CGO_ENABLED=0 go build`）
- 数据库：首次运行时自动创建于 `~/.aimemo/aimemo.db`
