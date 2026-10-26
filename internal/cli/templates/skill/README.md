# aimemo skill

调用本地 `aimemo` 管理可复用经验库。AI 负责检索与沉淀，人类通过 markdown 导出阅读。

## 快速开始

```bash
# 在当前项目初始化 AGENTS.md / skill / agent
aimemo init

# 检索历史经验（任务开始前，优先用 context）
aimemo context "修复 sqlite busy 超时" --limit 5 --format prompt
aimemo search errors.Is --limit 5

# 使用后反馈，参与后续排序
aimemo use 1
aimemo feedback 1 --helpful

# 沉淀经验（征求用户同意后）
aimemo add --category skills/go --type important --importance 5 \
  --title "错误处理模式" \
  --content "场景：... 方案：... 注意：..." \
  --tag lang:go --tag error

# 其他常用
aimemo list --category skills/go --min-confidence 4
aimemo get 1
aimemo stats
aimemo doctor
aimemo backup --out ./aimemo-backup.db
aimemo usage
```

## 语言约束

- 语言特定经验：`skills/<语言>/<领域>`、`gotchas/<语言>/<场景>`，tag `lang:<语言>`
- 项目通用经验：`patterns/architecture`、`workflows/general`、`decisions/general`，tag `lang:general`
- 跨语言通用：`patterns/general`、`skills/general`，tag `lang:general`

检索时优先当前语言经验，其次通用经验，其他语言经验默认不采用。

## 双轨存储

- SQLite：面向 AI，精简、无歧义即可
- Markdown：面向人类，`aimemo export-md --out ./markdown --min-importance 4`

## 说明

- DB 默认与 exe 同目录：`aimemo.db`
- 可用 `--db` 覆盖，或环境变量 `AIMEMO_DB`
- 输出 JSON，直接解析 `ok`、`data`、`error`
- 用 `--workspace NAME` 隔离不同项目/租户的经验
