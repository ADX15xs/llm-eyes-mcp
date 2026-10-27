# Handoff — uoma vision 组现状与验收指引（2026-10-28）

来源：uoma 网关侧已完成「关思考 + 非流式超时拆表」落地与验收（9 commits，本文件是其消费端交接）。
网关进程运行中：`100.69.40.56:4000`，`/health` 200。

## 0. TL;DR

| 项 | 状态 |
|---|---|
| vision-glm（默认组） | ✅ 可用。思考已关（`nvext.max_thinking_tokens:0`），生产 10/10 验证 rt=0 |
| vision-qwen | ⚠️ 名义可用实际单点：ms 余额不足（429 insufficient balance）、amd 有一次成功样本但并发 429 频繁（已改 15s 短冷却）。**验收走 vision_glm，别切 vision_qwen** |
| 非流式预算 | mt=2048 → 90s（客户端 240s 够两次故障转移）；mt=256 → 30s。流式表未动 |
| 已知慢源 | vision-glm/amd（weight 0.05，不支持图像，仅文本兜底，trivial 也要 ~50s） |

## 1. 怎么发请求

llm-eyes 的 `internal/vlm/openai.go` 已对齐（`stream:false` 硬编码、不传思考参数——思考开关由网关
extraBody 注入，客户端发了也会被网关白名单剥掉并 WARN，这是设计）。要点：

- **Endpoint**：`http://100.69.40.56:4000/v1/chat/completions`
- **Auth**：`Authorization: Bearer ${UOMA_API_KEY}`（.env 已有）
- **model**：组名 `vision-glm`（网关组内路由：nim/nim-adx 各 weight 1.2 ≈ 50/50，amd 0.05）
- **stream**: false；**max_tokens**: 2048（思考已关，定位 JSON 200-400 tok 足够）
- **图像**：`content` 数组 `{type:"text"} + {type:"image_url", image_url:{url:"data:image/png;base64,..."}}`
- **不要发** thinking/enable_thinking/chat_template_kwargs——会被丢弃（现在会打一条 WARN）

最小验证脚本（node，从 llm-eyes-mcp 目录跑）：

```js
import fs from 'node:fs';
const key = (fs.readFileSync('.env', 'utf8').match(/^UOMA_API_KEY=([^\r\n]+)/m) || [])[1]?.trim();
const res = await fetch('http://100.69.40.56:4000/v1/chat/completions', {
  method: 'POST',
  headers: { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json' },
  body: JSON.stringify({
    model: 'vision-glm',
    messages: [{ role: 'user', content: 'Reply with the word: ok' }],
    max_tokens: 256, temperature: 0, stream: false,
  }),
  signal: AbortSignal.timeout(120000),
});
const j = await res.json();
console.log(res.status, j.choices?.[0]?.finish_reason,
  'rt=', j.usage?.completion_tokens_details?.reasoning_tokens,   // 必须 0
  JSON.stringify(j.choices?.[0]?.message?.content));
```

带图请求（定位类 prompt）预期形状：`content` 是裸 JSON（无 markdown fence），
`{"regions":[{"id":"...","bbox":[x0,y0,x1,y1],"text":"..."}]}`。

## 2. 预期响应锚点

| 场景 | 延迟 | 说明 |
|---|---|---|
| 纯文本小问（mt=256） | 1.2-3.7s | 10/10 实测，rt=0，finish=stop |
| 8 区域定位批（mt=2048） | ~6-16s | 关思考后 5.8s 均值（IoU 0.91）；思考开时是 27s+ |
| 整页大图（in≈7.3k） | ~9-16s | 网关侧实测 8.7s/15.9s |
| 最坏情形（预算打满） | <90s | 网关掐断换源，客户端 240s 内至少两次 attempt |

坐标口径注意：关思考后模型可能回**像素坐标**（违归一化指令）——`internal/tools/parse.go:216
normaliseCoords()` 已能自动识别（maxV>1.5 → 按 w/h 归一），**客户端无需改动**。

## 3. 异常形状速查（503/429 时看哪）

网关日志 `gateway/logs/YYYYMMDD.log`（`[req]` 行带 `budget=`；`[resp]` 行尾 `rec=<日期>/<记录>`
可直接跳原始留存：request.json=实际发出体 / response.sse=上游原样返回）：

| 响应 | 含义 | 动作 |
|---|---|---|
| 503 `...failed: timeout` | 上游 90s 预算内没回（已自动换源重试过） | 看 rec= 留存；偶发属正常，连续出现跑探针 |
| 503 `...failed: truncated` | 上游 200 但正文空（finish=length）——**思考态回归的信号** | 跑 `node D:/github-clone/uoma/gateway/scripts/probe-thinking-off.mjs vision-glm` |
| 503 `cooldown Ns` | 部署冷却中（Retry-After 头有数） | 等待即可 |
| 429 `provider_rate_gate` | 网关发送前速率门（nim rpm=40） | 批量验收间隔 >2s/发 |
| 429 `rate_limited` | 上游限流；amd 的 process_concurrency 现在只冻 15s | 自动恢复 |

## 4. llm-eyes 侧验收清单

1. `config.yml`：`default_provider: vision_glm`（已是）；`timeout_seconds: 240`（已大于网关预算，
   **mt 若上到 4096+ 需同步上调**——mt=8192 单次预算 276s 会超 240）。
2. 跑 `compare_e2e.mjs`：用**匹配的 (截图, spec) 对**——上一轮 e2e 的 0/5 是坐标系串了
   （yyhd_reward_section.png 750×446 vs frame7_spec.json 750×1176），不是定位能力问题；
   或直接用 `probe_grounding_accuracy.mjs` 的合成图口径（关思考 IoU 0.91）。
3. 确认 L2 缓存键里的响应 `reasoning_tokens=0`（思考关掉的直接证据）。
4. 批量验收（>10 发）间隔 ≥2s，避开 nim rpm=40 门。
5. 想复测思考开关：`node D:/github-clone/uoma/gateway/scripts/probe-thinking-off.mjs vision-glm`
   （三态判定 OFF / ACCEPTED-INEFFECTIVE / REJECTED，带 `__bogus__` 对照）。

## 5. 边界与遗留（网关侧立字据）

- ms（ModelScope）余额恢复后 vision-qwen 才复活，届时网关侧会先跑探针复验 ctk 再放开。
- vision-glm/amd 不会接图像流量（supportsImage:false + 400 实证），仅当两台 NIM 都冷却时
  作文本兜底，慢是常态。
- 网关侧遗留 7 条见 `D:/github-clone/uoma/.workbuddy/memory/2026-10-28.md`（含 L3 每日探针、
  mt 上移联动 timeout 等）。
