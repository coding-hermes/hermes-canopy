/**
 * PWA installability contract (GAP-082)
 *
 * The product vision has always claimed a PWA, but nothing tied the SHELL to a
 * web app manifest: `frontend/index.html` carried no `rel="manifest"`, there was
 * no manifest in `frontend/public/`, and the service worker precached only the
 * document — so the app was not installable and not launchable offline.
 *
 * These tests read the REAL files (no mocks): `index.html`, `sw.ts`,
 * `public/manifest.webmanifest`, the PNGs under `public/`, and the generator
 * script that produces them. `import.meta.glob` is the node-free way to reach
 * build-time files outside `src/` and is already the pattern used by
 * `src/lib/__tests__/offlineQueue.test.ts` for `sw.ts`.
 *
 * A note on the assertion style: every read is paired with a guard that it
 * found something. A glob that silently matched nothing would otherwise turn
 * every check below into a vacuous pass — the exact failure mode this suite
 * exists to prevent (a "shipped PWA" that was never linked).
 */

import { describe, it, expect } from 'vitest';

/**
 * Read a build-time file as raw text (the app tsconfig only loads vite/client).
 *
 * `import.meta.glob` requires LITERAL patterns — a shared helper taking the
 * pattern compiles fine but fails at transform time with "Could only use
 * literals", so the calls below are written out in full.
 */
const SHELL = import.meta.glob('/index.html', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;

const WORKER = import.meta.glob('/sw.ts', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;

const MANIFEST = import.meta.glob('/public/manifest.webmanifest', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;

const PUBLIC_FILES = import.meta.glob('/public/*', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;

const GENERATOR = import.meta.glob('/scripts/generate-pwa-icons.mjs', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;

const indexHtml = SHELL['/index.html'] ?? '';
const swSource = WORKER['/sw.ts'] ?? '';
const manifestText = MANIFEST['/public/manifest.webmanifest'] ?? '';
const generatorSource = GENERATOR['/scripts/generate-pwa-icons.mjs'] ?? '';

interface ManifestIcon {
  src?: string;
  sizes?: string;
  type?: string;
  purpose?: string;
}

interface Manifest {
  name?: string;
  short_name?: string;
  description?: string;
  id?: string;
  start_url?: string;
  scope?: string;
  display?: string;
  background_color?: string;
  theme_color?: string;
  lang?: string;
  icons?: ManifestIcon[];
}

/** The manifest, parsed once — a parse failure must be a test failure, not a crash. */
let manifest: Manifest = {};
let manifestParseError: string | null = null;
try {
  manifest = JSON.parse(manifestText) as Manifest;
} catch (error) {
  manifestParseError = error instanceof Error ? error.message : String(error);
}

const icons: ManifestIcon[] = manifest.icons ?? [];

/** The service worker's precache list, read out of its own source text. */
function staticUrls(source: string): string[] {
  const block = /const STATIC_URLS = \[([\s\S]*?)\];/.exec(source);
  if (!block) return [];
  return [...block[1].matchAll(/'([^']*)'/g)].map((match) => match[1]);
}

/** `frontend/public/<name>` exists AND is a readable, non-empty file. */
function publicFile(name: string): string | undefined {
  const key = `/public/${name.replace(/^\//, '')}`;
  const contents = PUBLIC_FILES[key];
  return contents !== undefined && contents.length > 0 ? contents : undefined;
}

/** The size a manifest `sizes` string declares, as [width, height]. */
function declaredPair(sizes: string | undefined): [number, number] | null {
  const match = /^(\d+)x(\d+)$/.exec(sizes ?? '');
  return match ? [Number(match[1]), Number(match[2])] : null;
}

// ─── Non-vacuous reads ─────────────────────────────────────────────────

describe('the suite really read the shell, the worker and the manifest', () => {
  it('found every file it asserts about', () => {
    expect(indexHtml.length).toBeGreaterThan(200);
    expect(swSource.length).toBeGreaterThan(1000);
    expect(manifestText.length).toBeGreaterThan(100);
    expect(generatorSource.length).toBeGreaterThan(1000);
    expect(PUBLIC_FILES['/public/favicon.svg']).toBeDefined();
    expect(Object.keys(PUBLIC_FILES).length).toBeGreaterThan(3);
  });

  it('parsed the manifest as JSON', () => {
    expect(manifestParseError).toBeNull();
    expect(manifest).toBeTypeOf('object');
  });
});

// ─── a. the document links the manifest ────────────────────────────────

describe('index.html advertises the manifest and the touch icon', () => {
  const doc = new DOMParser().parseFromString(indexHtml, 'text/html');
  const links = [...doc.querySelectorAll('link')];

  it('parsed the document into real link elements', () => {
    // Guards the queries below against a parse that produced nothing. Kept
    // BELOW the count the manifest link itself contributes, so a missing
    // manifest link fails one test rather than tripping this guard too.
    expect(links.length).toBeGreaterThanOrEqual(2);
    expect(doc.querySelector('title')?.textContent).toBe('Hermes Canopy');
  });

  it('links the web app manifest at /manifest.webmanifest', () => {
    const manifestLink = doc.querySelector('link[rel="manifest"]');
    expect(manifestLink).not.toBeNull();
    expect(manifestLink?.getAttribute('href')).toBe('/manifest.webmanifest');
  });

  it('links an apple-touch-icon that exists in public/', () => {
    const touchIcon = doc.querySelector('link[rel="apple-touch-icon"]');
    expect(touchIcon).not.toBeNull();
    const href = touchIcon?.getAttribute('href') ?? '';
    expect(href).toMatch(/^\/icon-\d+\.png$/);
    expect(publicFile(href)).toBeDefined();
  });

  it('left the pre-existing head declarations untouched', () => {
    expect(doc.querySelector('link[rel="icon"][href="/favicon.svg"]')).not.toBeNull();
    expect(doc.querySelector('meta[name="viewport"]')?.getAttribute('content')).toBe(
      'width=device-width, initial-scale=1.0',
    );
    expect(doc.querySelector('meta[name="color-scheme"]')?.getAttribute('content')).toBe('dark');
    expect(doc.querySelector('meta[name="theme-color"]')?.getAttribute('content')).toBe('#0B0D17');
    expect(doc.querySelector('title')?.textContent).toBe('Hermes Canopy');
  });
});

// ─── b. the manifest's own fields ──────────────────────────────────────

describe('the manifest describes the installed app', () => {
  it('carries the identity the browser shows at install time', () => {
    expect(manifest.name).toBe('Hermes Canopy');
    expect(manifest.short_name).toBe('Canopy');
    expect(manifest.description).toBeTypeOf('string');
    expect((manifest.description ?? '').length).toBeGreaterThan(20);
    expect(manifest.lang).toBe('en');
  });

  it('scopes the installed app to the served root', () => {
    expect(manifest.id).toBe('/');
    expect(manifest.start_url).toBe('/');
    expect(manifest.scope).toBe('/');
  });

  it('declares an installable display mode', () => {
    expect(['standalone', 'minimal-ui', 'fullscreen']).toContain(manifest.display);
    expect(manifest.display).toBe('standalone');
  });

  it('uses the shell\u2019s own theme colour, in both manifest slots', () => {
    expect(manifest.theme_color).toBe('#0B0D17');
    expect(manifest.background_color).toBe('#0B0D17');

    // …and the shell keeps the matching <meta> — a mismatch would make the
    // installed window paint a different colour than the app inside it.
    const doc = new DOMParser().parseFromString(indexHtml, 'text/html');
    const meta = doc.querySelector('meta[name="theme-color"]')?.getAttribute('content');
    expect(meta).toBe(manifest.theme_color);
  });
});

// ─── c. the icon set the platforms require ─────────────────────────────

describe('the manifest declares a usable icon set', () => {
  /** The first icon matching both a size and a purpose predicate. */
  function iconWith(size: string, wanted: (purpose: string) => boolean): ManifestIcon | undefined {
    return icons.find((icon) => icon.sizes === size && wanted(icon.purpose ?? ''));
  }

  it('has a 192x192 icon for the install prompt', () => {
    expect(icons.length).toBeGreaterThanOrEqual(3);
    expect(iconWith('192x192', (p) => p === 'any')).toBeDefined();
  });

  it('has a 512x512 "any" icon for the launcher and splash screen', () => {
    expect(iconWith('512x512', (p) => p === 'any')).toBeDefined();
  });

  it('has a maskable 512x512 icon for adaptive launchers', () => {
    expect(iconWith('512x512', (p) => p.includes('maskable'))).toBeDefined();
  });

  it('types every icon as a PNG', () => {
    expect(icons.length).toBeGreaterThan(0);
    for (const icon of icons) {
      expect(icon.type, `${icon.src} type`).toBe('image/png');
    }
  });
});

// ─── d. every declared icon is a real file ─────────────────────────────

describe('every declared icon resolves to a file under frontend/public/', () => {
  it('declares at least the three required icons', () => {
    expect(icons.length).toBeGreaterThanOrEqual(3);
  });

  it('resolves each src to an existing, non-empty file', () => {
    const missing: string[] = [];
    for (const icon of icons) {
      const src = icon.src ?? '';
      if (!src.startsWith('/')) {
        missing.push(`${src || '<no src>'} (not root-absolute)`);
        continue;
      }
      if (publicFile(src) === undefined) missing.push(src);
    }
    expect(missing).toEqual([]);
  });

  it('names each file after the width it declares', () => {
    // A size/file mismatch is invisible to the browser until install time; the
    // generator is what closes it, so pin the naming convention here too.
    // (The convention is the WIDTH — `/icon-512.png` declares "512x512".)
    const mismatched: string[] = [];
    for (const icon of icons) {
      const pair = declaredPair(icon.sizes);
      const src = icon.src ?? '';
      if (!pair) {
        mismatched.push(`${src}: sizes=${icon.sizes ?? '<none>'}`);
        continue;
      }
      if (!src.includes(String(pair[0]))) mismatched.push(`${src}: sizes=${icon.sizes}`);
    }
    expect(mismatched).toEqual([]);
  });

  it('agrees with the generator on what each target is', () => {
    // The PNGs are produced by frontend/scripts/generate-pwa-icons.mjs. If the
    // manifest gains an icon the generator does not emit (or vice versa), the
    // file check above would only catch it once the generator had been re-run.
    const declaredSources = icons.map((icon) => (icon.src ?? '').replace(/^\//, ''));
    const generated: string[] = [
      ...generatorSource.matchAll(/file: '([^']+\.png)'/g),
    ].map((match) => match[1]);
    expect(generated.length).toBeGreaterThanOrEqual(3);
    expect([...declaredSources].sort()).toEqual([...generated].sort());

    for (const icon of icons) {
      const name = (icon.src ?? '').replace(/^\//, '');
      // The generator's own entry for this file must declare the same size.
      const entry = new RegExp(`\\{[^}]*file: '${name}'[^}]*\\}`).exec(generatorSource);
      expect(entry, `${name} entry in the generator`).not.toBeNull();
      const pair = declaredPair(icon.sizes);
      expect(entry?.[0]).toContain(`size: ${pair?.[0]}`);
    }
  });
});

// ─── e. the worker precaches the installable shell ─────────────────────

describe('the service worker precaches the installable shell', () => {
  const precached = staticUrls(swSource);

  it('read a real precache list out of sw.ts', () => {
    // Guards against the regex silently matching nothing.
    expect(precached.length).toBeGreaterThanOrEqual(2);
  });

  it('keeps the document precached', () => {
    expect(precached).toContain('/');
    expect(precached).toContain('/index.html');
  });

  it('precaches the manifest and every declared icon', () => {
    expect(precached).toContain('/manifest.webmanifest');
    for (const icon of icons) {
      expect(precached, `${icon.src} precached`).toContain(icon.src);
    }
  });

  it('precaches only URLs that resolve to served files', () => {
    // '/' is the SPA document (index.html), everything else must be a real
    // file under public/ — a typo here would fail the SW install silently.
    const unresolved = precached.filter((url) => {
      if (url === '/' || url === '/index.html') return false;
      return publicFile(url) === undefined;
    });
    expect(unresolved).toEqual([]);
  });
});
