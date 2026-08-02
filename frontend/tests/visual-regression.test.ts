/**
 * UI-09 visual regression baseline.
 *
 * This file intentionally runs under the existing Vitest integration config.
 * Playwright's browser automation captures the fixed viewport; a small
 * dependency-free PNG decoder compares the current capture with the committed
 * golden. UPDATE_VISUAL_GOLDENS=1 refreshes both the golden and the pair.
 */
/// <reference types="node" />
import { Buffer } from 'node:buffer';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { inflateSync } from 'node:zlib';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import {
  BASE_URL,
  createTestContext,
  destroyContext,
  isServerRunning,
  type TestContext,
} from './setup';

const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const VISUAL_ROOT = resolve(REPO_ROOT, 'docs/screenshots/visual-regression');
const GOLDEN_ROOT = resolve(VISUAL_ROOT, 'golden');
const PAIR_ROOT = resolve(VISUAL_ROOT, 'pairs');
const FAILURE_ROOT = resolve('/tmp/canopy-visual-regression');

const VIEWPORT = { width: 1440, height: 900 } as const;
const FIXED_NOW = new Date('2026-08-02T12:00:00.000Z');
const CHANNEL_THRESHOLD = 8;
const MAX_DIFF_PIXEL_RATIO = 0.02;
const UPDATE_VISUAL_GOLDENS = process.env.UPDATE_VISUAL_GOLDENS === '1';

const FREEZE_TRANSITIONS = `
  *, *::before, *::after {
    animation-duration: 0s !important;
    animation-delay: 0s !important;
    transition-duration: 0s !important;
    transition-delay: 0s !important;
    caret-color: transparent !important;
  }
  html { scroll-behavior: auto !important; }
`;

type VisualCase = {
  id: string;
  label: string;
  route: string;
  mockupPath: string;
  goldenName: string;
  pairName: string;
  prepare: (page: TestContext['page']) => Promise<void>;
};

type DecodedPng = {
  width: number;
  height: number;
  pixels: Uint8Array;
};

type DiffResult = {
  width: number;
  height: number;
  diffPixels: number;
  diffRatio: number;
  maxChannelDelta: number;
};

function dataUri(bytes: Buffer): string {
  return `data:image/png;base64,${bytes.toString('base64')}`;
}

function decodePng(bytes: Buffer): DecodedPng {
  const signature = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]);
  if (!bytes.subarray(0, 8).equals(signature)) {
    throw new Error('Visual regression comparator expected a PNG file.');
  }

  let width = 0;
  let height = 0;
  let bitDepth = 0;
  let colorType = 0;
  let interlaceMethod = 0;
  const idatChunks: Buffer[] = [];

  for (let offset = 8; offset < bytes.length;) {
    const length = bytes.readUInt32BE(offset);
    const type = bytes.toString('ascii', offset + 4, offset + 8);
    const dataStart = offset + 8;
    const dataEnd = dataStart + length;
    const chunk = bytes.subarray(dataStart, dataEnd);

    if (type === 'IHDR') {
      width = chunk.readUInt32BE(0);
      height = chunk.readUInt32BE(4);
      bitDepth = chunk[8] ?? 0;
      colorType = chunk[9] ?? 0;
      interlaceMethod = chunk[12] ?? 0;
    } else if (type === 'IDAT') {
      idatChunks.push(chunk);
    } else if (type === 'IEND') {
      break;
    }

    offset = dataEnd + 4; // skip the CRC
  }

  if (width === 0 || height === 0) {
    throw new Error('PNG is missing a valid IHDR dimension.');
  }
  if (bitDepth !== 8 || (colorType !== 2 && colorType !== 6)) {
    throw new Error(
      `Unsupported PNG format (bit depth ${bitDepth}, color type ${colorType}); expected 8-bit RGB/RGBA.`,
    );
  }
  if (interlaceMethod !== 0) {
    throw new Error('Interlaced PNGs are not supported by the comparator.');
  }

  const channels = colorType === 6 ? 4 : 3;
  const bytesPerPixel = channels;
  const rowBytes = width * channels;
  const inflated = inflateSync(Buffer.concat(idatChunks));
  const pixels = new Uint8Array(width * height * 4);
  let inputOffset = 0;
  let previousRow = new Uint8Array(rowBytes);

  for (let y = 0; y < height; y += 1) {
    const filter = inflated[inputOffset++];
    if (filter === undefined) throw new Error('PNG scanline is truncated.');

    const row = new Uint8Array(rowBytes);
    for (let x = 0; x < rowBytes; x += 1) {
      const encoded = inflated[inputOffset++];
      if (encoded === undefined) throw new Error('PNG scanline data is truncated.');

      const left = x >= bytesPerPixel ? row[x - bytesPerPixel] ?? 0 : 0;
      const up = previousRow[x] ?? 0;
      const upLeft = x >= bytesPerPixel ? previousRow[x - bytesPerPixel] ?? 0 : 0;
      let decoded = encoded;

      switch (filter) {
        case 0:
          break;
        case 1:
          decoded += left;
          break;
        case 2:
          decoded += up;
          break;
        case 3:
          decoded += Math.floor((left + up) / 2);
          break;
        case 4: {
          const p = left + up - upLeft;
          const pa = Math.abs(p - left);
          const pb = Math.abs(p - up);
          const pc = Math.abs(p - upLeft);
          const predictor = pa <= pb && pa <= pc ? left : pb <= pc ? up : upLeft;
          decoded += predictor;
          break;
        }
        default:
          throw new Error(`Unsupported PNG filter type ${filter}.`);
      }

      row[x] = decoded & 0xff;
    }

    for (let x = 0; x < width; x += 1) {
      const source = x * channels;
      const target = (y * width + x) * 4;
      pixels[target] = row[source] ?? 0;
      pixels[target + 1] = row[source + 1] ?? 0;
      pixels[target + 2] = row[source + 2] ?? 0;
      pixels[target + 3] = channels === 4 ? row[source + 3] ?? 255 : 255;
    }

    previousRow = row;
  }

  return { width, height, pixels };
}

function comparePngs(actualBytes: Buffer, expectedBytes: Buffer): DiffResult {
  const actual = decodePng(actualBytes);
  const expected = decodePng(expectedBytes);

  if (actual.width !== expected.width || actual.height !== expected.height) {
    return {
      width: actual.width,
      height: actual.height,
      diffPixels: actual.width * actual.height,
      diffRatio: 1,
      maxChannelDelta: 255,
    };
  }

  let diffPixels = 0;
  let maxChannelDelta = 0;
  for (let i = 0; i < actual.pixels.length; i += 4) {
    const redDelta = Math.abs((actual.pixels[i] ?? 0) - (expected.pixels[i] ?? 0));
    const greenDelta = Math.abs(
      (actual.pixels[i + 1] ?? 0) - (expected.pixels[i + 1] ?? 0),
    );
    const blueDelta = Math.abs(
      (actual.pixels[i + 2] ?? 0) - (expected.pixels[i + 2] ?? 0),
    );
    const alphaDelta = Math.abs(
      (actual.pixels[i + 3] ?? 0) - (expected.pixels[i + 3] ?? 0),
    );
    const pixelDelta = Math.max(redDelta, greenDelta, blueDelta, alphaDelta);
    maxChannelDelta = Math.max(maxChannelDelta, pixelDelta);
    if (pixelDelta > CHANNEL_THRESHOLD) diffPixels += 1;
  }

  return {
    width: actual.width,
    height: actual.height,
    diffPixels,
    diffRatio: diffPixels / (actual.width * actual.height),
    maxChannelDelta,
  };
}

async function waitForImages(page: TestContext['page']): Promise<void> {
  await page.waitForFunction(
    () => Array.from(document.images).every((image) => image.complete && image.naturalWidth > 0),
    { timeout: 10_000 },
  );
}

async function capturePair(
  ctx: TestContext,
  mockupBytes: Buffer,
  appBytes: Buffer,
  pairPath: string,
): Promise<void> {
  const mockupPage = await ctx.browser.newPage({ viewport: VIEWPORT });
  try {
    await mockupPage.setContent(`
      <!doctype html>
      <html><head><style>
        html, body { margin: 0; width: 100%; height: 100%; overflow: hidden; background: #050816; }
        img { display: block; width: 100vw; height: 100vh; object-fit: contain; }
      </style></head><body>
        <img alt="Mockup source" src="${dataUri(mockupBytes)}" />
      </body></html>
    `);
    await waitForImages(mockupPage);
    const mockupPanel = await mockupPage.screenshot({ type: 'png' });

    const pairPage = await ctx.browser.newPage({
      viewport: { width: VIEWPORT.width * 2, height: VIEWPORT.height },
    });
    try {
      await pairPage.setContent(`
        <!doctype html>
        <html><head><style>
          html, body { margin: 0; width: 100%; height: 100%; overflow: hidden; background: #050816; }
          main { display: flex; width: 100vw; height: 100vh; }
          section { width: 50vw; height: 100vh; display: grid; place-items: center; background: #050816; }
          img { display: block; width: 100%; height: 100%; object-fit: contain; }
        </style></head><body>
          <main>
            <section><img alt="Mockup source" src="${dataUri(mockupPanel)}" /></section>
            <section><img alt="Captured app" src="${dataUri(appBytes)}" /></section>
          </main>
        </body></html>
      `);
      await waitForImages(pairPage);
      await pairPage.screenshot({ path: pairPath, type: 'png' });
    } finally {
      await pairPage.close();
    }
  } finally {
    await mockupPage.close();
  }
}

async function addCaptureStabilizers(page: TestContext['page']): Promise<void> {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.addStyleTag({ content: FREEZE_TRANSITIONS });
  await page.evaluate(async () => {
    await document.fonts?.ready;
    window.scrollTo(0, 0);
    document.querySelector('main')?.scrollTo(0, 0);
  });
}

async function navigateAndWait(page: TestContext['page'], route: string): Promise<void> {
  await page.clock.setFixedTime(FIXED_NOW);
  await page.goto(`${BASE_URL}${route}`, {
    timeout: 10_000,
    waitUntil: 'domcontentloaded',
  });
  await page.waitForLoadState('domcontentloaded');
  await page.waitForTimeout(500);
  await addCaptureStabilizers(page);
}

async function seedDemoTree(page: TestContext['page']): Promise<void> {
  await page.waitForFunction(
    () =>
      typeof (window as unknown as { __canopySeedDemoTree?: unknown }).__canopySeedDemoTree ===
      'function',
    { timeout: 10_000 },
  );
  await page.evaluate(() => {
    (window as unknown as { __canopySeedDemoTree?: () => void }).__canopySeedDemoTree?.();
  });
  await page.waitForFunction(
    () => document.querySelectorAll('.react-flow__node').length > 0,
    { timeout: 10_000 },
  );
  await page.waitForSelector('textarea', { state: 'visible', timeout: 10_000 });

  // React Flow's initial `fitView` runs while the graph is still mounting,
  // so its animated viewport can land at a different offset between ticks.
  // Keep the real route untouched, but wait beyond its 300ms fit animation
  // after the graph and composer are both in the DOM.
  await page.waitForTimeout(1_000);
}

async function selectFirstTree(
  page: TestContext['page'],
  selector: '#nodes-tree-select' | '#topics-tree-select',
): Promise<void> {
  await page.waitForSelector(selector, { state: 'visible', timeout: 10_000 });
  const optionValues = await page.locator(`${selector} option`).evaluateAll((options) =>
    options
      .map((option) => (option as HTMLOptionElement).value)
      .filter((value) => value.length > 0),
  );
  const firstValue = optionValues[0];
  if (firstValue) {
    await page.locator(selector).selectOption(firstValue);
    await page.waitForTimeout(1_000);
  } else {
    console.warn(`⚠ No backend tree available for ${selector}; capturing the documented empty state`);
  }
}

const VISUAL_CASES: readonly VisualCase[] = [
  {
    id: 'mockup-1-graph-nav',
    label: 'mockup 1 — graph navigation',
    route: '/tree/demo',
    mockupPath: '/tmp/mockups/mockup-1.png',
    goldenName: 'mockup-1-graph-nav.png',
    pairName: 'pair-1.png',
    prepare: async (page) => {
      await navigateAndWait(page, '/tree/demo');
      await seedDemoTree(page);
    },
  },
  {
    id: 'mockup-2-cards',
    label: 'mockup 2 — cards / node hierarchy',
    route: '/nodes',
    mockupPath: '/tmp/mockups/mockup-2.png',
    goldenName: 'mockup-2-cards.png',
    pairName: 'pair-2.png',
    prepare: async (page) => {
      await navigateAndWait(page, '/nodes');
      await selectFirstTree(page, '#nodes-tree-select');
    },
  },
  {
    id: 'mockup-3-collaboration',
    label: 'mockup 3 — collaboration / approvals',
    route: '/approvals',
    mockupPath: '/tmp/mockups/mockup-3.png',
    goldenName: 'mockup-3-collaboration.png',
    pairName: 'pair-3.png',
    prepare: async (page) => {
      await navigateAndWait(page, '/approvals');
      await page.waitForSelector('h1:text-is("Approvals")', {
        state: 'visible',
        timeout: 10_000,
      });
      await page.waitForTimeout(1_000);
    },
  },
  {
    id: 'mockup-4-topics',
    label: 'mockup 4 — topics / references',
    route: '/topics',
    mockupPath: '/tmp/mockups/mockup-4.png',
    goldenName: 'mockup-4-topics.png',
    pairName: 'pair-4.png',
    prepare: async (page) => {
      await navigateAndWait(page, '/topics');
      await selectFirstTree(page, '#topics-tree-select');
    },
  },
];

async function captureAndCompare(
  ctx: TestContext,
  visualCase: VisualCase,
): Promise<DiffResult | null> {
  await visualCase.prepare(ctx.page);
  await addCaptureStabilizers(ctx.page);
  const actualBytes = await ctx.page.screenshot({ type: 'png' });
  const goldenPath = resolve(GOLDEN_ROOT, visualCase.goldenName);
  const pairPath = resolve(PAIR_ROOT, visualCase.pairName);
  const mockupBytes = await readFile(visualCase.mockupPath);

  if (UPDATE_VISUAL_GOLDENS) {
    await writeFile(goldenPath, actualBytes);
    await capturePair(ctx, mockupBytes, actualBytes, pairPath);
    console.log(`Updated ${goldenPath}`);
    console.log(`Updated ${pairPath}`);
    return null;
  }

  let expectedBytes: Buffer;
  try {
    expectedBytes = await readFile(goldenPath);
  } catch {
    throw new Error(
      `Missing golden ${goldenPath}. Run UPDATE_VISUAL_GOLDENS=1 npm run test:integration -- visual-regression.test.ts first.`,
    );
  }

  const diff = comparePngs(actualBytes, expectedBytes);
  if (diff.diffRatio > MAX_DIFF_PIXEL_RATIO || diff.width !== VIEWPORT.width || diff.height !== VIEWPORT.height) {
    await mkdir(FAILURE_ROOT, { recursive: true });
    const actualPath = resolve(FAILURE_ROOT, `${visualCase.id}-current.png`);
    await writeFile(actualPath, actualBytes);
    throw new Error(
      `${visualCase.label} drifted: ${diff.diffPixels} pixels (${(diff.diffRatio * 100).toFixed(3)}%) differ; ` +
        `max channel delta ${diff.maxChannelDelta}; allowed ratio ${MAX_DIFF_PIXEL_RATIO}. ` +
        `Current capture: ${actualPath}. Intentional changes: UPDATE_VISUAL_GOLDENS=1 npm run test:integration -- visual-regression.test.ts`,
    );
  }

  return diff;
}

describe('UI-09 visual regression baseline', () => {
  let ctx: TestContext | undefined;
  let serverAvailable = false;

  beforeAll(async () => {
    serverAvailable = await isServerRunning();
    if (!serverAvailable) return;
    await mkdir(GOLDEN_ROOT, { recursive: true });
    await mkdir(PAIR_ROOT, { recursive: true });
    ctx = await createTestContext();
    await ctx.page.clock.setFixedTime(FIXED_NOW);
  }, 30_000);

  afterAll(async () => {
    if (ctx) await destroyContext(ctx);
  });

  for (const visualCase of VISUAL_CASES) {
    it(`captures and diffs ${visualCase.label} at 1440x900`, async () => {
      if (!serverAvailable || !ctx) {
        console.warn('⚠ Dev server not running — skipping visual regression test');
        return;
      }

      const diff = await captureAndCompare(ctx, visualCase);
      if (diff) {
        expect(diff.width).toBe(VIEWPORT.width);
        expect(diff.height).toBe(VIEWPORT.height);
        expect(diff.diffRatio).toBeLessThanOrEqual(MAX_DIFF_PIXEL_RATIO);
      } else {
        expect(UPDATE_VISUAL_GOLDENS).toBe(true);
      }
    }, 30_000);
  }
});
