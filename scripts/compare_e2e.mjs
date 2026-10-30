/**
 * E2E smoke: run compare_design with real uoma provider (vision_glm)
 * Usage (repo root): node scripts/compare_e2e.mjs <screenshot_path> <spec_path>
 */
import fs from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { spawn } from 'node:child_process';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

const SCREENSHOT = process.argv[2] || path.resolve(ROOT, 'screenshots/yyhd_reward_section.png');
const SPEC = process.argv[3] || 'C:/Users/10078/AppData/Local/Temp/frame7_spec.json';

function loadApiKey() {
  try {
    const env = fs.readFileSync(path.join(ROOT, '.env'), 'utf8');
    const m = env.match(/UOMA_API_KEY=([^\n]+)/);
    return m ? m[1] : process.env.UOMA_API_KEY;
  } catch { return process.env.UOMA_API_KEY; }
}

async function callCompareDesign() {
  return new Promise((resolve, reject) => {
    const apiKey = loadApiKey();
    const server = spawn('bin/llm-eyes-mcp.exe', ['--config', 'config.yml'], {
      cwd: ROOT,
      env: { ...process.env, UOMA_API_KEY: apiKey }
    });

    let buffer = '';
    let requestId = 1;

    server.stdout.on('data', (data) => {
      buffer += data.toString();
      let lines = buffer.split('\n');
      buffer = lines.pop();
      for (const line of lines) {
        if (!line.trim()) continue;
        try {
          const msg = JSON.parse(line);
          if (msg.id === requestId) {
            server.kill();
            resolve(msg);
            return;
          }
        } catch {}
      }
    });

    server.stderr.on('data', (d) => console.error('[stderr]', d.toString()));
    server.on('error', reject);
    server.on('exit', (code) => { if (code !== 0) console.log('Server exited with', code); });

    // Wait for server ready then send requests
    setTimeout(() => {
      // 1. Initialize
      const initReq = {
        jsonrpc: '2.0',
        id: requestId++,
        method: 'initialize',
        params: { protocolVersion: '2024-11-05', capabilities: {}, clientInfo: { name: 'test', version: '1.0' } }
      };
      server.stdin.write(JSON.stringify(initReq) + '\n');

      // 2. Call compare_design with explicit vision_glm provider
      setTimeout(() => {
        const toolReq = {
          jsonrpc: '2.0',
          id: requestId,
          method: 'tools/call',
          params: {
            name: 'compare_design',
            arguments: {
              image_source: SCREENSHOT,
              spec: SPEC,
              provider: "vision_glm"
            }
          }
        };
        server.stdin.write(JSON.stringify(toolReq) + '\n');
      }, 800);
    }, 1000);
  });
}

callCompareDesign()
  .then(r => {
    console.log('=== RESULT ===');
    console.log(JSON.stringify(r, null, 2));
  })
  .catch(e => console.error(e));