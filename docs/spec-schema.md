# compare_design 规格文件契约（spec schema v1）

`compare_design` 把一份机器可读的设计规格与一张实现截图对比，输出逐区域还原度报告。
规格文件由 Agent 侧的 D2C 导出转换器生成，把 D2C 工具导出的 raw-tree 数据转成
本契约定义的格式（转换器随宿主项目的语言实现）。llm-eyes-mcp **不感知**任何 D2C
工具的私有格式——它只认本文件定义的通用 schema。

## 分工（设计定版 2026-10-27）

```text
D2C 导出 (sloth raw-tree JSON)
   │  转换器（Agent 侧，宿主项目语言）
   ▼
spec JSON ──────────────┐
                        ▼
              compare_design (llm-eyes-mcp)
                        ▲
裁剪好的组件截图 ────────┘
```

- **Agent 侧**：裁剪/对齐截图；raw-tree → spec 转换
- **llm-eyes 侧**：区域定位（VLM）；取色 / 几何 / 文本对比（Go）；报告与缓存

## 文件格式

```json
{
  "version": 1,
  "image": { "width": 375, "height": 812 },
  "regions": [
    {
      "id": "btn-start-match",
      "desc": "底部通栏主按钮，圆角，橙色背景，白色文字",
      "bbox": [16, 700, 343, 756],
      "color": "#FF6600",
      "text": "开始匹配"
    }
  ]
}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `version` | 是 | 当前固定 `1` |
| `image` | 否 | spec 坐标空间的设计尺寸；截图实际尺寸不同时 bbox 会按比例缩放 |
| `regions[].id` | 是 | 唯一标识；VLM 回复按它对齐 |
| `regions[].desc` | 强烈建议 | **VLM 定位的唯一线索**：写清它长什么样、在哪里、有什么字 |
| `regions[].bbox` | 否 | `[x0,y0,x1,y1]`，spec 像素空间；缺省则跳过位置检查 |
| `regions[].color` | 否 | `#RRGGBB` 期望底色（纯色填充才有意义） |
| `regions[].text` | 否 | 期望文本（空白归一后精确比较） |

约束：区域数 1–50（超出请按组件切片）；每个区域至少有 `bbox`/`color`/`text` 之一；
`id` 不得重复。

## 对比语义

VLM 只负责「找到区域 + 逐字转录其中的文字」，其余全部由 Go 计算：

| 检查 | 触发条件 | 计算方式 | 判定 |
|---|---|---|---|
| 定位 | 总是 | VLM 归一化 bbox → 缩放回原图像素 | 未找到 → `missing` |
| 位置 | 有 `bbox` | IoU + 中心偏移 (px)，spec 坐标按 image 尺寸缩放 | IoU < 0.5 → `offset` |
| 颜色 | 有 `color` | 定位框内缩 20% 区域的平均 RGB（**原始像素**，不经预处理） | ΔE(欧氏) > 24 → `color_mismatch` |
| 文本 | 有 `text` | VLM 转录文本 vs `text`（空白归一后比较） | 不等 → `text_mismatch` |

区域 `status` 为 `ok` 或上述问题标记的 `+` 组合；`score` = ok 数 / 区域总数（Go 计算，
非模型评分）。

## 截图要求（Agent 侧职责）

- **组件级粒度**：一次对比一张裁剪后的组件截图 + 对应的 spec 切片，不要整页对整页
- 截图与 spec 坐标空间尽量同尺度（同视口宽度、@1x）；不同尺寸会按比例缩放 bbox
- 截图应为真实渲染结果（浏览器/devtools 截图），不要缩放后再压缩失真

## 缓存与重跑

- **L2**：VLM 定位回复与最终报告，键 = 原图 MD5 + spec 内容 SHA256 + model_version。同图同 spec 重复对比零 VLM 成本
- `refresh: true` 丢弃缓存强制重跑；换模型（`provider` 参数）天然隔离缓存
