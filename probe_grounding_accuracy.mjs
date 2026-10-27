// 定位准确率实验：合成一张有真值 bbox 的截图，用 compare_design 的真实提示词测
// 「思考关掉 / 思考限预算 / 思考全开」三档的定位质量与延迟。
//
// 为什么必须合成图：现有 screenshots/yyhd_* 与 frame7_spec.json 不是一个坐标系
// （spec 是 750x1176 整页，截图是 750x446 裁切），拿它对分会把"盒子画错"和"坐标系不同"
// 混在一起——上一轮 e2e 的 0/5 就是这么来的。合成图的真值来自 DOM boundingBox()，无歧义。
//
// 用法：node probe_grounding_accuracy.mjs [reps]
// 结果：控制台表格 + grounding_accuracy.json

import fs from 'node:fs';
import path from 'node:path';
import { chromium } from 'playwright';

const ROOT = import.meta.dirname;
// 上游 key 在 uoma 仓库根的 .env 里（llm-eyes 自己的 .env 只有网关的 UOMA_API_KEY）
const ENV = {};
const keys = [
  path.join(ROOT, '.env'),
  path.join(ROOT, '..', 'uoma', '.env'),
];
for (const file of keys) {
  if (!fs.existsSync(file)) continue;
  for (const line of fs.readFileSync(file, 'utf8').split(/\r?\n/)) {
    const t = line.trim();
    if (!t || t.startsWith('#') || !t.includes('=')) continue;
    const i = t.indexOf('=');
    ENV[t.slice(0, i).trim()] = t.slice(i + 1).trim();
  }
}

const REPS = Number(process.argv[2] ?? 2);
// 与网关 vision-glm 部署同源：NIM 的 z-ai/glm-5.3-flash
const UPSTREAM = 'https://integrate.api.nvidia.com/v1/chat/completions';
const API_KEY = ENV.NVIDIA_API_KEY;
const MODEL = 'z-ai/glm-5.3-flash';

const W = 750;
const H = 450;

// 8 个待定位目标，模拟真实 UI 文案（含中文与数字，避免"能猜出来"）
const TARGETS = [
  { id: 'r1', text: 'Gift Name', label: '礼物名称' },
  { id: 'r2', text: 'Voucher x1', label: '代金券数量' },
  { id: 'r3', text: 'Not Reached', label: '未达成' },
  { id: 'r4', text: '1 Players', label: '一人团' },
  { id: 'r5', text: '3 Players', label: '三人团' },
  { id: 'r6', text: '10 Players', label: '十人团' },
  { id: 'r7', text: 'Claim', label: '领取按钮' },
  { id: 'r8', text: 'Expires 2026-11-30', label: '过期时间' },
];

const SYSTEM = `You are a region locator for a design-revert verification instrument. Your ONLY job is to locate regions described in the input and report their bounding boxes plus any text you see inside them.

Rules:
1. Reply with RAW JSON only. No markdown fences, no prose, no explanation.
2. Schema: {"regions":[{"id":"<region_id_from_spec>","bbox":[x0,y0,x1,y1],"text":"<text inside region>"}]}
3. Coordinates are NORMALISED floats in [0,1]: x0,y0 = top-left corner, x1,y1 = bottom-right corner. Origin is the top-left of the image.
4. Use EXACTLY the id strings the spec gives you, one entry per region. Never invent extra regions, never merge two.
5. The downstream system will compare your boxes against the spec and compute colour/text deltas in code. Accurate boxes matter more than anything else.
6. If a region is genuinely absent from this screenshot, still emit its entry with bbox [0,0,0,0] and text "".
7. Do not estimate distances, sizes or colours. Boxes and text only.`;

function locatePrompt() {
  let s = `Locate the following ${TARGETS.length} region(s) in this screenshot and return their bounding boxes plus any text inside. Return raw JSON only:\n`;
  for (const t of TARGETS) s += `  - id=${t.id} desc=TEXT ${t.label} text=${t.text}\n`;
  s += 'Return only: {"regions":[{"id":"...","bbox":[x0,y0,x1,y1],"text":"..."}]}';
  return s;
}

const HTML = `<!doctype html><html><head><meta charset="utf-8"><style>
  body{margin:0;width:${W}px;height:${H}px;background:#f4f6fb;font-family:Arial,"Microsoft YaHei",sans-serif}
  .grid{display:grid;grid-template-columns:repeat(4,1fr);gap:18px;padding:22px}
  .card{background:#fff;border-radius:10px;padding:14px;box-shadow:0 2px 6px rgba(0,0,0,.12);position:relative;height:150px}
  .card .name{font-size:15px;color:#8a5a2b;font-weight:700}
  .card .qty{font-size:13px;color:#2b6e8a;margin-top:6px}
  .card .state{font-size:12px;color:#c0392b;margin-top:6px}
  .card .btn{position:absolute;bottom:12px;right:12px;background:#e8452c;color:#fff;font-size:12px;padding:5px 12px;border-radius:14px}
  .foot{padding:0 22px;display:flex;justify-content:space-between;font-size:12px;color:#555}
</style></head><body>
  <div class="grid">
    ${[
      ['Gift Name', 'Voucher x1', 'Not Reached'],
      ['Gift Name', 'Voucher x2', '1 Players'],
      ['Gift Name', 'Voucher x3', '3 Players'],
      ['Gift Name', 'Voucher x5', '10 Players'],
    ]
      .map(
        ([name, qty, state]) => `<div class="card">
      <div class="name"><span data-t="Gift Name">${name}</span></div>
      <div class="qty"><span data-t="${qty}">${qty}</span></div>
      <div class="state"><span data-t="${state}">${state}</span></div>
      <div class="btn"><span data-t="Claim">Claim</span></div>
    </div>`,
      )
      .join('\n    ')}
  </div>
  <div class="foot"><span><span data-t="Expires 2026-11-30">Expires 2026-11-30</span></span><span>Terms apply</span></div>
</body></html>`;

function iou(a, b) {
  const x0 = Math.max(a[0], b[0]);
  const y0 = Math.max(a[1], b[1]);
  const x1 = Math.min(a[2], b[2]);
  const y1 = Math.min(a[3], b[3]);
  const inter = Math.max(0, x1 - x0) * Math.max(0, y1 - y0);
  const areaA = Math.max(0, a[2] - a[0]) * Math.max(0, a[3] - a[1]);
  const areaB = Math.max(0, b[2] - b[0]) * Math.max(0, b[3] - b[1]);
  const uni = areaA + areaB - inter;
  return uni > 0 ? inter / uni : 0;
}

function norm(s) {
  return String(s ?? '').replace(/\s+/g, ' ').trim().toLowerCase();
}

async function renderCase() {
  const b = await chromium.launch();
  const page = await b.newPage({ viewport: { width: W, height: H }, deviceScaleFactor: 1 });
  await page.setContent(HTML, { waitUntil: 'load' });
  // 每个目标文案只取第一个匹配元素（GT = 该元素本身的盒子，归一化到整图）
  const gt = {};
  for (const t of TARGETS) {
    const el = page.locator(`[data-t="${t.text}"]`).first();
    const box = await el.boundingBox();
    gt[t.id] = [box.x / W, box.y / H, (box.x + box.width) / W, (box.y + box.height) / H];
  }
  const buf = await page.screenshot({ type: 'png' });
  await b.close();
  return { gt, png: buf };
}

async function callOnce(png, extra) {
  const body = {
    model: MODEL,
    messages: [
      { role: 'system', content: SYSTEM },
      {
        role: 'user',
        content: [
          { type: 'text', text: locatePrompt() },
          { type: 'image_url', image_url: { url: 'data:image/png;base64,' + png.toString('base64') } },
        ],
      },
    ],
    max_tokens: 2048,
    temperature: 0.1,
    ...extra,
  };
  const t0 = Date.now();
  const res = await fetch(UPSTREAM, {
    method: 'POST',
    headers: { Authorization: 'Bearer ' + API_KEY, 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal: AbortSignal.timeout(240000),
  });
  const ms = Date.now() - t0;
  const txt = await res.text();
  let j;
  try { j = JSON.parse(txt); } catch { return { ms, status: res.status, error: 'non-JSON: ' + txt.slice(0, 160) }; }
  if (!Array.isArray(j.choices) || j.choices.length === 0) {
    return { ms, status: res.status, error: JSON.stringify(j).slice(0, 200) };
  }
  const c = j.choices[0];
  const content = c.message?.content ?? '';
  const usage = j.usage ?? {};
  let parsed = null;
  try { parsed = JSON.parse(content); } catch {}
  return {
    ms,
    status: res.status,
    finish: c.finish_reason,
    content,
    parsed,
    reasoningTokens: usage.completion_tokens_details?.reasoning_tokens ?? null,
    completionTokens: usage.completion_tokens ?? null,
  };
}

function score(gt, parsed) {
  const regions = parsed?.regions ?? [];
  const byId = new Map(regions.map((r) => [String(r.id), r]));
  let iouSum = 0;
  let iouOk = 0;
  let textOk = 0;
  let pixelSpace = 0;
  for (const t of TARGETS) {
    const r = byId.get(t.id);
    if (!r || !Array.isArray(r.bbox) || r.bbox.length !== 4) continue;
    let box = r.bbox.map(Number);
    // 归一化口径违约检测：思考关闭的模型会给像素坐标（36,36,107,53），
    // 直接和 [0,1] 真值比 IoU 恒为 0——不区分这一点会把"坐标口径错"误判成"定位能力差"
    if (Math.max(...box) > 1.5) {
      pixelSpace++;
      box = [box[0] / W, box[1] / H, box[2] / W, box[3] / H];
    }
    const v = iou(box, gt[t.id]);
    iouSum += v;
    if (v >= 0.5) iouOk++;
    if (norm(r.text) === norm(t.text)) textOk++;
  }
  return { returned: regions.length, iouMean: iouSum / TARGETS.length, iouOk, textOk, pixelSpace };
}

const CONDITIONS = [
  ['thinking off (mtt=0)', { nvext: { max_thinking_tokens: 0 } }],
  ['thinking 512', { nvext: { max_thinking_tokens: 512 } }],
  ['thinking full', {}],
];

const { gt, png } = await renderCase();
const results = [];
for (const [name, extra] of CONDITIONS) {
  for (let rep = 1; rep <= REPS; rep++) {
    const r = await callOnce(png, extra);
    if (r.error) {
      console.log(`${name.padEnd(22)} rep${rep} ${String(r.ms).padStart(6)}ms  ERR(${r.status}) ${r.error}`);
      results.push({ name, rep, ...r, ok: false });
      continue;
    }
    const s = score(gt, r.parsed);
    const okAll = s.iouOk === TARGETS.length;
    console.log(
      `${name.padEnd(22)} rep${rep} ${String(r.ms).padStart(6)}ms  rt=${r.reasoningTokens} out=${r.completionTokens} ` +
        `json=${r.parsed ? 'ok' : 'BAD'} n=${s.returned}/8 iou_mean=${s.iouMean.toFixed(3)} iou>=0.5=${s.iouOk}/8 text=${s.textOk}/8` +
        `${s.pixelSpace ? ` pixelSpace=${s.pixelSpace}/8` : ''}${okAll ? '  ✓' : ''}`,
    );
    results.push({ name, rep, ms: r.ms, ok: true, ...s, reasoningTokens: r.reasoningTokens, completionTokens: r.completionTokens, content: r.content });
  }
}

console.log('\n=== 汇总 ===');
for (const [name] of CONDITIONS) {
  const rows = results.filter((r) => r.name === name && r.ok);
  if (rows.length === 0) { console.log(`${name.padEnd(22)} 无有效样本`); continue; }
  const avg = (f) => (rows.reduce((s, r) => s + f(r), 0) / rows.length).toFixed(2);
  console.log(
    `${name.padEnd(22)} n=${rows.length} 延迟均值=${avg((r) => r.ms)}ms iou_mean=${avg((r) => r.iouMean)} iou>=0.5=${avg((r) => r.iouOk)}/8 text=${avg((r) => r.textOk)}/8`,
  );
}

fs.writeFileSync(path.join(ROOT, 'grounding_accuracy.json'), JSON.stringify({ gt, results }, null, 2));
