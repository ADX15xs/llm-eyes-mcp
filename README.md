# llm-eyes-mcp

一个零依赖、零 CGO 的 **MCP stdio 服务器**，为纯文本 LLM 赋予"眼睛"。它基于
Model Context Protocol（模型上下文协议），通过 stdin/stdout 上的 NDJSON 通信，
对外暴露三个专注的工具：

| 工具 | 用途 | 预处理管线 |
|------|------|-----------|
| `measure_image` | 精确的像素几何：距离、对齐、面积。VLM 只提供**坐标**，所有运算都在 Go 中确定性完成。 | **hard**（无损，CLAHE + 锐化） |
| `describe_image` | 语义理解：图里有什么、布局、风格。 | **soft**（激进的 JPEG 降采样） |
| `extract_text` | OCR：提取图像中的原始文字。 | **soft** |

因为坐标来自模型、而数值来自代码，测量结果可复现，而非靠估算得出。

## 设计初衷

带视觉的 LLM 往往又贵又慢，而同一张图常常会被反复提问。`llm-eyes-mcp` 就架在智能体与
视觉后端之间，用一套多层缓存把开销压到最低——原始字节（L0）、预处理产物（L1）、VLM 响应（L2），
以及计算出的几何结果（L3）。于是同一张图的第二次提问几乎零成本，也不会再产生任何 API 调用。

## 灵感来源

`llm-eyes-mcp` 的灵感来自 [ds-vision-skill](https://github.com/Sorwcyra/ds-vision-skill)：
把图像转成文本或 JSON、缓存结果，从而给纯文本 LLM 装上一双眼睛。本项目把这个想法
重新落地为一个 MCP stdio 服务器，并在其基础上加入了确定性的测量能力（`measure_image`）。

## 构建

需要 Go 1.22+。二进制以 `CGO_ENABLED=0` 编译，因此是一个单一的静态文件，可在任何平台干净构建。

```bash
make build          # -> bin/llm-eyes-mcp  （Windows 上为 .exe）
make test           # 运行完整测试套件
make check          # 构建 + 校验配置与 provider，然后退出
make all            # 交叉编译 windows / linux / darwin
```

生成的二进制体积远小于 **20 MB**（仅依赖标准库 + `golang.org/x/image`
+ `gopkg.in/yaml.v3` + `modernc.org/sqlite`）。

## 配置

将 `config.yml.example` 复制为 `config.yml` 并编辑。密钥以 `${VAR}` 形式引用，
并从环境中展开（参见 `.env.example`）。配置解析顺序：

1. `--config <path>`
2. `$LLM_EYES_CONFIG`
3. 可执行文件旁的 `config.yml`
4. 工作目录下的 `./config.yml`

```bash
./bin/llm-eyes-mcp --check --config config.yml   # 部署前先校验
```

## 运行

服务器是一个长时间运行的 stdio 进程。由你的 MCP 客户端启动：

```bash
./bin/llm-eyes-mcp --config config.yml
```

## 接入 MCP 客户端

MCP 客户端以子进程方式启动服务器。对 `command`（二进制）和 `--config` 参数都**使用绝对路径**——
相对路径会基于客户端自身的工作目录解析，而那通常并不是你的目录。

### Reasonix (`config.toml`, `reasonix.toml`)

```toml
[[plugins]]
name    = "llm-eyes-mcp"
type    = "stdio"
command = "C:\\absolute\\path\\to\\llm-eyes-mcp.exe"
args    = ["--config", "C:\\absolute\\path\\to\\config.yml"]
env     = { GLM_API_KEY = "${GLM_API_KEY}" }
```

### Claude Desktop（`claude_desktop_config.json`）

```json
{
  "mcpServers": {
    "llm-eyes-mcp": {
      "command": "/absolute/path/to/llm-eyes-mcp",
      "args": ["--config", "/absolute/path/to/config.yml"]
    }
  }
}
```

### VS Code（`settings.json` → `mcp.servers`）

```json
{
  "mcp": {
    "servers": {
      "llm-eyes-mcp": {
        "command": "C:\\absolute\\path\\to\\llm-eyes-mcp.exe",
        "args": ["--config", "C:\\absolute\\path\\to\\config.yml"]
      }
    }
  }
}
```

### 工具接受的图片输入

每个工具都接受 `image_source`，可以是：

- 一个 `http(s)://` URL
- 绝对文件路径或 `file://` URI
- 一个 `data:` URI 或原始 base64
- 上一次调用返回的 **32 位 `image_id`**（最省——跳过重新上传并命中缓存，因为原始字节已归档在 L0）

## 缓存如何让它变便宜

| 层级 | 存储内容 | 后端 | 失效策略 |
|------|----------|------|----------|
| L0 | 原始字节 | 磁盘 | 永不（按内容寻址） |
| L1 | 按意图预处理后的产物 | 磁盘（24 小时，1 GiB LRU） | TTL / 空闲 / 字节预算 |
| L2 | 按 工具+模型+参数 的 VLM 响应 | SQLite（7 天，100 MiB） | TTL / 字节预算 / **凭据变更** |
| L3 | 计算出的几何结果 | 内存 LRU | 会话结束 |

如果 API key 或端点发生变化，L2 会被自动清空——来自不同后端的答案绝不能被复用。

## 项目结构

```
cmd/llm-eyes-mcp   入口：标志位、配置、provider、缓存、服务器装配
internal/
  config   配置加载 / 环境变量展开 / 校验 / 凭据指纹
  mcp      协议层：NDJSON 帧、并发分发、工具注册表
  imageio  通用图像加载器（URL / 文件 / data-uri / base64 / 归档 id）
  imgproc  纯 Go 预处理（CLAHE、锐化、缩放）
  vlm      视觉后端适配器（OpenAI 兼容 + mock）
  tools    三个工具 + 解析/几何/测量逻辑
  cache    四层缓存
  geometry 确定性几何运算
```

## 测试

测试采用表驱动，且**从不触及网络**——VLM 通过 `vlm.NewMock` 注入，缓存使用
`t.TempDir()`。运行全部测试：

```bash
make test
```

覆盖范围包括协议帧（见 `docs/MCP_DEV_GUIDE.md` 第 7 节）、坐标解析、四个缓存层级，
以及端到端的工具运行——断言重复调用会从缓存返回（mock 的 `CallCount` 保持为 1）。

## License

MIT.
