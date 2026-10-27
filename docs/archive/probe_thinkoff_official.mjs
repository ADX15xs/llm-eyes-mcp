/**
 * Test the official Zhipu `thinking:{type:"disabled"}` and json_schema
 * response_format on the 8-region vision locate prompt.
 */
import fs from 'node:fs';

const KEY = (fs.readFileSync('.env', 'utf8').match(/UOMA_API_KEY=([^\n]+)/) || [])[1];
const URL = 'http://100.69.40.56:4000/v1/chat/completions';
const imgB64 = fs.readFileSync('screenshots/yyhd_reward_section.png').toString('base64');
const spec = JSON.parse(fs.readFileSync('C:/Users/10078/AppData/Local/Temp/frame7_spec.json', 'utf8'));
const regions = spec.regions.slice(0, 8);
const text = `Locate the following 8 regions in the image and return JSON {"regions":[{"id":"...","bbox":[x0,y0,x1,y1],"text":"..."}]} with pixel coordinates. Regions:\n` +
  regions.map((r, i) => `${i + 1}. id=${r.id} ${r.desc || ''} (expected text: ${r.text || ''})`).join('\n');

async function probe(name, extra) {
  const body = {
    model: 'vision-glm',
    messages: [{
      role: 'user',
      content: [
        { type: 'text', text },
        { type: 'image_url', image_url: { url: `data:image/png;base64,${imgB64}` } },
      ],
    }],
    max_tokens: 2048,
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
      console.log(`${name.padEnd(28)} ${String(ms).padStart(7)}ms  ERR(${res.status}): ${JSON.stringify(j.error).slice(0, 160)}`);
      return;
    }
    const c = j.choices[0];
    const content = c.message.content;
    const rt = j.usage?.completion_tokens_details?.reasoning_tokens ?? '?';
    let parsed = 0;
    if (content) { try { parsed = (JSON.parse(content).regions || []).length; } catch {} }
    console.log(`${name.padEnd(28)} ${String(ms).padStart(7)}ms  finish=${c.finish_reason} reasoning=${rt} regionsParsed=${parsed} content=${content === null ? 'NULL' : 'present'}`);
  } catch (e) {
    console.log(`${name.padEnd(28)} FAILED ${e.message}`);
  }
}

const cases = [
  ['thinking.type=disabled', { thinking: { type: 'disabled' } }],
  ['thinking off + enable_thinking', { thinking: { type: 'disabled' }, enable_thinking: false }],
  ['json_schema response_format', {
    response_format: {
      type: 'json_schema',
      json_schema: {
        name: 'regions',
        schema: {
          type: 'object',
          properties: { regions: { type: 'array', items: { type: 'object', properties: { id: { type: 'string' }, bbox: { type: 'array', items: { type: 'number' } }, text: { type: 'string' } } } } },
        },
      },
    },
  }],
];

for (const [name, extra] of cases) {
  await probe(name, extra);
  await new Promise(r => setTimeout(r, 500));
}
