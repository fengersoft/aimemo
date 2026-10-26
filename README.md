# aimemo

> AI 的可复用经验库 — 让 AI 记住踩过的坑，下次不再重复

[![CI](https://github.com/fengersoft/aimemo/actions/workflows/ci.yml/badge.svg)](https://github.com/fengersoft/aimemo/actions/workflows/ci.yml)
[![Go Report](https://goreportcard.com/badge/github.com/fengersoft/aimemo)](https://goreportcard.com/report/github.com/fengersoft/aimemo)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/fengersoft/aimemo)](https://github.com/fengersoft/aimemo/releases)

aimemo 是一个为 AI Agent 设计的本地经验库。AI 在解决非平凡问题（修 bug、重构、技术选型）前先检索历史经验，任务结束后将可复用的结论沉淀回来。数据存 SQLite，离线可用，无外部依赖。

## 核心特性

- **本地优先** — SQLite 单文件存储，零外部依赖，`CGO_ENABLED=0` 纯 Go 编译
- **全文检索** — FTS5 索引，中文按子串精确匹配，支持 CJK 混排
- **语义检索** — 字符二元组 + 词 unigram + 同义词扩展，离线可用
- **冲突检测** — 自动发现经验库内部的矛盾（否定、重复、方案冲突）
- **工作区隔离** — 全链路 `workspace` 隔离，多项目互不干扰
- **Agent 集成** — 一键初始化 opencode / Claude Code / Cursor / Codex / CodeBuddy
- **结构化记忆** — problem / solution / pitfall / example 四段式，不是简单的便签
- **质量闭环** — use / feedback / supersedes / valid_until，经验会过期、会迭代

## 安装

### 从 Release 下载

```bash
# macOS (Apple Silicon)
curl -L https://github.com/fengersoft/aimemo/releases/latest/download/aimemo_darwin_arm64.tar.gz | tar xz
sudo mv aimemo /usr/local/bin/

# Linux (amd64)
curl -L https://github.com/fengersoft/aimemo/releases/latest/download/aimemo_linux_amd64.tar.gz | tar xz
sudo mv aimemo /usr/local/bin/

# Windows (PowerShell)
Invoke-WebRequest https://github.com/fengersoft/aimemo/releases/latest/download/aimemo_windows_amd64.zip -OutFile aimemo.zip
Expand-Archive aimemo.zip -DestinationPath .
```

### 从源码构建

```bash
git clone https://github.com/fengersoft/aimemo.git
cd aimemo
CGO_ENABLED=0 go build -o aimemo .
```

### 包管理器

```bash
# Homebrew (coming soon)
brew install aimemo

# Scoop (coming soon)
scoop install aimemo
```

## 快速开始

```bash
# 1. 初始化（写入 AGENTS.md、CLAUDE.md、opencode/Claude/Cursor 配置）
aimemo init

# 2. 写入一条经验
aimemo add \
  --category "go/sqlite" \
  --type important \
  --importance 5 \
  --title "SQLite FTS5 中文搜索" \
  --problem "FTS5 unicode61 分词器不切分中文，整句作为一个 token" \
  --solution "自研 TransformCJK 预处理，每个 CJK 字符两侧加空格" \
  --pitfall "只在右侧加空格会导致 CJK 与 Latin 融合成一个 token" \
  --tag lang:go --tag fts5

# 3. 检索
aimemo search "中文搜索"           # 全文检索
aimemo search --semantic "分词"   # 语义检索
aimemo context "修复中文搜索问题"  # AI 用的经验包

# 4. AI 集成
aimemo usage                       # 输出给 AI 看的使用指南
```

## 与 AI 工具集成

aimemo 通过 `AGENTS.md`（和 `CLAUDE.md`）与 AI 工具集成。`aimemo init` 会自动：

- 在项目根目录生成/合并 `AGENTS.md`
- 同步 `CLAUDE.md`（Claude Code / CodeBuddy / WorkBuddy）
- 安装 `.opencode/skills/aimemo/` 和 `.opencode/agent/memory-*.md`
- 安装 `~/.claude/agents/memory-*.md`（全局）
- 生成 `.cursor/rules/aimemo.mdc`（Cursor）

支持的工具：

| 工具 | 集成方式 |
|---|---|
| opencode | `.opencode/skills/` + `.opencode/agent/` |
| Claude Code | `CLAUDE.md` + `.claude/agents/` |
| Cursor | `.cursor/rules/aimemo.mdc` |
| Codex CLI | `AGENTS.md` |
| CodeBuddy / WorkBuddy | `CLAUDE.md` |

## 命令参考

### 核心命令

| 命令 | 作用 |
|---|---|
| `add` | 写入记忆，分类自动创建，标题相似时返回 `conflict` |
| `list` | 按分类/类型/标签/重要度/置信度/时间过滤列举 |
| `search` | 全文检索；`--semantic` 启用语义检索；`--all-workspaces` 跨工作区 |
| `context` | 按任务描述生成排序、去重、限长的经验包（`--format prompt`） |
| `get` | 按 id 取单条完整记录 |
| `update` | 修改字段，仅覆盖显式传入的项 |
| `delete` | 删除记录 |

### 质量闭环

| 命令 | 作用 |
|---|---|
| `use` | 记录一次经验使用，累加 `use_count` |
| `feedback` | 记录 `--helpful` / `--wrong`，参与排序 |
| `conflicts` | 检测经验之间的矛盾（否定/重复/冲突） |
| `prune` | 清理过期/被替代/过旧记录（默认 dry-run） |

### 导入导出

| 命令 | 作用 |
|---|---|
| `export-md` | 导出为面向人类的 markdown |
| `import-md` | 从 markdown 导回数据库 |
| `export-json` | 导出 JSON（`--all` 导出全部工作区） |
| `import-json` | 导入 JSON；默认按标题去重 |

### 运维

| 命令 | 作用 |
|---|---|
| `init` | 初始化 AGENTS.md / skill / agent；重复执行按标记块合并 |
| `doctor` | 检查数据库/FTS 完整性、是否在 PATH；`--rebuild` 重建索引 |
| `backup` | 用 `VACUUM INTO` 生成一致性备份 |
| `stats` | 分类/类型/作用域分布统计 |
| `categories` | 列出已有分类及条目数 |
| `workspaces` | 列出工作区及条目数 |
| `tag` | 管理标签 |
| `web` | 本地网页查看数据库（只读，默认 127.0.0.1:8080） |
| `completion` | 生成 bash/zsh/fish/powershell 补全脚本 |
| `version` | 查看构建版本 |

完整参数见 `aimemo usage`。

## 配置

优先级：命令行 flag > 环境变量 > 配置文件 > 内置默认值。

配置文件路径：`$AIMEMO_CONFIG` 或 `os.UserConfigDir()/aimemo/config.json`：

```json
{
  "db": "D:/data/aimemo.db",
  "workspace": "my-project",
  "web": { "addr": "127.0.0.1:8080", "open": false }
}
```

环境变量：`AIMEMO_DB`、`AIMEMO_WORKSPACE`、`AIMEMO_CONFIG`、`AIMEMO_WEB_ADDR`、`AIMEMO_WEB_OPEN`。

## 输出契约

成功：`{"ok": true, "data": {...}}`（stdout）

失败：`{"ok": false, "error": {"code": "...", "message": "..."}}`（stderr）

退出码：`0` 成功，`1` 运行时错误，`2` 用法错误

错误码：`invalid_args`、`not_found`、`conflict`、`db_error`、`io_error`

## 架构

```
aimemo/
├── main.go                  # 入口，注入 version/commit/date
├── internal/
│   ├── cli/                 # 命令行层
│   │   ├── root.go          # 命令分发、全局 flag
│   │   ├── commands.go      # 核心命令实现
│   │   ├── commands_extra.go# 扩展命令（doctor/prune/init/conflicts）
│   │   ├── web.go           # Web 服务器
│   │   ├── config.go        # 配置加载
│   │   ├── render.go        # 输出渲染
│   │   ├── assets.go        # 嵌入模板
│   │   └── templates/       # AGENTS.md / skill / agent 模板
│   └── store/               # 数据存储层
│       ├── store.go         # 连接、事务、迁移
│       ├── schema.go        # DDL、版本迁移
│       ├── memory.go        # CRUD、列表、搜索
│       ├── query.go         # 过滤器、排序
│       ├── fts.go           # FTS5 索引、CJK 处理
│       ├── semantic.go      # 语义搜索（特征向量）
│       ├── conflict.go      # 冲突检测
│       ├── lifecycle.go     # supersedes / use / feedback
│       └── admin.go         # doctor / backup / prune / 统计
└── tests/                   # 端到端测试
```

### 数据库 Schema

- `categories` — 分类，自动创建
- `memories` — 核心表，含结构化字段（problem/solution/pitfall/example）、workspace、confidence、valid_until、supersedes、使用计数、features BLOB
- `memory_tags` — 多对多标签
- `memories_fts` — FTS5 全文索引

Schema 版本记录在 `PRAGMA user_version`，打开旧库时自动 `ALTER TABLE` 迁移。

## 常见场景

```bash
# 跨项目复用经验
aimemo search --all-workspaces "SQLite 并发"

# 检测经验库内部矛盾
aimemo conflicts

# 按任务描述获取经验包（给 AI 用）
aimemo context "修复 WebSocket 连接泄漏" --format prompt

# 定期维护
aimemo doctor --rebuild      # 重建 FTS + features 索引
aimemo prune --expired --force
aimemo backup --out ./backup.db

# 导出给团队共享
aimemo export-md --out ./knowledge --min-importance 4
```

## Contributing

欢迎贡献！请先阅读 [CONTRIBUTING.md](CONTRIBUTING.md)。

```bash
# 开发环境
git clone https://github.com/fengersoft/aimemo.git
cd aimemo
go test ./...          # 运行测试
go vet ./...           # 静态检查
./build.bat            # 构建（Windows）
```

## License

[MIT](LICENSE)
