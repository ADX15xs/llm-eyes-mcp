# 项目记忆归档（2026-08-03 ~ 2026-08-07）

本文件由 `.workbuddy/memory/` 提炼归档：保留设计决策、根因结论、教训与环境陷阱等**长期有价值**的内容；剔除一次性执行细节（逐次任务状态、可重复推导的过程、已过期远端 URL）。详见文末"归档取舍"。

## 1. 项目概览

- llm-eyes-mcp：Go 手写 stdio JSON-RPC MCP 服务，三个聚焦工具
  `measure_image` / `describe_image` / `extract_text` + 三级缓存（L1 内存 / L2 磁盘 SQLite / L3 内存 LRU）。
- 零 CGO（`CGO_ENABLED=0`），单静态二进制 < 20 MB（实测 11~12 MB，`-ldflags "-s -w"`）。
- 自实现协议层（不用 SDK），当前声明版本 `2024-11-05`（`internal/mcp/jsonrpc.go:13`）。
- 灵感来自 [Sorwcyra/ds-vision-skill](https://github.com/Sorwcyra/ds-vision-skill)（PowerShell 版），
  更深层渊源是 DeepSeek 论文的"视觉原语 / 指代鸿沟"思路；工程简化为"模型只报坐标 + Go 计算测量"。
  README `## Inspiration` 与 `## 支持与贡献` 已写明。

## 2. 版本管理约定（含踩坑教训）

- `.gitignore` 忽略模式**必须加前导斜杠锚定**。曾因 `llm-eyes-mcp` 无斜杠，匹配到
  `cmd/llm-eyes-mcp/` 入口源码目录并将其整体排除，`main.go` 差点未入库。同类风险有 `*.db`（无斜杠、任意层级生效，当前是期望行为）。
- 教训：给二进制产物写 gitignore 时，凡与 `cmd/` 子目录同名的一律加前导斜杠；提交前
  `git ls-tree -r HEAD --name-only | grep cmd/` 确认入口源码入库。
- `.gitattributes`：`* text=auto eol=lf` + 各类型显式 `eol=lf`。项目交叉编译到 linux/darwin，
  Windows autocrlf 会给 Makefile 塞 CRLF 破坏 recipe。
- `.workbuddy/memory/` 曾**临时纳入版本管理**（commit `b2e0f08`，因记录设计决策与验证结论）；
  现已提炼归档到 `docs/archive/memory-archive.md` 并将 `.workbuddy/` 排除出版本管理。

## 3. 版本发布

- Release 工作流 `.github/workflows/release.yml`：push `v*` tag 触发，
  `make test` → matrix 三平台（windows/amd64、linux/amd64、darwin/arm64，零 CGO，`ldflags` 注入 `main.version=${GITHUB_REF_NAME}`）→ 产物打包 + changelog + checksums.txt。
- 版本真相源 = git tag；构建期 `-ldflags -X` 注入，仓库无硬编码版本常量。
- **发版推送教训**：branch 与 tag 同一次 push 时，tag 触发的 workflow 偶发不派发（GitHub 已知行为）。
  修法：分开推——先 `git push`（分支），再 `git push origin <tag>`；若 workflow 没跑，先单独重推 tag，
  而非去怀疑 repo Settings。
- 已发 tag：`v0.1.0`（prerelease）、`v0.1.1`（annotated，指向 `eb6a037`）。

## 4. 已修复的关键缺陷（根因 + 方案，长期记忆）

1. **`config.Default()` 预填 `DefaultProvider:"glm"`** → 唯一启用 provider 若非 glm 会被 `Validate` 拒绝。
   改为空串，让 `Validate` 回退到唯一 provider。
2. **`config.ResolvePath` 对 `--config` 做 `os.Stat` 判定 found** → 路径写错静默回落到"未找到配置"的误导信息。
   改为 `--config` 权威，直接返回 `found=true`，由 `Load` 报明确错误。
3. **L2 磁盘缓存 WAL 模式无回退**（`internal/cache/l2.go`）→ 加 WAL 打开失败时降级到默认 DELETE journal 的兜底。
4. **MCP content union 兼容（`ResourceContent` 死代码）**：`EmbeddedResource.resource` 必须是 `{uri,text}` 或 `{uri,blob}`，
   只给 `{uri,mimeType}` 在严格校验 SDK 下被整条拒收；`resource_link` 还要求 `name`（来自 BaseMetadata，必填）。
   本项目 `ResourceContent` 全仓库无调用点（死代码），且未实现 resources capability → 按 YAGNI 直接删除而非修正。
   加 `validContentBlock` 校验器 + 回归测试防复现。运行时行为零变化。
5. **L3 几何缓存 key 不含 provider/model**（`measure_image`）→ `provider` 参数在 L3 命中时被静默忽略、不同 provider 串用同一份几何结果。
   修法：`cache.L3Key` 加 `modelVersion`；`measure.go` 把 `providerFor` 提到 L3 命中检查之前（附带语义变更：之前不存在 provider + L3 命中会静默返缓存，现 fail-fast）。
   新增 `TestL3KeyModelVersionMatters`。
6. **像素/坐标尺寸错配（根因）**：`detectObjects` 把**原始** `img.Width/Height` 喂给 `parseDetections/normaliseCoords`，
   但模型看到的是缩放后（≤1500px）的 `proc.Data`；像素回复被原始尺寸除 → 任何长边 >1500px 的图坐标全错。
   修法（自描述信封、零耦合）：L2 存 `{text,w,h}` 信封，命中时用信封里"模型看到的尺寸"重解析；fresh 路径传 `proc.Width/Height`；
   旧裸文本条目解析失败被自然丢弃，无需迁移。
   测试：`TestMeasureImagePixelsRelativeToResizedImage`（2000×1333→缩放1500×1000，断言距离=1600.00 而非 bug 的 1200.00）。
7. 以上修复集中在 `eb6a037`（`fix(cache): include model version in L3 key and fix coordinate scaling`），随 `v0.1.1` 发布。

## 5. 已知限制 / 潜在缺陷（待定，非隐性债）

- **千分值 vs 像素歧义**：`normaliseCoords` 对"千分值(0..1000, Qwen-VL 系)"与"像素值"按数值大小启发式二选一。
  图片长边 ≥~1000px 时千分值(≤1000)必落在像素范围内被误判 → 坐标约减半。即便修了尺寸错配，缩放图长边仍可能 1000–1500px，歧义残留。
  正确解法：给 provider 加带外 `coord_convention: thousandths|normalized|pixels` 配置并接入 prompt。注释已写明，列为待定。
- 轻微项（未动，不影响运行）：① `server.go` 未知工具/参数错误用 `CodeInternalError`（语义偏）；
  ② `cache.Settings` 的 `SweepIdle`/`CredentialHash` 是死字段（实际由 `Manager.Startup` 参数传入）；
  ③ `geometry.Area` 多框时 `Value` 取面积和、`Details` 另有 `combined_area`(并集)，语义略出入。
- L1/L2 缓存 key 已含 prompt（describe→question、extract→hint、measure→labels），**无"同图不同问命中脏缓存"风险**——比上游 ds-vision-skill 更严谨。

## 6. 环境陷阱

- **`D:` 盘无法承载 L2 SQLite 缓存**：modernc.org/sqlite 在该卷报 `SQLITE_READONLY_DBMOVED`(776) / disk I/O error；`C:` 盘正常。默认 `cache_dir` 是 `~/.llm-eyes-mcp`（C 盘）不受影响，但手动指向 D 盘会踩雷。
- 无 gcc，`go test -race` 不可用（项目零 CGO，不影响交付；需则先装 MinGW）。
- Git Bash 给原生 Windows 二进制传路径要用 `$(pwd -W)` 转 `D:/...`，`/d/...` 不被识别。
- 本 shell wrapper 对超长命令 / 多段 heredoc 链式调用不稳定（返回空输出），拆成小步执行更可靠。
- 此 shell 下 `sleep` 不可用；PowerShell `Start-Sleep` / `python time.sleep` 被沙箱中断；可用 `ping -n 90 127.0.0.1` 作等待（Git Bash 调 Windows ping）。

## 7. 外部依赖 / 工具生态笔记

- **gh**：GitHub CLI v2.97.0，安装于 `C:\Program Files\GitHub CLI\gh.exe`；需新开终端才有 `gh`，首次 `gh auth login`（浏览器 OAuth）。
- **MCP 官方 spec 权威入口**（勿凭记忆答细节）：
  人读 `https://modelcontextprotocol.io/specification/<版本>`；机器真相 = `github.com/modelcontextprotocol/modelcontextprotocol` 的 `schema/<版本>/schema.ts`（+ `schema.json`），raw 直读可用。
  版本目录按号分，**必须查自己声明的版本**（不同版本 union 成员不同：`audio`、`resource_link` 均为后加）。
  WebFetch 对同 URL 有 15 分钟缓存，反复查加 `?cb=xxx`；sandbox 内 curl 取 GitHub 返回空，用 WebFetch。
- **ds-vision-skill 回借评估（全判不借）**：端口探测/跨 provider 降级/PDF 通道、README 隐私声明、result provenance、confidence 字段，经 YAGNI + 用户拍板均不借（理由详见 `MEMORY.md` 原文，此处略）。

## 8. MCP 2026-07-28 新规范跟进

- 评估结论（`docs/mcp-跟进评估.md`）：**不必立刻跟进**，纳入 Q3–Q4 路线图，观察 1–2 个月（避开 errata 期）后启动。
- 当前架构（STDIO + 无状态）与新规范"无状态核心"天然契合，**未触及任何废弃功能**（roots/sampling/logging/SSE/Tasks 均未实现），享 12 个月废弃窗口。
- 主要迁移点：移除 `initialize` 握手 + 新增 `server/discover` + `_meta` 改造 + result 补 `resultType`。估算 3–5 人日。
- 已核对最新 schema：`ContentBlock` union 与 2025-06-18 一致（text/image/audio/resource_link/resource），无 `image-uri` 变体，`ImageContent` 仍是 base64 内联 → **将来迁移 content 层无需改动，迁移面仅在握手层**。

## 9. 推广（短视频）—— 已 pivot 的历史记录

- 目标：为 llm-eyes-mcp 推广，主投抖音，KPI = 涨 GitHub stars。
- 早期重资产方案（已弃）：钩子片头 / 图生视频 / B-roll 合成。技术调研有参考价值的点：
  - AI 视频文字乱码是全模型通病；最优解 = 生成阶段不放文字，文字全后期剪映叠加；辅以 no-text 约束。
  - agnes-video-v2.0 的 MCP schema 最初未暴露 aspect_ratio/width/height（`additionalProperties:false`），9:16 只能塞进 prompt 文本被忽略 → 产出偏横屏。规格书推动其补 `aspect_ratio`+`resolution`（行业主流；`width/height` 是少数低层备选）。
  - VideoGen 与 agnes 生成视频**均有音轨**（此前误判 agnes 无声是播放器配置问题，已纠正）。
- 最终 pivot（用户决策）：改走极简 MVP —— **仓库截图 + 爆款文案 + 热门 BGM** 直接发抖音。
  取舍：用发布频次/速度换制作精度，对"核心稳定、目标涨 star"的工具，速度 > 精修。
- 仍保留的硬约束：视频内不挂外链（限流）；GitHub 入口只放评论区置顶 + 主页简介（"GitHub 搜 llm-eyes-mcp"）；文案即钩子（前 3 秒靠文字/画面冲突）。
- 本地产物（`generated-images/`、`generated-videos/`，均已 `.gitignore`）：含 agnes 写实风静帧、VideoGen/agnes 样片，仅作历史参考。

## 归档取舍

- **保留**：项目约定、根因/修法（长期复用）、环境陷阱、生态工具笔记、规范迁移结论、推广最终决策与仍生效的约束。
- **剔除**：逐次任务的实时状态更新（"已生成 2 条""轮询中""是否重试"）、可重新推导的执行过程、已过期或一次性远端 URL（COS/TOS 签名链接）、中间误判及其纠偏轨迹（仅保留纠偏后的正确结论）。
