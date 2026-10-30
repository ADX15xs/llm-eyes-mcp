const { chromium } = require('playwright');
const fs = require('fs');
const path = require('path');

const URL = 'https://cdn.pixeldesgin.cn/demos/nshm-yyhd/yyhdnam/?from=mobile#/';

async function main() {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({
    viewport: { width: 375, height: 812 },
    deviceScaleFactor: 2,
    isMobile: true,
    hasTouch: true
  });
  const page = await context.newPage();

  console.log('Navigating to', URL);
  await page.goto(URL, { waitUntil: 'networkidle', timeout: 60000 });
  await page.waitForTimeout(3000);

  // 尝试找到奖品/活动相关的区域
  // Frame 7 特征：标题 + 网格排列的奖品卡片（每个含图片+文字）
  const selectors = [
    // 常见奖品列表选择器
    '[class*="reward"]', '[class*="prize"]', '[class*="gift"]',
    '[class*="goods"]', '[class*="item"]', '[class*="card"]',
    '[class*="grid"]', '[class*="list"]',
    // 结构化查找：包含多个相似子元素的容器
    'section', '.section', '[class*="section"]',
    'div > div > div > div > div > div' // 深层嵌套可能是列表
  ];

  let bestTarget = null;
  let bestScore = 0;

  for (const sel of selectors) {
    const elements = await page.$$(sel);
    for (const el of elements) {
      const box = await el.boundingBox();
      if (!box) continue;
      
      // 评分：宽度接近视口、高度合理、有多个子元素
      let score = 0;
      if (box.width > 300 && box.width <= 375) score += 30;
      if (box.height > 200 && box.height < 1500) score += 30;
      
      const children = await el.$$(':scope > *');
      if (children.length >= 3) score += children.length * 5; // 奖品卡片通常 >= 3 个
      
      // 检查子元素是否相似（相同 tag/class 模式）
      const childTags = [];
      for (const ch of children.slice(0, 5)) {
        const tag = await ch.evaluate(e => e.tagName.toLowerCase());
        const cls = await ch.getAttribute('class');
        childTags.push(tag + (cls ? '.' + cls.split(' ')[0] : ''));
      }
      const uniqueTags = new Set(childTags);
      if (uniqueTags.size <= 2 && children.length >= 3) score += 20; // 结构一致
      
      console.log(`Selector: ${sel} | Box: ${Math.round(box.x)},${Math.round(box.y)} ${Math.round(box.w)}x${Math.round(box.h)} | Children: ${children.length} | Score: ${score} | Tags: ${childTags.join(', ')}`);
      
      if (score > bestScore) {
        bestScore = score;
        bestTarget = el;
      }
    }
  }

  const outDir = path.resolve(__dirname, '..', 'screenshots');
  if (!fs.existsSync(outDir)) fs.mkdirSync(outDir, { recursive: true });

  if (bestTarget && bestScore > 50) {
    const compPath = path.join(outDir, 'yyhd_frame7_candidate.png');
    await bestTarget.screenshot({ path: compPath });
    const box = await bestTarget.boundingBox();
    console.log(`\n✅ Best candidate (score ${bestScore}): ${compPath}`);
    console.log(`   Dimensions: ${Math.round(box.width)}x${Math.round(box.height)}`);
  } else {
    console.log('\n⚠️ No strong candidate, using full page');
    const fullPath = path.join(outDir, 'yyhd_full.png');
    await page.screenshot({ path: fullPath, fullPage: true });
  }

  await browser.close();
}

main().catch(e => { console.error(e); process.exit(1); });