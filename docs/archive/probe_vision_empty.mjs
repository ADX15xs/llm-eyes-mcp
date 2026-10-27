/**
 * Reproduce the E2E empty-content failure with a real image.
 * Tests a simple vision prompt vs. the 8-region JSON prompt, then
 * tries explicit enable_thinking:false variants.
 */
import fs from 'node:fs';

const KEY = (fs.readFileSync('.env', 'utf8').match(/UOMA_API_KEY=([^\n]+)/) || [])[1];
const URL = 'http://100.69.40.56:4000/v1/chat/completions';
const IMG = 'screenshots/yyhd_reward_section.png';
const SPEC = 'C:/Users/10078/AppData/Local/Temp/frame7_spec.json';

const imgB64 = fs.readFileSync(IMG).toString('base64');
const spec = JSON.parse(fs.readFileSync(SPEC, 'utf8'));
const first8 = spec.regions.slice(0, 8);

const locatePrompt = `Locate the following ${first8.length} regions in the image and return JSON {"regions":[{"id":"...","bbox":[x0,y0,x1,y1],"text":"..."}]} with pixel coordinates. Regions:\n` +
  first8.map((r, i) => `${i + 1}. id=${r.id} ${r.desc || ''} (expected text: ${r.text || ''})`).join('\n');

async function probe(name, { text, extra = {}, maxTokens = 2048 }) {
  const body = {
    model: 'vision-glm',
    messages: [{
      role: 'user',
      content: [
        { type: 'text', text },
        { type: 'image_url', image_url: { url: `data:image/png;base64,${imgB64}` } },
      ],
    }],
    max_tokens: maxTokens,
    temperature: 0,
    ...extra,
  };
  const t0 = Date.now();
  try {
    const res = await fetch(URL, {
      method: 'POST',
      headers: { 'Authorization': 'Bearer ' + KEY, 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(240000),
    });
    const ms = Date.now() - t0;
    const j = await res.json();
    if (j.error) {
      console.log(`${name.padEnd(30)} ${String(ms).padStart(7)}ms  ERR(${res.status}): ${JSON.stringify(j.error).slice(0, 200)}`);
      return;
    }
    const c = j.choices[0];
    const content = c.message.content;
    const rt = j.usage?.completion_tokens_details?.reasoning_tokens ?? '?';
    const ct = j.usage?.completion_tokens ?? '?';
    const shown = content === null ? 'NULL' : JSON.stringify(content).slice(0, 80);
    console.log(`${name.padEnd(30)} ${String(ms).padStart(7)}ms  finish=${c.finish_reason} completionTok=${ct} reasoningTok=${rt} content=${shown}`);
  } catch (e) {
    console.log(`${name.padEnd(30)} FAILED ${e.message}`);
  }
}

const cases = [
  ['simple vision prompt', { text: 'List the text you see in this image, one line per item.' }],
  ['8-region JSON locate', { text: locatePrompt }],
  ['8-region + enable_thinking', { text: locatePrompt, extra: { enable_thinking: false } }],
  ['8-region + chat_template', { text: locatePrompt, extra: { chat_template_kwargs: { enable_thinking: false } } }],
  ['8-region + maxTokens 8192', { text: locatePrompt, maxTokens: 8192 }],
];

for (const [name, opts] of cases) {
  await probe(name, opts);
  await new Promise(r => setTimeout(r, 500));
}
