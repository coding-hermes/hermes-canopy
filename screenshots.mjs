/**
 * E2E screenshot capture script for Hermes Canopy.
 * Run: npx tsx screenshots.mjs
 */
import { chromium } from '@playwright/test';

const BASE = 'http://localhost:5173';
const SCREENSHOT_DIR = '/home/kara/hermes-canopy/screenshots';

const pages = [
  { name: 'TreesPage', path: '/trees' },
  { name: 'NodesPage', path: '/nodes' },
  { name: 'TopicsPage', path: '/topics' },
  { name: 'CardsPage', path: '/cards' },
  { name: 'Approvals', path: '/approvals' },
  { name: 'AgentCards', path: '/tree/demo' },
  { name: 'Dashboard', path: '/' },
];

async function main() {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });

  for (const { name, path } of pages) {
    const page = await context.newPage();
    try {
      console.log(`Navigating to ${path}...`);
      await page.goto(`${BASE}${path}`, { timeout: 15000, waitUntil: 'domcontentloaded' });
      await page.waitForTimeout(2000);

      const filepath = `${SCREENSHOT_DIR}/${name}.png`;
      await page.screenshot({ path: filepath, fullPage: false });
      console.log(`  ✅ ${name} saved to ${filepath}`);

      // Also check for errors in console
      const consoleErrors = [];
      page.on('console', msg => {
        if (msg.type() === 'error') consoleErrors.push(msg.text());
      });
    } catch (err) {
      console.log(`  ❌ ${name}: ${err.message}`);
    } finally {
      await page.close();
    }
  }

  await browser.close();
  console.log('\nDone!');
}

main().catch(console.error);
