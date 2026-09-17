#!/usr/bin/env node
/**
 * Hermes Canopy — PWA icon generator (GAP-082)
 *
 * Rasterises the EXISTING brand art (frontend/public/favicon.svg) into the
 * three PNGs declared by frontend/public/manifest.webmanifest. It reuses the
 * Playwright chromium already present in frontend/node_modules: there is no
 * system SVG rasteriser on this host (rsvg-convert / inkscape / convert /
 * cairosvg are all absent) and this task adds no dependency.
 *
 * INVOCATION (from anywhere — every path is derived from this file's location):
 *
 *     node frontend/scripts/generate-pwa-icons.mjs
 *
 * OUTPUTS (frontend/public/, which vite serves verbatim in dev and copies
 * verbatim into dist/ at build time):
 *
 *     icon-192.png           192x192   transparent ground, purpose "any"
 *     icon-512.png           512x512   transparent ground, purpose "any"
 *     icon-maskable-512.png  512x512   #0B0D17 ground,   purpose "maskable"
 *
 * HARD CHECKS (any failure prints the reason and exits 1):
 *   1. every produced file carries a real IHDR size equal to the size its
 *      manifest entry declares (read from the PNG bytes, not assumed);
 *   2. the maskable variant's ink lies inside the maskable safe zone — the
 *      centred circle of 80% diameter — measured from the WRITTEN file's
 *      pixels, not from the geometry that produced it.
 *
 * IDEMPOTENCE: rendering is a pure function of favicon.svg, the target table
 * and the chromium build, so re-running on unchanged inputs produces
 * byte-identical files (each sha256 is printed; an identical file is left
 * untouched instead of rewritten).
 *
 * The maskable scale is DERIVED, not guessed: a probe pass measures the art's
 * real maximum ink radius at full bleed (the favicon's bounding box is 48x46,
 * but its ink never reaches the bbox corners), and the final scale is chosen so
 * that radius lands just inside the safe circle. The written PNG is then
 * measured again to prove it.
 */

import { chromium } from 'playwright';
import { readFileSync, writeFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

// ─── Paths ────────────────────────────────────────────────────────────

const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url));
const PUBLIC_DIR = join(SCRIPT_DIR, '..', 'public');
const FAVICON_PATH = join(PUBLIC_DIR, 'favicon.svg');
const MANIFEST_PATH = join(PUBLIC_DIR, 'manifest.webmanifest');

// ─── Target table ─────────────────────────────────────────────────────

/**
 * `fill` (plain variants): the fraction of the canvas the art's bounding box
 * spans on its long side — a ~6% margin per side, the usual optical padding
 * for a non-maskable app icon.
 *
 * The maskable variant is not driven by `fill`: its scale is derived from a
 * measured ink radius so the art provably fits the 80%-diameter safe circle.
 */
const ICON_TARGETS = [
  { file: 'icon-192.png', size: 192, fill: 0.88, background: null },
  { file: 'icon-512.png', size: 512, fill: 0.88, background: null },
  { file: 'icon-maskable-512.png', size: 512, background: '#0B0D17', maskable: true },
];

/** The maskable safe zone: ink must stay inside the centred 80%-diameter circle. */
const SAFE_ZONE_DIAMETER_FRACTION = 0.8;
/** Aim just inside the safe radius so anti-aliasing cannot straddle the edge. */
const MASKABLE_TARGET_RADIUS_FRACTION = SAFE_ZONE_DIAMETER_FRACTION / 2 - 0.005;

// ─── Small helpers ────────────────────────────────────────────────────

/** Real pixel size of a PNG, read from its IHDR chunk. */
function pngIhdrSize(buffer) {
  const PNG_SIGNATURE = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  if (buffer.length < 24 || !buffer.subarray(0, 8).equals(PNG_SIGNATURE)) {
    throw new Error('not a PNG (signature mismatch)');
  }
  if (buffer.subarray(12, 16).toString('latin1') !== 'IHDR') {
    throw new Error('not a PNG (first chunk is not IHDR)');
  }
  return { width: buffer.readUInt32BE(16), height: buffer.readUInt32BE(20) };
}

const sha256 = (buffer) => createHash('sha256').update(buffer).digest('hex');

/**
 * Write only when the bytes change, so a re-run is a no-op on disk.
 * Returns 'written' or 'unchanged'.
 */
function writeIfChanged(path, buffer) {
  let current = null;
  try {
    current = readFileSync(path);
  } catch {
    current = null;
  }
  if (current && current.equals(buffer)) return 'unchanged';
  writeFileSync(path, buffer);
  return 'written';
}

/**
 * The favicon SVG, re-declared at an explicit intrinsic size so chromium
 * rasterises the vector art AT the destination size (crisp) instead of
 * upscaling its 48x46 intrinsic bitmap.
 */
function svgAtSize(svg, width, height) {
  const resized = svg.replace(
    /<svg\b[^>]*>/,
    (tag) =>
      tag
        .replace(/\swidth="[^"]*"/, ` width="${width}"`)
        .replace(/\sheight="[^"]*"/, ` height="${height}"`),
  );
  if (resized === svg) throw new Error('could not re-declare the SVG size');
  return `data:image/svg+xml;base64,${Buffer.from(resized, 'utf8').toString('base64')}`;
}

// ─── In-page rasteriser ───────────────────────────────────────────────

/**
 * Installs `window.__pwa`, a tiny canvas toolkit, into the browser page.
 * Everything the generator needs from chromium lives here: drawing the art,
 * measuring its ink radius, and encoding a canvas to PNG bytes.
 */
function installHarness() {
  const INK_ALPHA_THRESHOLD = 8;
  const INK_CHANNEL_TOLERANCE = 4;

  function contextFor(canvas, background) {
    const ctx = canvas.getContext('2d');
    ctx.clearRect(0, 0, canvas.width, canvas.height);
    if (background) {
      ctx.fillStyle = background;
      ctx.fillRect(0, 0, canvas.width, canvas.height);
    }
    return ctx;
  }

  /**
   * Furthest distance from the canvas centre to an ink pixel.
   *
   * "Ink" is decided conservatively: any pixel with alpha above a low
   * threshold on a transparent ground, or any pixel differing from the ground
   * colour on an opaque one. Anti-aliased edge pixels therefore COUNT, which
   * can only overstate the radius.
   */
  function maxInkRadius(canvas, blank, background) {
    const ctx = canvas.getContext('2d');
    const { width, height } = canvas;
    const data = ctx.getImageData(0, 0, width, height).data;
    let best = 0;
    for (let y = 0; y < height; y += 1) {
      for (let x = 0; x < width; x += 1) {
        const i = (y * width + x) * 4;
        let ink;
        if (background === null) {
          ink = data[i + 3] > INK_ALPHA_THRESHOLD;
        } else {
          // `blank` is the ground COLOUR (4 bytes), not a pixel array — it must
          // be indexed 0/1/2, never by the pixel's byte offset, or every
          // comparison becomes `x - undefined` = NaN and silently reports "no
          // ink anywhere" (which reads exactly like a perfectly fitted icon).
          ink =
            Math.abs(data[i] - blank[0]) > INK_CHANNEL_TOLERANCE ||
            Math.abs(data[i + 1] - blank[1]) > INK_CHANNEL_TOLERANCE ||
            Math.abs(data[i + 2] - blank[2]) > INK_CHANNEL_TOLERANCE;
        }
        if (!ink) continue;
        const dx = x + 0.5 - width / 2;
        const dy = y + 0.5 - height / 2;
        const r = Math.hypot(dx, dy);
        if (r > best) best = r;
      }
    }
    return best;
  }

  window.__pwa = {
    async load(source) {
      const image = new Image();
      image.src = source;
      await image.decode();
      return image;
    },

    /** Draw the art into `box` on a `size`x`size` canvas and encode it to PNG. */
    render(image, size, box, background) {
      const canvas = document.createElement('canvas');
      canvas.width = size;
      canvas.height = size;
      const ctx = contextFor(canvas, background);
      ctx.drawImage(image, box.x, box.y, box.width, box.height);
      return canvas.toDataURL('image/png').replace(/^data:image\/png;base64,/, '');
    },

    /** The ground colour as real pixel bytes, for the opaque-ground comparison. */
    groundBytes(background) {
      const probe = document.createElement('canvas');
      probe.width = 1;
      probe.height = 1;
      const pctx = probe.getContext('2d');
      pctx.fillStyle = background;
      pctx.fillRect(0, 0, 1, 1);
      return pctx.getImageData(0, 0, 1, 1).data;
    },

    /** Ink radius of the art as laid out in `box` on a `size` canvas. */
    measure(image, size, box, background) {
      const canvas = document.createElement('canvas');
      canvas.width = size;
      canvas.height = size;
      const ctx = contextFor(canvas, background);
      ctx.drawImage(image, box.x, box.y, box.width, box.height);
      const blank = background === null ? null : window.__pwa.groundBytes(background);
      return maxInkRadius(canvas, blank, background);
    },

    /** Ink radius of an already-written PNG, measured from its real pixels. */
    async measurePng(pngBase64, background) {
      const image = await window.__pwa.load(`data:image/png;base64,${pngBase64}`);
      const canvas = document.createElement('canvas');
      canvas.width = image.naturalWidth;
      canvas.height = image.naturalHeight;
      const ctx = contextFor(canvas, background);
      ctx.drawImage(image, 0, 0);
      const blank = background === null ? null : window.__pwa.groundBytes(background);
      return {
        radius: maxInkRadius(canvas, blank, background),
        width: canvas.width,
        height: canvas.height,
      };
    },
  };
}

// ─── Main ─────────────────────────────────────────────────────────────

async function main() {
  const svg = readFileSync(FAVICON_PATH, 'utf8');
  const manifest = JSON.parse(readFileSync(MANIFEST_PATH, 'utf8'));
  const m = /viewBox="0 0 (\d+(?:\.\d+)?) (\d+(?:\.\d+)?)"/.exec(svg);
  if (!m) throw new Error(`no viewBox found in ${FAVICON_PATH}`);
  const viewBox = { width: Number(m[1]), height: Number(m[2]) };
  const longSide = Math.max(viewBox.width, viewBox.height);

  const browser = await chromium.launch();
  const failures = [];
  try {
    const page = await browser.newPage();
    await page.setContent('<!doctype html><html><body></body></html>');
    await page.evaluate(installHarness);

    // ── Probe pass: the art's real ink radius at full bleed ─────────────
    // Without this the maskable scale would have to fit the art's bounding
    // box (48x46) diagonally inside the safe circle, which wastes ~18% of the
    // icon on padding the art's shape does not need.
    const probeSize = 512;
    const probeWidth = probeSize;
    const probeHeight = Math.round((probeSize * viewBox.height) / viewBox.width);
    const probeRadius = await page.evaluate(
      async ({ svgUrl, size, box }) => {
        const image = await window.__pwa.load(svgUrl);
        return window.__pwa.measure(image, size, box, null);
      },
      {
        svgUrl: svgAtSize(svg, probeWidth, probeHeight),
        size: probeSize,
        box: { x: 0, y: 0, width: probeWidth, height: probeHeight },
      },
    );
    if (!(probeRadius > 0)) {
      throw new Error('probe measured no ink — the favicon did not rasterise');
    }
    console.log(
      `probe: art ink radius ${probeRadius.toFixed(1)}px at full bleed ` +
        `(scale ${(probeSize / viewBox.width).toFixed(3)}x)`,
    );

    for (const target of ICON_TARGETS) {
      const { size, file } = target;

      // ── Geometry ─────────────────────────────────────────────────────
      let box;
      if (target.maskable) {
        const targetRadius = MASKABLE_TARGET_RADIUS_FRACTION * size;
        const scale = (probeSize / viewBox.width) * (targetRadius / probeRadius);
        const width = Math.round(viewBox.width * scale);
        const height = Math.round(viewBox.height * scale);
        box = {
          width,
          height,
          x: Math.round((size - width) / 2),
          y: Math.round((size - height) / 2),
        };
      } else {
        const scale = (target.fill * size) / longSide;
        const width = Math.round(viewBox.width * scale);
        const height = Math.round(viewBox.height * scale);
        box = {
          width,
          height,
          x: Math.round((size - width) / 2),
          y: Math.round((size - height) / 2),
        };
      }

      // ── Render ───────────────────────────────────────────────────────
      const base64 = await page.evaluate(
        async ({ svgUrl, size: canvasSize, box: artBox, background }) => {
          const image = await window.__pwa.load(svgUrl);
          return window.__pwa.render(image, canvasSize, artBox, background);
        },
        {
          svgUrl: svgAtSize(svg, box.width, box.height),
          size,
          box,
          background: target.background,
        },
      );
      const png = Buffer.from(base64, 'base64');

      // ── Check 1: real IHDR size vs the manifest's declared size ──────
      const real = pngIhdrSize(png);
      const entry = (manifest.icons ?? []).find(
        (icon) => icon && typeof icon.src === 'string' && icon.src.endsWith(`/${file}`),
      );
      const declared = entry ? String(entry.sizes) : '<no manifest entry>';
      const expected = `${size}x${size}`;
      const sizeOk = real.width === size && real.height === size;
      const manifestOk = declared === expected;
      if (!sizeOk || !manifestOk) {
        failures.push(
          `${file}: IHDR ${real.width}x${real.height}, manifest declares ${declared}, ` +
            `generator intends ${expected}`,
        );
      }

      // ── Check 2 (maskable only): ink inside the safe circle ──────────
      let safeZoneNote = '';
      if (target.maskable) {
        const measured = await page.evaluate(
          async ({ pngBase64, background }) =>
            window.__pwa.measurePng(pngBase64, background),
          { pngBase64: base64, background: target.background },
        );
        const safeRadius = (SAFE_ZONE_DIAMETER_FRACTION / 2) * size;
        // A measurement that found no ink over a non-empty canvas is a broken
        // probe, not a well-fitted icon — fail loudly instead of reporting a
        // safe zone that was never actually checked.
        if (!(measured.radius > 0)) {
          failures.push(
            `${file}: the safe-zone probe found no ink over ${measured.width}x` +
              `${measured.height} real pixels — the measurement is broken`,
          );
        }
        if (measured.width !== size || measured.height !== size) {
          failures.push(
            `${file}: the written PNG decoded as ${measured.width}x${measured.height}, ` +
              `expected ${expected}`,
          );
        }
        if (measured.radius > safeRadius) {
          failures.push(
            `${file}: ink radius ${measured.radius.toFixed(1)}px exceeds the ` +
              `maskable safe radius ${safeRadius.toFixed(1)}px`,
          );
        }
        safeZoneNote =
          ` ink radius ${measured.radius.toFixed(1)}px <= ` +
          `${safeRadius.toFixed(1)}px safe zone ` +
          `(${((measured.radius / size) * 200).toFixed(1)}% diameter, ` +
          `measured over ${measured.width}x${measured.height} real pixels)`;
      }

      const status = writeIfChanged(join(PUBLIC_DIR, file), png);
      console.log(
        `${file}  ${real.width}x${real.height} (manifest ${declared}) ` +
          `${sizeOk && manifestOk ? 'OK' : 'MISMATCH'}${safeZoneNote}  ` +
          `${status}  sha256 ${sha256(png).slice(0, 16)}`,
      );
    }
  } finally {
    await browser.close();
  }

  // Every target must have a manifest entry, or check 1 above compared
  // against '<no manifest entry>' and said nothing about the gap.
  const declaredSources = (manifest.icons ?? []).map((icon) => icon?.src);
  for (const target of ICON_TARGETS) {
    if (!declaredSources.some((src) => typeof src === 'string' && src.endsWith(`/${target.file}`))) {
      failures.push(`${target.file}: produced but not declared in manifest.webmanifest`);
    }
  }

  if (failures.length > 0) {
    console.error('\nICON GENERATION FAILED:');
    for (const failure of failures) console.error(`  - ${failure}`);
    process.exit(1);
  }
  console.log(`\nPWA icons: ${ICON_TARGETS.length}/${ICON_TARGETS.length} match their manifest entries.`);
}

main().catch((error) => {
  console.error('ICON GENERATION FAILED:', error instanceof Error ? error.message : error);
  process.exit(1);
});
