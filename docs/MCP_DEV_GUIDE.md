# MCP 服务端开发指导文档

> 来源：从 `media-mcp` 项目（Go，stdio JSON-RPC MCP Server）中抽取的通用开发规范与踩坑经验
> 适用：任何基于 Go 手写 MCP stdio 服务端的项目
> 说明：本文只沉淀 MCP 协议实现与工程规范，不包含任何具体业务逻辑

---

## 0. 快速红线（先看这 8 条）

| # | 红线 | 违反后果 |
| :-- | :--- | :--- |
| 1 | stdio 传输必须用 **NDJSON**（一行一条 JSON），禁止 `Content-Length` 帧格式 | 客户端握手失败/无响应 |
| 2 | **stdout 只允许输出 JSON-RPC 消息**，所有日志走 stderr | 一条 `fmt.Println` 就能让整个协议流崩溃 |
| 3 | 通知（notification）**没有 id，绝不能回响应** | 客户端收到孤儿响应，状态机错乱 |
| 4 | 业务失败用 `result.isError = true`，**不要**用 JSON-RPC `error` | Agent 拿不到失败原因，无法自我修复 |
| 5 | 单条消息解析失败只回错误，**不能退出主循环** | 一个畸形包直接杀死 server |
| 6 | 所有出站 HTTP 调用必须设 `Timeout` | 客户端永久挂起，无超时无重试 |
| 7 | 配置路径不能依赖 cwd，必须按优先级解析 | MCP 客户端启动进程时 cwd 不可控，读不到配置 |
| 8 | 真实配置/密钥文件必须 gitignore，只提交 `.example` | 密钥泄漏 |

---

## 1. 协议层规范

### 1.1 传输格式：NDJSON，不是 LSP 帧

MCP stdio 传输的标准格式是 **换行分隔的 JSON（NDJSON）**：一行一条完整 JSON 消息，行尾 `\n`。

`media-mcp` 早期误按 LSP 风格实现了 `Content-Length: N\r\n\r\n<body>` 的帧格式（提交 `61dadf1` 专门回退），额外维护了 `parseContentLength` / `consumeLine` 两个函数，最终整段删除。

**结论：不要自作聪明兼容 Content-Length。** 写入端：

```go
func (s *Server) write(v interface{}) {
    data, err := json.Marshal(v)
    if err != nil {
        fmt.Fprintf(os.Stderr, "marshal response: %v\n", err) // 失败也只能打 stderr
        return
    }
    data = append(data, '\n') // NDJSON：一条消息一行
    os.Stdout.Write(data)
    os.Stdout.Sync()          // 必须 flush，否则客户端可能读不到
}
```

读取端要点：

- 用 `bufio.NewReader(os.Stdin)` + `ReadString('\n')`，`ReadString` 会自动处理超长行；
- 兼容 `\r\n`：统一裁剪行尾的 `\r` 和 `\n`；
- **空行直接 `continue`**，不要当成解析错误；
- `io.EOF` 表示客户端关闭，正常退出循环，不是错误。

```go
for {
    line, err := reader.ReadString('\n')
    if err != nil {
        if err == io.EOF { break }
        return fmt.Errorf("read line: %w", err)
    }
    line = trimTrailing(line) // 去掉尾部所有 \r \n
    if line == "" { continue }
    // ... unmarshal & dispatch
}
```

### 1.2 stdout / stderr 严格分离

这是 stdio MCP 最容易踩的坑：**stdout 是协议通道，不是日志通道**。

- 协议消息 → `os.Stdout`
- 启动信息、进度、警告、错误 → `os.Stderr`，统一加 `[server-name]` 前缀便于客户端日志区分

```go
fmt.Fprintf(os.Stderr, "[my-mcp] Loading config from: %s\n", cfgPath)
fmt.Fprintf(os.Stderr, "[my-mcp] Registered tool: %s\n", name)
```

注意 `log.Fatalf` 默认输出到 stderr，可以安全用于启动期致命错误；但**运行期不要 Fatal**，单个请求出错不应该终止进程。

### 1.3 必须处理的方法集

最小可用集合如下，缺一个都可能导致某些客户端握手失败：

| 方法 | 类型 | 处理方式 |
| :--- | :--- | :--- |
| `initialize` | 请求 | 返回 protocolVersion / capabilities / serverInfo |
| `initialized` | 通知 | **静默忽略** |
| `notifications/initialized` | 通知 | **静默忽略** |
| `notifications/cancelled` | 通知 | **静默忽略**（或标记取消，但绝不回包） |
| `tools/list` | 请求 | 返回工具数组 |
| `tools/call` | 请求 | 执行工具，返回 content |
| `ping` | 请求 | 返回空对象 `{}` |
| 其他 | 请求 | 回 `-32601 Method not found` |

```go
switch msg.Method {
case "initialize":
    s.handleInitialize(msg)
case "initialized", "notifications/initialized", "notifications/cancelled":
    // 通知：无 id，不回任何东西
case "tools/list":
    s.handleToolsList(msg)
case "tools/call":
    s.handleToolCall(msg)
case "ping":
    s.sendResult(msg.ID, map[string]interface{}{})
default:
    s.sendError(msg.ID, -32601, "Method not found: "+msg.Method)
}
```

### 1.4 initialize 响应结构

```go
{
  "protocolVersion": "2024-11-05",     // 与客户端约定的版本，硬编码要与文档/测试一致
  "capabilities": { "tools": {} },     // 只声明真正实现的能力
  "serverInfo": { "name": "xxx-mcp-server", "version": "0.2.0" }
}
```

规范要求：

- `protocolVersion` 写成常量，测试中断言，避免升级时漏改；
- `capabilities` 只声明实现了的能力（只做 tools 就别声明 resources/prompts）；
- `serverInfo.version` 与二进制版本保持同源，便于线上排障。

### 1.5 ID 的三态语义

JSON-RPC 的 `id` 有三种状态：**有值 / 显式 null / 不存在**。用 Go 的 `int` 无法表达，必须自定义类型：

```go
type jsonNumber struct {
    Num  int
    Seen bool // 是否出现过
}

func (j *jsonNumber) MarshalJSON() ([]byte, error) {
    if !j.Seen { return []byte("null"), nil }
    return []byte(fmt.Sprintf("%d", j.Num)), nil
}
```

配合 `ID *jsonNumber` + `json:"id,omitempty"`：

- 解析错误（连 id 都读不出来）时传 `nil`，响应中 id 为 null，符合规范；
- 通知消息没有 id，响应结构里就不该出现 id 字段。

### 1.6 错误码与「两层错误」模型

| 场景 | 错误码 | 用法 |
| :--- | :--- | :--- |
| JSON 解析失败 | `-32700` | Parse error，id 传 nil |
| 方法不存在 | `-32601` | Method not found |
| 参数结构非法 | `-32602` | Invalid params（如 params 不是对象） |
| 工具不存在 / 内部异常 | `-32603` | Internal error |

**关键区分：协议错误 ≠ 业务错误。**

- 协议错误（上表）→ 走 JSON-RPC `error` 字段；
- 业务执行失败（外部 API 报错、超时、限流）→ **走正常 `result`**，用 `isError: true` + `content` 里的文本说明：

```go
s.sendResult(id, map[string]interface{}{
    "content": []map[string]interface{}{{
        "type": "text",
        "text": fmt.Sprintf("Error executing xxx: %v", err),
    }},
    "isError": true,
})
```

这样 Agent 能读到具体错误文本并自行调整参数重试；如果塞进 JSON-RPC error，多数客户端只会把它当传输故障抛出。

### 1.7 并发模型的取舍

`media-mcp` 采用单 goroutine 顺序处理：读一条、处理一条、写一条。优点是 stdout 写入天然串行、无需加锁；代价是：

- **一个耗时工具调用会阻塞后续所有请求**（包括 `ping`），客户端可能误判超时。

注意：该项目 `Server` 结构体里留了一个 `mu sync.Mutex` 但从未使用——这是典型的「预留但没用上」死代码。规范要求：

- 若坚持顺序模型，删掉未使用的锁，并在文档中明示「工具调用是串行的」；
- 若要并发处理（推荐用于有长任务的场景），必须为每个请求起 goroutine，并**用互斥锁保护 `write()`**，否则多条 JSON 会交错写入 stdout 造成流损坏。

---

## 2. 工具（Tool）设计规范

### 2.1 命名

- 格式固定、可预测，例如 `{namespace}_{action}`；
- 全局唯一，且**跨版本稳定**（工具名变更等于破坏性变更，Agent 侧的提示词/缓存都会失效）；
- 只用 `[a-zA-Z0-9_]`，不要用中文、空格、连字符。

### 2.2 inputSchema 必须精确

工具的 `description` 和参数 `description` 是 Agent **唯一**的选型依据，写得含糊 = 调用错工具。

```go
"inputSchema": map[string]interface{}{
    "type": "object",
    "properties": map[string]interface{}{
        "prompt": map[string]interface{}{
            "type":        "string",
            "description": "The prompt to ...",       // 必填项：说明用途
        },
        "model": map[string]interface{}{
            "type":        "string",
            "description": "Optional: model name",    // 可选项：统一以 "Optional:" 开头
        },
    },
    "required": []string{"prompt"},                   // 只列真正必填的
},
```

规范：

- 必填项尽量少（1~2 个），其余全部可选并有服务端默认值；
- 可选参数的 description 统一以 `Optional:` 起头，Agent 更容易判断；
- 数值参数写清单位（`duration in seconds`）和示例（`e.g. 2752x1536`）。

### 2.3 参数解析必须防御性

JSON 反序列化成 `map[string]interface{}` 后，**所有数字都是 `float64`**，且任何字段都可能缺失或类型错误。禁止直接类型断言取值，统一走 helper：

```go
func getString(m map[string]interface{}, key string) string {
    if v, ok := m[key]; ok {
        if s, ok := v.(string); ok { return s }
    }
    return "" // 缺失/类型错 → 零值，不 panic
}

func getInt(m map[string]interface{}, key string) int {
    if v, ok := m[key]; ok {
        switch n := v.(type) {
        case float64: return int(n) // JSON number 默认是 float64
        case int:     return n
        }
    }
    return 0
}
```

指针型可选参数（需要区分「没传」和「传了 0」）单独处理：

```go
if seed, ok := args["seed"]; ok {
    if n, ok := seed.(float64); ok {
        v := int(n)
        req.Seed = &v
    }
}
```

### 2.4 空值必须回退到配置默认值

真实踩坑（提交 `adc58cb`）：Agent 调用时不传 `model`，服务端把空字符串直接透传给下游 API，导致 400。

**规范：每个可选参数都要有「请求值 → 适配器配置值 → 内置兜底」的三级回退链。**

```go
if req.Model != "" {
    payload["model"] = req.Model
} else if h.Model != "" {
    payload["model"] = h.Model
}
n := req.N
if n <= 0 { n = h.N }
```

### 2.5 返回 content 的构造

`content` 是数组，可混合多种类型。推荐组合：**先给一条人类/Agent 可读的 text 摘要，再给结构化条目**。

```go
content := []map[string]interface{}{
    {"type": "text", "text": "摘要：结果数量、实际使用的参数、耗时等"},
}
// 远程资源
content = append(content, map[string]interface{}{
    "type": "resource",
    "resource": map[string]interface{}{
        "uri":      url,
        "mimeType": mimeTypeFromURL(url),
    },
})
```

要点：

- 文本摘要里带上**实际生效的参数**（真正使用的 model、尺寸等），便于 Agent 校验自己的意图是否被满足；
- 即使结果为空也要给一条 text（`"No results."`），不要返回空 content 数组。

**⚠️ 已知缺陷（勿照抄）**：`media-mcp` 在返回本地文件时写成了

```go
fileData, _ := os.ReadFile(path)
content = append(content, map[string]interface{}{
    "type": "image",
    "data": string(fileData), // ❌ 原始字节直接转 string
    "mimeType": ...,
})
```

MCP 的 `image` content 中 `data` 字段**必须是 base64 字符串**。正确写法是 `base64.StdEncoding.EncodeToString(fileData)`。同时要注意大文件 base64 会急剧膨胀上下文，超过阈值应改为返回本地路径或 `resource` URI。

### 2.6 mimeType 推断要处理真实 URL

从 URL 猜 MIME 时（提交 `90a8041` 的修复点）必须处理：

- **query string 和 fragment**：`https://cdn.x.com/a.png?token=abc` → 先按 `?#` 截断；
- **大写扩展名**：`.PNG` → 先 `strings.ToLower`；
- **无扩展名** → 回退 `application/octet-stream`，不要猜。

```go
func mimeTypeFromURL(rawURL string) string {
    u := rawURL
    if i := strings.IndexAny(u, "?#"); i >= 0 { u = u[:i] }
    u = strings.ToLower(u)
    switch {
    case strings.HasSuffix(u, ".png"): return "image/png"
    // ...
    default: return "application/octet-stream"
    }
}
```

### 2.7 动态工具注册

工具列表可以由配置驱动动态生成（启用哪些后端就暴露哪些工具）。两条约束：

- **顺序必须确定**：Go map 遍历是随机的，生成前先对 key 排序（`sort.Strings`），否则 `tools/list` 每次顺序不同，破坏客户端缓存与测试稳定性；
- **允许空列表**：一个工具都没有时 `tools/list` 要正常返回 `{"tools": []}`，不能报错。

---

## 3. 配置与密钥管理

### 3.1 文件布局

```
config.yml.example   # 模板，进 git，含全部字段说明与注释
config.yml           # 真实配置，gitignore
.env.example         # 环境变量模板，进 git
.env                 # 真实密钥，gitignore
```

`.gitignore` 至少包含：`build/`、`*.exe`、`.env`、`config.yml`。

### 3.2 配置路径解析优先级（关键）

MCP 客户端拉起服务端进程时，**工作目录完全不可控**，用相对路径读配置几乎必然失败。必须按优先级逐级回退：

```go
cfgPath := "config.yml"
switch {
case len(os.Args) > 2 && os.Args[1] == "--config":
    cfgPath = os.Args[2]                       // 1. 命令行显式指定
case os.Getenv("MY_MCP_CONFIG") != "":
    cfgPath = os.Getenv("MY_MCP_CONFIG")       // 2. 环境变量
default:
    if exe, err := os.Executable(); err == nil {
        c := filepath.Join(filepath.Dir(exe), "config.yml")
        if _, err := os.Stat(c); err == nil { cfgPath = c } // 3. 可执行文件同目录
    }
    if _, err := os.Stat(cfgPath); err != nil {
        cfgPath = filepath.Join(".", "config.yml")          // 4. 当前目录兜底
    }
}
fmt.Fprintf(os.Stderr, "[my-mcp] Loading config from: %s\n", cfgPath) // 必须打日志
```

最终选定的路径**一定要打到 stderr**，否则线上排查「为什么配置没生效」会非常痛苦。

### 3.3 环境变量展开

配置文件里写 `${VAR_NAME}`，加载时统一正则替换：

```go
var envVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func expandEnvVars(data []byte) []byte {
    return envVarRe.ReplaceAllFunc(data, func(match []byte) []byte {
        key := match[2 : len(match)-1]
        if val := os.Getenv(string(key)); val != "" { return []byte(val) }
        return match // 未设置：保留原样
    })
}
```

两个注意事项：

1. **缺失变量保留原值**的策略会让 `${FOO}` 这个字符串通过「非空」校验，把错误推迟到运行时才暴露。若追求 fail-fast，应在校验阶段额外检查「值是否仍匹配 `${...}` 模式」并直接报错。
2. **文档不要承诺没实现的语法**。该项目 README 写了支持 `${VAR:-default}` 默认值语法，但正则并不支持——文档与实现不一致比没文档更危险。写进 README 前先加测试。

### 3.4 启动时校验（fail fast）

配置加载后立刻校验，缺字段直接退出，不要拖到第一次工具调用：

```go
for name, s := range cfg.Items {
    if !s.Enabled { continue }              // 只校验启用项
    if s.APIKey == ""  { return fmt.Errorf("%q: api_key is required", name) }
    if s.BaseURL == "" { return fmt.Errorf("%q: base_url is required", name) }
    if s.Type != "a" && s.Type != "b" { return fmt.Errorf("%q: invalid type", name) }
}
```

错误信息里**必须带上出错的配置项名称**。

### 3.5 鉴权方式抽象

不同下游的鉴权头五花八门，抽象成配置项而不是硬编码：

```yaml
auth_method: bearer        # bearer(默认) | basic | custom_header
custom_header: X-Api-Key   # auth_method=custom_header 时生效
headers:                   # 额外静态头
  X-Region: cn
```

```go
func setAuthHeader(req *http.Request, cfg *Config, apiKey string) {
    switch {
    case cfg.AuthMethod == "bearer" || cfg.AuthMethod == "":
        req.Header.Set("Authorization", "Bearer "+apiKey)
    case cfg.AuthMethod == "basic":
        req.Header.Set("Authorization", "Basic "+apiKey)
    case cfg.CustomHeader != "":
        req.Header.Set(cfg.CustomHeader, apiKey)
    default:
        req.Header.Set("Authorization", "Bearer "+apiKey) // 兜底
    }
}
```

另外预留一个 `extra: map[string]interface{}` 字段承接各后端的私有参数，避免每加一个后端就改一次结构体。

---

## 4. 架构分层与可扩展性

### 4.1 目录分层

```
cmd/<app>/main.go         # 仅做：解析路径 → 加载配置 → 构建能力 → 启动 server
internal/config/          # 配置结构 + 加载 + 展开 + 校验
internal/mcp/             # 协议层：JSON-RPC/stdio，不含任何业务
internal/<domain>/        # 业务能力：接口定义 + 注册表 + 各实现
```

**硬性要求：协议层不 import 业务实现，只依赖业务包暴露的接口。** 这样协议层可以用 mock 完整测试。

### 4.2 接口 + 注册表 + init 自注册

```go
// 1. 定义窄接口
type Handler interface {
    Name() string
    Do(req Request) *Result
}

// 2. 全局注册表（仅 init 期写入，运行期只读，无需加锁）
var defaultRegistry = NewRegistry()
func Register(name string, fn builder) { defaultRegistry.Register(name, fn) }

// 3. 各实现文件里 init() 自注册
func init() { Register("foo", newFoo) }

// 4. 未注册的名字回退到通用实现
func (r *Registry) buildOne(name string, cfg *Config) (Handler, error) {
    if fn, ok := r.impls[name]; ok { return fn(cfg) }
    return NewGenericAdapter(name, cfg), nil // 兜底
}
```

这个演进路径值得记：项目最初在 `main.go` 里用一个大 `switch` 分发（新增一个后端要改 main.go），后来（提交 `61480f4`）重构为「注册表 + init 自注册 + 通用兜底」，`main.go` 从 122 行降到 69 行，新增后端**零改动主流程**。

### 4.3 部分失败不致命

构建多个能力时，单个失败只收集错误并跳过，不要中断整体：

```go
func (r *Registry) BuildAll(cfg *GlobalConfig) ([]Handler, []error) {
    names := sortedKeys(cfg.Items) // 排序保证确定性
    var handlers []Handler
    var errs []error
    for _, name := range names {
        if !cfg.Items[name].Enabled { continue }
        h, err := r.buildOne(name, cfg.Items[name])
        if err != nil { errs = append(errs, fmt.Errorf("%q: %w", name, err)); continue }
        handlers = append(handlers, h)
    }
    return handlers, errs
}
```

main 里：错误逐条打 stderr 警告，**全部失败才 Fatal**。

```go
for _, err := range buildErrs {
    fmt.Fprintf(os.Stderr, "[my-mcp] Warning: %v\n", err)
}
if len(handlers) == 0 {
    log.Fatal("No enabled handlers could be built. Please check config.yml")
}
```

### 4.4 通用兜底适配器

为「遵循常见响应格式」的下游提供一个 generic adapter，**新接入无需写代码，只加配置**。这是把接入成本从「50~80 行 Go」降到「10 行 YAML」的关键设计。

配套要求：generic adapter 的字段名要可配置（如 `status_field`、`result_field`），并对响应结构做宽容解析。

---

## 5. 外部调用与长耗时任务

### 5.1 HTTP 客户端必须设超时

`&http.Client{}` 零值**没有超时**，一旦下游挂起，MCP 客户端就永久等待。按场景分层设置：

| 场景 | 建议超时 |
| :--- | :--- |
| 快速查询 / 任务提交 | 30~60s |
| 轮询单次请求 | 30s |
| 同步长任务 | 180s |

```go
client := &http.Client{Timeout: 60 * time.Second}
```

### 5.2 异步任务的轮询模板

对「提交返回 task_id、之后轮询状态」的下游，三个参数缺一不可，且**都要可配置**：

```go
initialWait     time.Duration // 首次等待，避免刚提交就狂查（如 90s）
pollInterval    time.Duration // 轮询间隔（如 5s）
maxTotalTimeout time.Duration // 总超时上限（如 15min）
```

```go
time.Sleep(h.initialWait)
start := time.Now()
for {
    if time.Since(start) >= h.maxTotalTimeout {
        return errResult(fmt.Errorf("timeout after %v (task_id=%s)", h.maxTotalTimeout, taskID))
    }
    // 单次查询失败（网络抖动/5xx）→ sleep 后重试，不要立即放弃
    // 明确的失败状态 → 立即返回错误
    // 成功 → 返回结果
    time.Sleep(h.pollInterval)
}
```

配套规范：

- 轮询进度打到 **stderr**，让用户知道服务没死：
  `fmt.Fprintf(os.Stderr, "[my-mcp] task [%s]: %s (progress: %.0f%%)\n", taskID, status, progress)`
- 超时错误信息里**必须带 task_id**，便于事后去下游控制台查证；
- 总超时要小于 MCP 客户端的调用超时，否则客户端先断开、轮询白跑。

### 5.3 错误信息截断

下游返回的错误体可能是几十 KB 的 HTML，原样塞进 content 会污染 Agent 上下文：

```go
func truncate(s string, max int) string {
    if len(s) <= max { return s }
    return s[:max] + "...[truncated]"
}

// HTTP 非 2xx
return errResult(fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200)))
```

建议：主调用截断到 200 字符，轮询截断到 100 字符。

> 注意：`truncate` 按字节切片，对中文会切出乱码半字符。若错误信息可能含中文，改用 `[]rune` 切分。

### 5.4 响应解析要宽容

- 状态码非 2xx 先返回带响应体片段的错误，再谈解析；
- 下游可能在 HTTP 200 里塞 `{"error": "..."}`，解析后要单独判空；
- 用「结构体只声明关心的字段」的方式解析，下游加字段不会导致解析失败；
- 多种可能的返回形态（同步直返 vs 异步 task_id）在同一个入口里判别，不要分裂成两个工具。

---

## 6. 错误处理与日志

- 一律用标准 `error`，跨层用 `%w` 包装并带上下文：`fmt.Errorf("read config %s: %w", path, err)`；
- 结果结构体内嵌 `Error error` 字段，把「调用失败」当作正常返回值向上传递，由协议层统一转成 `isError`；
- 结果结构体回显原始请求（`Request` 字段），便于日志与追踪；
- **运行期任何单请求错误都不允许 `os.Exit` / `log.Fatal`**；
- 启动期的致命错误才用 `log.Fatalf`，且信息要可执行（告诉用户去改哪个文件）。

---

## 7. 测试规范

协议层必须有测试，否则每次改动都是在赌客户端能不能握手。

### 7.1 stdio 测试脚手架

用 `os.Pipe()` 替换 `os.Stdin` / `os.Stdout`，在 goroutine 里跑 `server.Start()`：

```go
stdinReader, stdinWriter, _ := os.Pipe()
stdoutReader, stdoutWriter, _ := os.Pipe()
origStdin, origStdout := os.Stdin, os.Stdout
os.Stdin, os.Stdout = stdinReader, stdoutWriter
// defer 恢复 origStdin / origStdout

go func() { serverErr <- server.Start() }()
```

读响应必须带超时，否则断言失败时测试会永久挂起：

```go
select {
case r := <-ch:
    // unmarshal & assert
case <-time.After(5 * time.Second):
    t.Fatal("timeout reading response")
}
```

### 7.2 必测清单

| 用例 | 断言点 |
| :--- | :--- |
| `initialize` | jsonrpc/id/protocolVersion/serverInfo/capabilities 全字段 |
| initialize 后的通知 | 收到 `notifications/initialized` 且**无 id** |
| `tools/list` | 工具数量、名称、inputSchema.properties、required |
| `tools/list` 空注册 | 返回 `tools: []` 不报错 |
| `tools/call` 正常 | `isError=false`，content[0].type=text，含关键结果 |
| `tools/call` 业务失败 | **无 JSON-RPC error**，但 `isError=true` 且文本含原因 |
| `tools/call` 未知工具 | error code `-32603` |
| `tools/call` 非法 params | error code `-32602` |
| 未知方法 | error code `-32601` |
| 畸形 JSON | error code `-32700` |
| 纯通知消息 | 不产生任何响应（用超时反证） |
| 参数透传 | mock 里捕获请求对象，逐字段比对 |
| 多实现共存 | 调用 A 只命中 A，调用 B 只命中 B |

### 7.3 其他约定

- 业务实现用 mock（函数字段注入）而非真实网络调用；
- 配置测试用 `t.TempDir()` 写临时 yaml、`t.Setenv()` 注入环境变量，天然隔离且自动清理；
- 纯函数（mime 推断、类型转换、字符串裁剪）走表驱动测试，把边界值（大写、query string、空值、负数、类型错）全列出来；
- `make test` = `go test -v ./...`，CI 必跑。

---

## 8. 工程化与交付

### 8.1 依赖最小化

`media-mcp` 全项目只有一个第三方依赖（YAML 解析），MCP 协议层完全用标准库手写。收益：编译快、二进制小、无供应链风险、跨平台零折腾。

**建议：除非协议特性复杂到手写不划算，否则 stdio + tools 场景优先手写。**

### 8.2 Makefile 与交叉编译

单文件二进制是 MCP 服务端的最佳交付形态。标准目标：

```makefile
build:   go build -o build/$(APP) ./cmd/$(APP)
run:     go run ./cmd/$(APP)
test:    go test -v ./...
clean:   rm -rf build/
windows: GOOS=windows GOARCH=amd64 go build -o build/$(APP)-windows-amd64.exe ./cmd/$(APP)
linux:   GOOS=linux   GOARCH=amd64 go build -o build/$(APP)-linux-amd64 ./cmd/$(APP)
darwin:  GOOS=darwin  GOARCH=amd64 go build -o build/$(APP)-darwin-amd64 ./cmd/$(APP)
all:     clean build windows linux darwin
```

### 8.3 客户端接入配置

在 README 中给出可直接复制的客户端配置，并强调 **command 用绝对路径**（cwd 不可控）：

```json
{
  "mcpServers": {
    "my-mcp": {
      "command": "/absolute/path/to/build/my-mcp",
      "args": ["--config", "/absolute/path/to/config.yml"],
      "env": { "MY_API_KEY": "sk-xxx" }
    }
  }
}
```

### 8.4 文档同步维护

项目里保留一份 `AGENTS.md`（给 AI 协作者看的项目速查：语言/依赖/入口/命令/架构/约定），但要注意**文档漂移**：

- 该项目 `AGENTS.md` 至今仍写着「main.go 用显式 switch 创建适配器」，而代码早已重构为注册表自注册；
- README 声称支持 `${VAR:-default}` 语法，实现并不支持。

**规范：任何改动架构、约定、命令的提交，必须在同一个 commit 内更新 AGENTS.md / README。** 重构提交尤其容易漏。

---

## 9. 上线前检查清单

**协议层**
- [ ] 输出为 NDJSON，每条消息以 `\n` 结尾并 `Sync()`
- [ ] stdout 无任何非协议输出（`grep` 一遍 `fmt.Print`、`println`、`log.Print`）
- [ ] `initialize` / `tools/list` / `tools/call` / `ping` 全部实现
- [ ] 三类通知静默忽略，不回响应
- [ ] 四种错误码（-32700/-32601/-32602/-32603）路径都有测试
- [ ] 业务失败走 `isError`，不走 JSON-RPC error
- [ ] 畸形消息不会终止主循环，EOF 正常退出

**工具层**
- [ ] 工具名唯一稳定，schema 的 description 准确、可选项标注 `Optional:`
- [ ] 参数解析全部走防御性 helper，JSON number 按 float64 处理
- [ ] 每个可选参数都有「请求 → 配置 → 内置」回退链
- [ ] content 至少含一条 text 摘要；image 的 data 是 base64
- [ ] 工具列表顺序确定（map key 已排序）

**配置与安全**
- [ ] 配置路径四级回退，选定路径打 stderr
- [ ] `config.yml` / `.env` 已 gitignore，`.example` 已提交
- [ ] 启动时校验必填项，错误信息含配置项名
- [ ] 日志与错误文本中不回显完整密钥

**健壮性**
- [ ] 所有 `http.Client` 设置了 Timeout
- [ ] 长任务有 initialWait / pollInterval / maxTotalTimeout 且可配置
- [ ] 错误响应体已截断
- [ ] 单个后端构建失败只警告，全部失败才退出

**工程**
- [ ] `go test ./...` 全绿
- [ ] `make all` 三平台交叉编译通过
- [ ] README 含可直接复制的客户端接入配置（绝对路径）
- [ ] AGENTS.md / README 与当前代码一致

---

## 10. 踩坑速查表

| 现象 | 根因 | 处置 |
| :--- | :--- | :--- |
| 客户端连上后无任何响应 | 用了 Content-Length 帧格式 | 改 NDJSON |
| 协议随机崩溃 / JSON 解析错 | 有日志写进了 stdout | 全部改 stderr |
| 客户端报「收到未知响应」 | 对通知回了响应 | 通知分支留空 |
| Windows 上重测总跑旧版 | 无扩展名与 `.exe` 并存，spawn/PATHEXT 总优先 `.exe` | 只保留 `make build` 的产物，或手动删 `bin/llm-eyes-mcp` |
| Agent 收到失败但无法重试 | 业务错误用了 JSON-RPC error | 改 `isError` + text |
| 下游报 400 缺参数 | 可选参数为空直接透传 | 加配置默认值回退 |
| `tools/list` 顺序每次不同 | 遍历了 Go map | 先 `sort.Strings` |
| 找不到 config.yml | 依赖了 cwd | 四级路径回退 + 打日志 |
| 服务永久挂起 | `http.Client` 无 Timeout | 显式设置超时 |
| 上下文被错误信息撑爆 | 未截断下游响应体 | `truncate(body, 200)` |
| 资源 mimeType 全是 octet-stream | 未处理 query string / 大写扩展名 | 截断 `?#` + `ToLower` |
| 图片在客户端渲染不出来 | `image.data` 不是 base64 | `base64.StdEncoding.EncodeToString` |
| 测试偶发永久挂起 | 读 pipe 无超时 | `select` + `time.After` |
| 一个工具卡住导致全部超时 | 单 goroutine 顺序处理 | 并发处理 + 写 stdout 加锁 |
| 文档写的功能实际不存在 | 重构未同步文档 | 同 commit 更新 AGENTS.md/README |
