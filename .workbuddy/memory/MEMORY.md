# llm-eyes-mcp 项目约定

## 版本管理
- **`.workbuddy/` 纳入版本管理**，不要写进 `.gitignore`。用户明确要求（2026-08-04）。
  其中的 memory 笔记记录了设计决策与验证结论，视作项目资产。
- `.gitignore` 里的忽略模式**必须加前导斜杠锚定**。曾因不带斜杠的 `llm-eyes-mcp`
  匹配到任意层级，把入口源码目录 `cmd/llm-eyes-mcp/` 整个排除，`main.go` 差点没进仓库。
  同类风险还有 `*.db`（不带斜杠，任意层级生效，目前是期望行为）。
- `.gitattributes` 用 `* text=auto eol=lf` 锁定 LF —— 项目要交叉编译到 linux/darwin，
  Windows 的 autocrlf 会给 Makefile 塞 CRLF 破坏 recipe。

## 构建与测试
- 零 CGO 是硬约束：`CGO_ENABLED=0 go build/test`。机器上没有 gcc，
  因此 `-race` 跑不了（需要的话得先装 MinGW）。
- 二进制预算 < 20MB，当前用 `-ldflags "-s -w"` 稳定在 11~12MB（四端）。

## 环境陷阱
- **`D:` 盘无法承载 L2 SQLite 缓存**：modernc.org/sqlite 在该卷报
  `SQLITE_READONLY_DBMOVED`（776），`C:` 盘正常。默认 `cache_dir` 是
  `~/.llm-eyes-mcp`（C 盘）所以不受影响，但手动把 `cache_dir` 指向 D 盘会踩雷。
- Git Bash 里给原生 Windows 二进制传路径要用 `$(pwd -W)` 转成 `D:/...` 形式，
  `/d/...` 形式二进制识别不了。
- 本 shell wrapper 对超长命令 / 多段 heredoc 链式调用不稳定（返回空输出），
  拆成小步执行更可靠。
