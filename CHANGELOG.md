# Changelog

All notable changes to this project will be documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased] - 2026-10-26

### Added

#### Semantic Search
- `aimemo search --semantic` uses feature-vector similarity (character bigrams + word tokens + synonym expansion) for offline semantic matching
- `features` BLOB column stores sparse feature vectors
- Synonym dictionary covers common technical terms (concurrency, database, security, etc.)
- `doctor --rebuild` now also rebuilds semantic features

#### Conflict Detection
- `aimemo conflicts` finds contradictions between experiences
- Detects: negation conflicts, near-duplicate titles, divergent solutions
- `--all-workspaces` and `--max-scan` flags

#### Cross-Workspace Search
- `--all-workspaces` flag on `search`, `list`, `context` commands

#### Agent Integration
- `updateself` command to sync embedded agent/skill/AGENTS.md templates
- Cross-tool compatibility: auto-generate `CLAUDE.md`, global `~/.claude/CLAUDE.md`, `.cursor/rules/aimemo.mdc`, and global Claude agents
- AGENTS.md updated with cross-tool compatible instructions (works without subagent support)

#### Build & CI
- `build.bat` to build with version/commit/date injected into `bin/`
- `.goreleaser.yaml` for cross-platform release builds with SHA256 checksums
- GitHub Actions CI (test on Linux/macOS/Windows, auto-release on tags)

#### Web Server
- Host header validation for security

#### CLI
- `prune --dry-run` enrichment with human-readable items

### Changed
- `init` now installs memory agents globally by default (`--no-global` to skip)

### Fixed
- Workspace isolation enforced across all store operations
- `supersedes` handled atomically in transactions
- Import preserves timestamps and deduplicates by title
- FTS consistency checks and `doctor --rebuild`

## [0.1.0] - 2026-10-23

### Added
- Initial release
- Core CRUD: `add`, `list`, `search`, `get`, `update`, `delete`
- FTS5 full-text search with CJK support (TransformCJK)
- Workspace isolation across all operations
- Structured memory: problem / solution / pitfall / example
- Quality loop: `use`, `feedback`, `supersedes`, `valid_until`
- Import/export: `export-md`, `import-md`, `export-json`, `import-json`
- Operations: `doctor`, `backup`, `prune`, `stats`, `categories`
- Agent integration: `init`, `usage`, `completion`
- Web server: `web` (read-only, localhost only)
- Configuration: flag > env > config file > default
