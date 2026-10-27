/**
 * Probe which request parameter actually disables thinking on vision-glm.
 * Small text-only prompts so each probe returns fast.
 */
import fs from 'node:fs';

const KEY = (fs.readFileSync('.env', 'utf8').match(/UOMA_API_KEY=([^\n]+)/) || [])[1];
const URL = 'http://100.69.40.56:4000/v1/chat/completions';

const variants = [
  ['baseline', {}],
  ['thinking.disabled', { thinking: { type: 'disabled' } }],
  ['enable_thinking:false', { enable_thinking: false }],
  ['reasoning_effort:none', { reasoning_effort: 'none' }],
  ['chat_template_kwargs', { chat_template_kwargs: { enable_thinking: false } }],
];

async function probe(name, extra) {
  const body = {
    model: 'vision-glm',
    messages: [{ role: 'user', content: 'Reply with the single word: alpha' }],
    max_tokens: 512,
    ...extra,
  };
  const t0 = Date.now();
  const res = await fetch(URL, {
    method: 'POST',
    headers: { 'Authorization': 'Bearer ' + KEY, 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal: AbortSignal.timeout(120000),
  });
  const ms = Date.now() - t0;
  const txt = await res.text();
  let out;
  try {
    const j = JSON.parse(txt);
    if (j.error) {
      out = `ERR(${res.status}): ${JSON.stringify(j.error).slice(0, 160)}`;
    } else {
      const c = j.choices[0];
      const content = c.message.content;
      const rl = (c.message.reasoning_content || '').length;
      const rt = j.usage?.completion_tokens_details?.reasoning_tokens ?? '?';
      out = `content=${JSON.stringify(content)?.slice(0, 70)} reasoningLen=${rl} reasoningTok=${rt} finish=${c.finish_reason}`;
    }
  } catch {
    out = `RAW(${res.status}): ${txt.slice(0, 160)}`;
  }
  console.log(`${name.padEnd(24)} ${String(ms).padStart(7)}ms  ${out}`);
}

for (const [name, extra] of variants) {
  try { await probe(name, extra); } catch (e) { console.log(`${name.padEnd(24)} FAILED ${e.message}`); }
  await new Promise(r => setTimeout(r, 300));
}
