const { chromium } = require('playwright');
const fs = require('fs');
const path = require('path');

const URL = 'https://cdn.pixeldesgin.cn/demos/nshm-yyhd/yyhdnam/?from=mobile#/';

// 目标：找到对应 Frame 7 的容器（根据 class 或结构定位）
// 先全屏截图，再尝试定位具体组件
async function main() {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({
    viewport: { width: 375, height: 812 }, // 移动端视口
    deviceScaleFactor: 2,
    isMobile: true,
    hasTouch: true
  });
  const page = await context.newPage();

  console.log('Navigating to', URL);
  await page.goto(URL, { waitUntil: 'networkidle', timeout: 60000 });

  // 等待内容渲染
  await page.waitForTimeout(3000);

  // 尝试定位常见容器
  const candidates = [
    '.page', '.container', '.main', '#app', '#root',
    '[class*="frame"]', '[class*="page"]', '[class*="activity"]',
    'body > div:first-child'
  ];

  let target = null;
  for (const sel of candidates) {
    const el = await page.$(sel);
    if (el) {
      const box = await el.boundingBox();
      if (box && box.width > 200 && box.height > 300) {
        target = el;
        console.log(`Found target with selector: ${sel}`, box);
        break;
      }
    }
  }

  if (!target) {
    console.log('No specific container found, using full page body');
    target = await page.$('body');
  }

  // 截图
  const outDir = path.resolve(__dirname, 'screenshots');
  if (!fs.existsSync(outDir)) fs.mkdirSync(outDir, { recursive: true });

  const fullPath = path.join(outDir, 'yyhd_full.png');
  await page.screenshot({ path: fullPath, fullPage: true });
  console.log('Full page screenshot:', fullPath);

  if (target !== (await page.$('body'))) {
    const compPath = path.join(outDir, 'yyhd_component.png');
    await target.screenshot({ path: compPath });
    console.log('Component screenshot:', compPath);
  }

  await browser.close();
  console.log('Done. Screenshots in', outDir);
}

main().catch(e => { console.error(e); process.exit(1); });