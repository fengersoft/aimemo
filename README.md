# aimemo

供 AI 调用的记忆存储程序。AI 自行决定分类，记录会话摘要或重要信息，数据存入 SQLite，所有数据表带创建时间。

## 构建

```bash
CGO_ENABLED=0 go build -o aimemo.exe .
```

## 调用

```bash
# 写入一条重要信息，分类不存在时自动创建
aimemo add --category "技术选型" --type important --importance 5 \
  --title "选型结论" --content "最终选择 SQLite 作为本地持久化数据库" --tag db

# 查看已有分类，复用而不是新建近义分类
aimemo categories

# 全文检索，中文按子串精确匹配
aimemo search 数据库

# 结构化列举
aimemo list --category "技术选型" --min-importance 4 --sort importance
```

## 输出契约

成功时 stdout 输出 `{"ok": true, "data": {...}}`；失败时 stderr 输出
`{"ok": false, "error": {"code": "...", "message": "..."}}`。

退出码：`0` 成功，`1` 运行时错误，`2` 用法错误。

错误码：`invalid_args`、`not_found`、`conflict`、`db_error`、`io_error`。

## 命令

| 命令 | 作用 |
|---|---|
| `add` | 写入记忆，分类不存在则自动创建 |
| `list` | 按分类/作用域/类型/标签/重要度/时间过滤列举 |
| `search` | 全文检索，返回正文与命中上下文 |
| `get` | 按 id 取单条完整记录 |
| `update` | 修改字段，仅覆盖显式传入的项 |
| `delete` | 删除记录 |
| `categories` | 列出已有分类及条目数 |
| `stats` | 分类/类型/作用域分布统计 |
| `usage` | 输出给 AI 看的使用指南 |

完整参数见 `aimemo usage`。

## 数据库

默认路径 `~/.aimemo/aimemo.db`，可用 `--db PATH` 或环境变量 `AIMEMO_DB` 覆盖。

表：`categories`、`memories`、`memory_tags`，以及 FTS5 索引 `memories_fts`。
每张表都有 `created_at`，`memories` 与 `categories` 另有 `updated_at`。
