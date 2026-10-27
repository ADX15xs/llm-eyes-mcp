/**
 * Find the real batch size vision-glm can handle: 1,2,3,4-region locate.
 * Uses the same image and prompt shape as the real tool.
 */
import fs from 'node:fs';

const KEY = (fs.readFileSync('.env', 'utf8').match(/UOMA_API_KEY=([^\n]+)/) || [])[1];
const URL = 'http://100.69.40.56:4000/v1/chat/completions';
const IMG = 'screenshots/yyhd_reward_section.png';
const SPEC = 'C:/Users/10078/AppData/Local/Temp/frame7_spec.json';

const imgB64 = fs.readFileSync(IMG).toString('base64');
const spec = JSON.parse(fs.readFileSync(SPEC, 'utf8'));

async function probe(n) {
  const regions = spec.regions.slice(0, n);
  const text = `Locate the following ${n} region(s) in the image and return JSON {"regions":[{"id":"...","bbox":[x0,y0,x1,y1],"text":"..."}]} with pixel coordinates. Regions:\n` +
    regions.map((r, i) => `${i + 1}. id=${r.id} ${r.desc || ''} (expected text: ${r.text || ''})`).join('\n');
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
      console.log(`${n}-region`.padEnd(12) + `${String(ms).padStart(7)}ms  ERR(${res.status}): ${JSON.stringify(j.error).slice(0, 140)}`);
      return;
    }
    const c = j.choices[0];
    const content = c.message.content;
    const rt = j.usage?.completion_tokens_details?.reasoning_tokens ?? '?';
    const ct = j.usage?.completion_tokens ?? '?';
    let parsed = 0;
    if (content) { try { parsed = (JSON.parse(content).regions || []).length; } catch {} }
    console.log(`${n}-region`.padEnd(12) + `${String(ms).padStart(7)}ms  finish=${c.finish_reason} tok=${ct} reasoning=${rt} regionsParsed=${parsed} content=${content === null ? 'NULL' : 'present'}`);
  } catch (e) {
    console.log(`${n}-region`.padEnd(12) + ` FAILED ${e.message}`);
  }
}

for (const n of [1, 2, 3, 4]) {
  await probe(n);
  await new Promise(r => setTimeout(r, 500));
}
