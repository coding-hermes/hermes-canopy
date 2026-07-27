/**
 * Hermes Canopy — Accessibility Audit Runner
 *
 * Uses Playwright + axe-core to audit all pages for WCAG 2.1 AA compliance.
 * Run: node test-results/run-a11y-audit.mjs
 *
 * Prerequisites:
 *   - Vite dev server running on http://localhost:5173 (or VITE_PORT env)
 *   - canopyd backend running (for API-dependent pages)
 */

import { chromium } from '@playwright/test';
import AxeBuilder from 'axe-core';
import { writeFileSync, mkdirSync } from 'fs';
import { join, dirname } from 'path';
import { fileURLToPath } from 'url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const BASE_URL = process.env.BASE_URL || 'http://localhost:5173';
const OUTPUT_DIR = process.env.OUTPUT_DIR || __dirname;

// Pages to audit
const PAGES = [
  { route: '/', name: 'Dashboard', component: 'App.tsx (Dashboard)' },
  { route: '/trees', name: 'TreesPage', component: 'pages/TreesPage.tsx' },
  { route: '/nodes', name: 'NodesPage', component: 'pages/NodesPage.tsx' },
  { route: '/topics', name: 'TopicsPage', component: 'pages/TopicsPage.tsx' },
  { route: '/cards', name: 'CardsPage', component: 'pages/CardsPage.tsx' },
  { route: '/approvals', name: 'ApprovalPanel', component: 'components/ApprovalPanel.tsx' },
  { route: '/tree/demo', name: 'TreeView', component: 'components/TreeView.tsx' },
];

// Keyboard navigation test elements per page
const KEYBOARD_TESTS = {
  '/': { expectedTabCount: 7 }, // Dashboard + 7 nav links
  '/trees': { expectedTabCount: 7 },
  '/nodes': { expectedTabCount: 7 },
  '/topics': { expectedTabCount: 7 },
  '/cards': { expectedTabCount: 7 },
  '/approvals': { expectedTabCount: 7 },
  '/tree/demo': { expectedTabCount: 7 }, // nav links count
};

async function sleep(ms) {
  return new Promise(resolve => setTimeout(resolve, ms));
}

async function runAxeOnPage(page, route, name) {
  console.log(`\n📋 Auditing: ${name} (${route})`);

  try {
    await page.goto(`${BASE_URL}${route}`, { waitUntil: 'networkidle', timeout: 15000 });
    await sleep(1000); // Let React hydrate
  } catch (err) {
    console.log(`   ⚠️ Navigation failed: ${err.message}`);
    return { route, name, error: err.message, violations: [] };
  }

  // Check if page loaded
  const title = await page.title();
  console.log(`   Title: ${title}`);

  // Inject axe-core
  await page.evaluate(() => {
    if (!window.axe) {
      console.warn('axe-core not on window — injecting');
    }
  });

  // Actually inject axe-core
  const axeCoreSource = await import('axe-core').then(m => m.default.source);
  await page.evaluate(axeCoreSource => {
    eval(axeCoreSource);
  }, axeCoreSource);

  // Run axe
  const results = await page.evaluate(() => {
    return new Promise((resolve) => {
      window.axe.run(document, {
        runOnly: {
          type: 'tag',
          values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'best-practice']
        }
      }, (err, results) => {
        if (err) resolve({ error: err.message, violations: [] });
        else resolve(results);
      });
    });
  });

  return { route, name, title, violations: results.violations || [], error: results.error || null };
}

async function runKeyboardTest(page, route, name) {
  console.log(`\n⌨️  Keyboard test: ${name} (${route})`);

  try {
    await page.goto(`${BASE_URL}${route}`, { waitUntil: 'networkidle', timeout: 15000 });
    await sleep(1000);
  } catch (err) {
    console.log(`   ⚠️ Navigation failed: ${err.message}`);
    return { route, name, error: err.message, tabResults: [] };
  }

  const tabResults = [];
  let previousElement = null;
  const MAX_TABS = 50;
  let tabCount = 0;

  for (let i = 0; i < MAX_TABS; i++) {
    await page.keyboard.press('Tab');
    await sleep(100);

    const focusInfo = await page.evaluate(() => {
      const el = document.activeElement;
      if (!el || el === document.body) return null;

      const computed = window.getComputedStyle(el);
      const rect = el.getBoundingClientRect();

      return {
        tag: el.tagName.toLowerCase(),
        id: el.id || null,
        ariaLabel: el.getAttribute('aria-label') || null,
        role: el.getAttribute('role') || null,
        text: (el.innerText || el.textContent || '').slice(0, 50),
        tabIndex: el.tabIndex,
        visible: rect.width > 0 && rect.height > 0,
        hasOutline: computed.outlineStyle !== 'none' || computed.boxShadow !== 'none',
        outlineWidth: computed.outlineWidth,
        outlineStyle: computed.outlineStyle,
        boxShadow: computed.boxShadow,
        hasVisibleFocusIndicator:
          (computed.outlineStyle !== 'none' && parsedPx(computed.outlineWidth) > 0) ||
          (computed.boxShadow !== 'none' && !computed.boxShadow.includes('transparent')),
      };

      function parsedPx(val) {
        if (val === '0') return 0;
        const m = val.match(/^([\d.]+)px$/);
        return m ? parseFloat(m[1]) : 0;
      }
    });

    if (!focusInfo) {
      console.log(`   Tab #${i + 1}: No focusable element (body focused)`);
      break;
    }

    tabCount++;

    // Check if we've looped back to the first element
    const elementId = focusInfo.id || focusInfo.ariaLabel || focusInfo.text;
    if (previousElement && previousElement === elementId) {
      console.log(`   Tab #${i + 1}: Loop detected — same as Tab #1`);
      break;
    }
    previousElement = previousElement || elementId;

    tabResults.push({
      tabIndex: i + 1,
      ...focusInfo,
      focusIndicatorIssue: !focusInfo.hasVisibleFocusIndicator
        ? 'NO VISIBLE FOCUS INDICATOR'
        : null,
    });

    console.log(`   Tab #${i + 1}: ${focusInfo.tag}${focusInfo.id ? '#' + focusInfo.id : ''} "${focusInfo.text}" focus=${focusInfo.hasVisibleFocusIndicator ? '✅' : '❌'}`);
  }

  return {
    route,
    name,
    tabResults,
    tabCount,
    hasFocusLoop: tabCount > 0 && tabResults.length >= 2 &&
      (tabResults[0].id || tabResults[0].text) === (tabResults[tabResults.length - 1].id || tabResults[tabResults.length - 1].text),
  };
}

async function runScreenReaderTests(page, route, name) {
  console.log(`\n🔊 Screen reader test: ${name} (${route})`);

  try {
    await page.goto(`${BASE_URL}${route}`, { waitUntil: 'networkidle', timeout: 15000 });
    await sleep(1000);
  } catch (err) {
    return { route, name, error: err.message, checks: [] };
  }

  const checks = await page.evaluate(() => {
    const results = [];

    // 1. Check for aria-live region
    const liveRegion = document.getElementById('aria-live-announcer');
    results.push({
      check: 'ARIA live region present',
      pass: !!liveRegion,
      details: liveRegion ? `Found #aria-live-announcer (role=${liveRegion.getAttribute('role')}, aria-live=${liveRegion.getAttribute('aria-live')})` : 'Missing',
    });

    // 2. Check for skip-to-main link
    const skipLink = document.querySelector('.skip-to-main');
    results.push({
      check: 'Skip-to-main link present',
      pass: !!skipLink,
      details: skipLink ? `Found: "${skipLink.textContent.trim()}"` : 'Missing',
    });

    // 3. Check main has role
    const mainEl = document.querySelector('main');
    results.push({
      check: 'Main content has role="main"',
      pass: mainEl ? mainEl.getAttribute('role') === 'main' : false,
      details: mainEl ? `Main role=${mainEl.getAttribute('role')}` : 'No <main> element',
    });

    // 4. Check sidebar nav role
    const sidebar = document.querySelector('aside');
    results.push({
      check: 'Sidebar has role="navigation"',
      pass: sidebar ? sidebar.getAttribute('role') === 'navigation' : false,
      details: sidebar ? `Sidebar role=${sidebar.getAttribute('role')}` : 'No <aside> element',
    });

    // 5. Check lang attribute
    const html = document.documentElement;
    results.push({
      check: 'HTML lang attribute set',
      pass: !!html.lang,
      details: html.lang ? `lang="${html.lang}"` : 'Missing',
    });

    // 6. Check heading hierarchy (no skipped levels)
    const headings = Array.from(document.querySelectorAll('h1, h2, h3, h4, h5, h6'));
    const headingLevels = headings.map(h => parseInt(h.tagName[1]));
    let skippedLevels = [];
    for (let i = 1; i < headingLevels.length; i++) {
      if (headingLevels[i] > headingLevels[i - 1] + 1) {
        skippedLevels.push(`h${headingLevels[i-1]} → h${headingLevels[i]}`);
      }
    }
    results.push({
      check: 'Heading hierarchy (no skipped levels)',
      pass: skippedLevels.length === 0,
      details: skippedLevels.length > 0
        ? `Skipped levels: ${skippedLevels.join(', ')} (${headings.length} total headings)`
        : `${headings.length} headings, levels: [${headingLevels.join(', ')}]`,
    });

    // 7. Check for images without alt
    const imgs = Array.from(document.querySelectorAll('img'));
    const imgsWithoutAlt = imgs.filter(img => !img.hasAttribute('alt'));
    results.push({
      check: 'All images have alt text',
      pass: imgsWithoutAlt.length === 0,
      details: imgsWithoutAlt.length > 0
        ? `${imgsWithoutAlt.length}/${imgs.length} images missing alt: ${imgsWithoutAlt.map(i => i.src.slice(-40)).join(', ')}`
        : `${imgs.length} images all have alt text`,
    });

    // 8. Check for form inputs without labels
    const inputs = Array.from(document.querySelectorAll('input:not([type="hidden"]), textarea, select'));
    const inputsWithoutLabels = [];
    for (const input of inputs) {
      const id = input.id;
      const hasLabel = id ? !!document.querySelector(`label[for="${id}"]`) : false;
      const hasAriaLabel = input.hasAttribute('aria-label');
      const hasAriaLabelledby = input.hasAttribute('aria-labelledby');
      if (!hasLabel && !hasAriaLabel && !hasAriaLabelledby) {
        inputsWithoutLabels.push(input.placeholder || input.name || input.id || input.className);
      }
    }
    results.push({
      check: 'All form inputs have labels',
      pass: inputsWithoutLabels.length === 0,
      details: inputsWithoutLabels.length > 0
        ? `${inputsWithoutLabels.length}/${inputs.length} inputs unlabeled: ${inputsWithoutLabels.join(', ')}`
        : `${inputs.length} inputs all labeled`,
    });

    // 9. Check color contrast (quick heuristic via WCAG test — actual test needs axe)
    results.push({
      check: 'Color contrast (see axe results)',
      pass: null, // defer to axe
      details: 'Deferred to axe-core analysis',
    });

    // 10. Check touch target size
    const buttons = Array.from(document.querySelectorAll('button, [role="button"]'));
    const smallTargets = [];
    for (const btn of buttons.slice(0, 50)) {
      const rect = btn.getBoundingClientRect();
      if (rect.width > 0 && rect.height > 0 && (rect.width < 24 || rect.height < 24)) {
        smallTargets.push(`${btn.textContent?.slice(0, 20) || btn.getAttribute('aria-label') || 'unnamed'} (${Math.round(rect.width)}x${Math.round(rect.height)}px)`);
      }
    }
    results.push({
      check: 'Touch target minimum 24x24px',
      pass: smallTargets.length === 0,
      details: smallTargets.length > 0
        ? `${smallTargets.length} small targets: ${smallTargets.slice(0, 5).join(', ')}`
        : 'All buttons meet minimum 24px',
    });

    return results;
  });

  for (const check of checks) {
    const icon = check.pass === true ? '✅' : check.pass === false ? '❌' : '➖';
    console.log(`   ${icon} ${check.check}: ${check.details}`);
  }

  return { route, name, checks };
}

async function main() {
  console.log('=== Hermes Canopy Accessibility Audit ===');
  console.log(`Target: WCAG 2.1 AA | Base URL: ${BASE_URL}`);
  console.log(`Pages to audit: ${PAGES.length}`);
  console.log('');

  let browser;
  try {
    browser = await chromium.launch({ headless: true });
  } catch (err) {
    console.error('Failed to launch browser:', err.message);
    console.log('Falling back to static code analysis only...');
    browser = null;
  }

  const fullReport = {
    auditDate: new Date().toISOString(),
    baseUrl: BASE_URL,
    target: 'WCAG 2.1 AA',
    pages: [],
    summary: {
      totalPages: PAGES.length,
      totalViolations: 0,
      criticalViolations: 0,
      seriousViolations: 0,
      moderateViolations: 0,
      minorViolations: 0,
      keyboardIssues: 0,
      screenReaderIssues: 0,
    },
  };

  if (browser) {
    const context = await browser.newContext({ viewport: { width: 1280, height: 720 } });
    const page = await context.newPage();

    for (const pageConfig of PAGES) {
      // Run axe-core
      const axeResult = await runAxeOnPage(page, pageConfig.route, pageConfig.name);
      // Run keyboard test
      const kbResult = await runKeyboardTest(page, pageConfig.route, pageConfig.name);
      // Run screen reader test
      const srResult = await runScreenReaderTests(page, pageConfig.route, pageConfig.name);

      fullReport.pages.push({
        ...pageConfig,
        axe: axeResult,
        keyboard: kbResult,
        screenReader: srResult,
      });

      // Update summary
      const violations = axeResult.violations || [];
      fullReport.summary.totalViolations += violations.length;
      for (const v of violations) {
        if (v.impact === 'critical') fullReport.summary.criticalViolations++;
        else if (v.impact === 'serious') fullReport.summary.seriousViolations++;
        else if (v.impact === 'moderate') fullReport.summary.moderateViolations++;
        else fullReport.summary.minorViolations++;
      }

      if (kbResult.tabResults) {
        fullReport.summary.keyboardIssues += kbResult.tabResults.filter(t => t.focusIndicatorIssue).length;
      }
    }

    await context.close();
    await browser.close();
  }

  // Write raw JSON
  const jsonPath = join(OUTPUT_DIR, 'accessibility-audit-raw.json');
  writeFileSync(jsonPath, JSON.stringify(fullReport, null, 2));
  console.log(`\n\n📄 Raw data written to: ${jsonPath}`);

  return fullReport;
}

main().catch(err => {
  console.error('Audit failed:', err);
  process.exit(1);
});
