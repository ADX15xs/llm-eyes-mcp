import fs from 'node:fs';
const KEY = (fs.readFileSync('.env','utf8').match(/UOMA_API_KEY=([^\n]+)/)||[])[1];
const URL = 'http://100.69.40.56:4000/v1/chat/completions';
const imgB64 = fs.readFileSync('screenshots/yyhd_reward_section.png').toString('base64');
const spec = JSON.parse(fs.readFileSync('C:/Users/10078/AppData/Local/Temp/frame7_spec.json','utf8'));
const regions = spec.regions.slice(0,8);
const text = `Locate the following 8 regions in the image and return JSON {"regions":[{"id":"...","bbox":[x0,y0,x1,y1],"text":"..."}]} with pixel coordinates. Regions:\n` + regions.map((r,i)=>`${i+1}. id=${r.id} expected text: ${r.text}`).join('\n');
const body = { model:'vision-glm', messages:[{role:'user',content:[{type:'text',text},{type:'image_url',image_url:{url:`data:image/png;base64,${imgB64}`}}]}], max_tokens:8192, temperature:0, thinking:{type:'disabled'} };
const t0=Date.now();
try {
  const res = await fetch(URL,{method:'POST',headers:{'Authorization':'Bearer '+KEY,'Content-Type':'application/json'},body:JSON.stringify(body),signal:AbortSignal.timeout(290000)});
  const ms=Date.now()-t0;
  const j=await res.json();
  if(j.error){console.log(`${ms}ms ERR(${res.status}): ${JSON.stringify(j.error).slice(0,200)}`);}
  else { const c=j.choices[0]; let n=0; if(c.message.content){try{n=JSON.parse(c.message.content).regions.length}catch{}} console.log(`${ms}ms finish=${c.finish_reason} completion=${j.usage.completion_tokens} reasoning=${j.usage.completion_tokens_details.reasoning_tokens} regionsParsed=${n}`); }
} catch(e){ console.log(`${Date.now()-t0}ms FAILED ${e.message}`); }
