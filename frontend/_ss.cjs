const { chromium } = require('playwright');
const path = require('path');

(async () => {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  
  const dir = process.argv[2];
  const pages = [
    { name: '01-trees', path: '/trees' },
    { name: '02-nodes', path: '/nodes' },
    { name: '03-topics', path: '/topics' },
    { name: '04-cards', path: '/cards' },
    { name: '05-approvals', path: '/approvals' },
  ];
  
  for (const p of pages) {
    try {
      await page.goto(`http://localhost:5173${p.path}`, { waitUntil: 'networkidle', timeout: 15000 });
      await page.waitForTimeout(1500);
      await page.screenshot({ path: path.join(dir, p.name + '.png'), fullPage: false });
      console.log(`OK: ${p.name}`);
    } catch(e) {
      console.log(`FAIL: ${p.name} - ${e.message.substring(0, 150)}`);
      try { 
        await page.screenshot({ path: path.join(dir, p.name + '-error.png'), fullPage: false });
      } catch(_) {}
    }
  }
  
  await browser.close();
  console.log('DONE');
})();
