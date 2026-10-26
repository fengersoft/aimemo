# Contributing to aimemo

感谢你对 aimemo 的关注！本项目遵循 MIT 许可证，欢迎任何形式的贡献。

## 开发环境

```bash
git clone https://github.com/your-org/aimemo.git
cd aimemo

# 要求 Go 1.24+
go version

# 运行测试
go test ./...

# 静态检查
go vet ./...

# 构建
CGO_ENABLED=0 go build -o aimemo .
```

## 项目结构

```
internal/
├── cli/        # 命令行层：命令注册、flag 解析、输出渲染
└── store/      # 数据存储层：SQLite、FTS5、事务、迁移
```

**关键约束：**

- `internal/store` 不依赖 `internal/cli`（依赖方向单向）
- 所有 store 方法接受 `workspace` 参数，禁止绕过
- 输出契约：`{"ok":true,"data":...}` / `{"ok":false,"error":{...}}`
- 错误码：`invalid_args` / `not_found` / `conflict` / `db_error` / `io_error`
- 退出码：`0` 成功，`1` 运行时错误，`2` 用法错误

## 提交流程

1. **Fork** 仓库并创建特性分支：`git checkout -b feature/my-feature`
2. **开发**：遵循现有代码风格，添加必要的测试
3. **测试**：确保 `go test ./...` 全部通过
4. **提交**：使用清晰的 commit message（见下方规范）
5. **PR**：提交 Pull Request，描述改动内容和动机

## Commit Message 规范

```
<type>: <subject>

type:
  feat:     新功能
  fix:      修 bug
  docs:     文档
  test:     测试
  refactor: 重构
  chore:    杂项（构建、CI 等）

subject: 简短描述，不超过 50 字符，不加句号
```

示例：
```
feat: add semantic search with synonym expansion
fix: prevent workspace isolation bypass in export-json
docs: improve README with architecture section
```

## 代码规范

### Go 风格

- 遵循 `gofmt` / `go vet`
- 函数职责单一，避免过长函数
- 错误处理：包装错误时保留原始错误链
- 注释：导出函数必须有文档注释

### 测试

- 新功能必须附带测试
- 测试命名：`TestXxx`，描述行为而非实现
- 使用 `t.TempDir()` 创建临时目录
- 测试数据用 `newDB(t)` 辅助函数创建

### 安全

- 所有输入必须验证
- workspace 必须在查询中过滤
- 禁止将 SQL 错误直接暴露给用户
- Web 服务器仅监听 `127.0.0.1`

## 架构原则

1. **依赖单向**：cli → store，禁止反向依赖
2. **事务原子**：多表写入必须在事务内完成
3. **workspace 全链路**：每个 store 方法都接受 workspace 参数
4. **输出契约统一**：所有命令通过 `emit()` 输出
5. **向后兼容**：schema 变更需通过 `migrate()` 自动迁移

## 报告 Bug

请使用 [GitHub Issues](https://github.com/your-org/aimemo/issues)，包含：

- 操作系统和 Go 版本
- 复现步骤
- 期望行为 vs 实际行为
- 错误信息或日志

## 功能建议

欢迎在 Issues 中提出功能建议，请说明：

- 使用场景
- 期望的命令行接口
- 是否与现有功能冲突

## License

贡献代码即表示你同意将代码以 [MIT License](LICENSE) 授权。
