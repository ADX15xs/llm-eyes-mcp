import fs from 'node:fs';
const KEY = (fs.readFileSync('.env','utf8').match(/UOMA_API_KEY=([^\n]+)/)||[])[1];
const URL = 'http://100.69.40.56:4000/v1/chat/completions';
const imgB64 = fs.readFileSync('screenshots/yyhd_reward_section.png').toString('base64');
const spec = JSON.parse(fs.readFileSync('C:/Users/10078/AppData/Local/Temp/frame7_spec.json','utf8'));
async function one(idx){
  const r = spec.regions[idx];
  const text = `Find the UI element whose text is "${r.text}" (id ${r.id}). Return ONLY one JSON object {"bbox":[x0,y0,x1,y1],"text":"..."} in pixel coordinates, no prose.`;
  const body = { model:'vision-glm', messages:[{role:'user',content:[{type:'text',text},{type:'image_url',image_url:{url:`data:image/png;base64,${imgB64}`}}]}], max_tokens:2048, temperature:0, thinking:{type:'disabled'} };
  const t0=Date.now();
  try {
    const res = await fetch(URL,{method:'POST',headers:{'Authorization':'Bearer '+KEY,'Content-Type':'application/json'},body:JSON.stringify(body),signal:AbortSignal.timeout(240000)});
    const ms=Date.now()-t0; const j=await res.json();
    if(j.error){console.log(`run${idx} ${ms}ms ERR ${res.status}`);return false;}
    const c=j.choices[0]; let ok=false;
    if(c.message.content){ try{const o=JSON.parse(c.message.content); ok=Array.isArray(o.bbox)&&o.bbox.length===4;}catch{} }
    console.log(`run${idx} ${ms}ms finish=${c.finish_reason} reasoning=${j.usage.completion_tokens_details.reasoning_tokens} validJSON=${ok}`);
    return ok;
  } catch(e){console.log(`run${idx} FAILED ${e.message}`);return false;}
}
let wins=0;
for(const i of [0,1,2]){ if(await one(i)) wins++; await new Promise(r=>setTimeout(r,400)); }
console.log(`single-region reliability: ${wins}/3`);
