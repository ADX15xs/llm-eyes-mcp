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

  const outDir = path.resolve(__dirname, '..', 'screenshots');
  if (!fs.existsSync(outDir)) fs.mkdirSync(outDir, { recursive: true });

  // 1. 先滚动到奖品区域
  const rewardSection = await page.$('section:has(ul.team-reward-tabs__row)');
  if (rewardSection) {
    await rewardSection.scrollIntoViewIfNeeded();
    await page.waitForTimeout(1000);
  }

  // 2. 直接截取 ul.team-reward-tabs__row（9 个奖品卡片的容器）
  const grid = await page.$('ul.team-reward-tabs__row');
  if (grid) {
    const box = await grid.boundingBox();
    console.log('Grid box:', box);
    const compPath = path.join(outDir, 'yyhd_reward_grid.png');
    await grid.screenshot({ path: compPath, timeout: 60000 });
    console.log('✅ Reward grid screenshot:', compPath);
  }

  // 3. 也截取整个 team-reward-tabs__body（含 tabs + grid）
  const body = await page.$('div.team-reward-tabs__body');
  if (body) {
    const compPath = path.join(outDir, 'yyhd_reward_section.png');
    await body.screenshot({ path: compPath, timeout: 60000 });
    console.log('✅ Full reward section screenshot:', compPath);
  }

  // 4. 截取从 team-section 开始到 subsidy-section 之前的完整活动区域
  const activitySection = await page.$('section:has(h2.team-section__title)');
  if (activitySection) {
    await activitySection.scrollIntoViewIfNeeded();
    await page.waitForTimeout(500);
    const compPath = path.join(outDir, 'yyhd_activity_section.png');
    await activitySection.screenshot({ path: compPath, timeout: 60000 });
    console.log('✅ Activity section screenshot:', compPath);
  }

  await browser.close();
  console.log('Done.');
}

main().catch(e => { console.error(e); process.exit(1); });