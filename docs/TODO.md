# 技术债清单（TODO）

> 范围：从项目文档中**已显式记录**的未决事项提炼而成。源码（`*.go`）无 TODO/FIXME/HACK 标记。
> 按纯净原则"显式负债"格式记录：每项注明**为什么妥协**、**怎么还**、**何时还**。
> 区分三档：🔴 技术债（妥协产物，需偿还）/ 🟡 已知限制（非债，已说明）/ 🔵 演进方向（未来功能，非债）。

---

## 🔴 技术债

### T-1. 千分值坐标歧义

- **位置**：`internal/tools/parse.go` `normaliseCoords`（`measure_image` 坐标归一化）
- **原因（为什么妥协）**：VLM 回复的坐标值在「千分值(0..1000，Qwen-VL 系)」与「像素值」间按数值大小启发式二选一；图片长边 ≥~1000px 时千分值必落入像素区间被误判为像素，坐标约减半。消歧需要带外"坐标约定"知识，单靠数值无法区分；此前按优先级投入功能修复，此项**显式推迟**。
- **偿还方式**：给 provider 加 `coord_convention: thousandths|normalized|pixels` 配置字段并接入 prompt，由 VLM 显式声明其坐标语义，`normaliseCoords` 据此分支而非启发式。
- **何时还**：待定（无排期）。
- **影响**：仅影响"VLM 吐千分值 + 长边≥1000px 缩放图"场景；归一化[0,1]路径与像素路径（已修复）不受影响。

### T-2. compare_design 单区域批处理（上游 thinking 参数失效的绕行）

- **位置**：`internal/tools/compare.go` `compareBatchSize`（当前 =1）；`internal/tools/prompt.go` 单区域最小提示词
- **原因（为什么妥协）**：uoma 网关对 vision 请求忽略所有 thinking 禁用参数（`enable_thinking` / `chat_template_kwargs.enable_thinking` / `thinking:{type:disabled}` / `response_format` json_schema 均实测无效），多区域定位提示词把整个 token 预算烧在推理上、content 返回 NULL（finish=length）；加大 `max_tokens` 至 8192 只产生更久的推理。batch=1 + 最小单区域提示词是唯一验证可靠的组合（直连 3/3 成功）。2026-10-27 探针与日志归档于 `docs/archive/`。
- **偿还方式**：uoma 侧修复 thinking 透传后，调回 `compareBatchSize` 并重跑 E2E；或消费 `config.ProviderConfig.Extra`（现为死字段）透传后端参数，并把 batch size 做成 provider 级可配置。
- **何时还**：待 uoma 修复（跟踪中，2026-10-28 记账）。
- **影响**：请求量与图片流量 ×N（N=区域数）；首跑延迟约 20s/区域串行（29 区域 ≈10 分钟，README 已注明预期）。附带残留风险：单区域可靠性是经验结论，机制上无 `finish_reason=length` 防护——极长 desc 仍可能推理超限后整体失败。

---

## 🟡 已知限制（非债，已记录说明）

### R-1. 坐标归一化的"原始 vs 缩放"尺寸错配

- **位置**：`internal/tools/measure.go` `detectObjects` → `parseDetections`
- **状态**：✅ 已修复（`eb6a037` / `v0.1.1`），保留记录以防回退。
- **方案**：L2 缓存用自描述信封 `{text,w,h}` 记录"模型看到的尺寸"，fresh/L2 命中路径统一用该尺寸归一化。

### R-2. L3 几何缓存 key 不含 provider/model

- **位置**：`internal/cache/keys.go` `L3Key`；`internal/tools/measure.go` 命中顺序
- **状态**：✅ 已修复（`eb6a037` / `v0.1.1`）：key 加 `modelVersion`，`providerFor` 前置到命中检查之前（附带语义变更：不存在 provider + L3 命中现 fail-fast，此前静默返缓存）。

### R-3. 轻微项（未动，不影响运行）

- **位置**：见下，均经评估"不影响运行"，暂不投入
  - `internal/mcp/server.go`：未知工具/参数解析错误使用 `CodeInternalError`，语义偏（应为更精确错误码）
  - `internal/cache/manager.go`：`cache.Settings` 的 `SweepIdle` / `CredentialHash` 字段为死字段（实际由 `Manager.Startup` 参数传入）
  - `internal/geometry/geometry.go`：`Area` 多框时 `Value` 取面积和，而 `Details.combined_area` 为并集，两处语义略有出入

### R-4. 环境限制

- **`D:` 盘无法承载 L2 SQLite 缓存**（modernc.org/sqlite 报 `SQLITE_READONLY_DBMOVED` 776），默认 `cache_dir=~/.llm-eyes-mcp`（C 盘）不受影响，仅手动指向 D 盘踩雷。属运行时环境约束，非代码债。
- **无 gcc，`go test -race` 不可用**（项目零 CGO，不影响交付；需则先装 MinGW）。

---

## 🔵 演进方向（未来功能，非债）

以下摘自 `docs/TECH_SPEC.md` §13，属规划中的能力扩展，**非妥协产物，无需偿还**：

1. **绝对尺寸测量**：若需"螺丝钉实际多少毫米"，需标定板 + 专用测量工具（可作独立微服务，不编译进主 MCP）。
2. **软管道失败降级**：VLM API 超时时自动降级为仅返回几何数据。
3. **VLM 坐标质量监控**：无物理标定时精度取决于 VLM 坐标输出，可考虑多 VLM 交叉验证。
4. **强制触发场景**：若未来 Agent 生态需要"图片进来即预处理"，需客户端配合（当前不做，尊重 MCP 规范）。
5. **compare_design 有界并发定位**：区域已按单区域拆分，天然可并行；仓库当前无并发先例（无 errgroup/信号量模式），需独立设计并评估网关限流（vision_qwen 已 429），可把 29 区域首跑从 ~10 分钟压至 ~3 分钟。

---

## 已评估并延期（非债，有明确排期决策）

### D-1. MCP 2026-07-28 规范迁移

- **来源**：`docs/mcp-跟进评估.md`
- **结论**：不必立刻跟进，纳入 **Q3–Q4 路线图**，观察 1–2 个月（避开 errata 期）后启动。
- **依据**：STDIO + 无状态架构与新规范"无状态核心"天然契合，未触及任何废弃功能，享 12 个月窗口期；迁移点集中在握手层，估算 3–5 人日。
- **性质**：主动的排期决策，非隐性债——已记录评估、依据与窗口期。

### D-2. compare_design 上线验证（依赖 uoma 网关稳定）

- **来源**：2026-10-27/28 缓解修复会话（steel-all 审查后实施）
- **结论**：两项验证后置，待 uoma 网关变更结束再执行——① 5 区域 E2E（`compare_e2e.mjs` + `frame7_small.json`），确认 isError=false 且 `vlm_rounds`=区域数；② 真值基准：在现有 750x446 截图上手工标注 3-5 个可见区域生成 spec，跑 compare_design 断言每区域 IoU ≥ 0.5（此前"精度提升"仅有"返回合法 JSON"的实证，无"框是对的"的实证）。
- **依据**：uoma 正在改代码，网关行为不稳定期间跑网关依赖的验证，会把网关波动误判为代码回归。
- **性质**：主动排期决策（等待依赖稳定），非隐性债。注意 Frame 7 对齐截图（750x1176）仍缺失；真值基准不依赖它——`probe_grounding_accuracy.mjs`（仓库根，合成图 + DOM 真值）已可用，且其 2026-10-28 直连上游的结果（`docs/archive/grounding_accuracy.json`）显示 8 区域 iouMean 0.90、7/8 达标——NULL 问题出在 uoma 网关层而非上游模型本身。

---

*本清单与 `docs/archive/memory-archive.md` 同源；后者含完整上下文（根因细节、修法、测试覆盖），本文件仅摘录待办。新增技术债须显式记账于此，避免隐性债。*
