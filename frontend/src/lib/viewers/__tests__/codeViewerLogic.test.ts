import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  detectCodeLanguage,
  findCodeMatches,
  normalizeCodeConfig,
  truncateCodeText,
} from '../codeViewerLogic';
import { codeHelpers, codeViewerBody } from '../codeViewerBody';

type HandlerMap = Record<string, Array<(payload: unknown) => void>>;

function ensureRoot(): HTMLElement {
  const root = document.createElement('div');
  root.id = 'root';
  document.body.appendChild(root);
  return root;
}

function installShim(text: string | Promise<string>, config: Record<string, unknown> = {}) {
  const handlers: HandlerMap = {};
  const getTextContent = vi.fn(() => Promise.resolve(text));
  const logAccess = vi.fn(() => Promise.resolve({ ok: true }));
  const ready = vi.fn(() => Promise.resolve({ ok: true }));
  (window as unknown as { canopy: unknown }).canopy = {
    fileId: 'code-1',
    __handlers: handlers,
    __bootstrap: { fileMeta: { id: 'code-1', filename: 'sample.ts', mimeType: 'text/plain' }, config },
    viewer: { getTextContent, logAccess, ready },
  };
  return { handlers, getTextContent, logAccess, ready };
}

async function flush(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

afterEach(() => {
  document.getElementById('root')?.remove();
  delete (window as unknown as { canopy?: unknown }).canopy;
  vi.restoreAllMocks();
});

describe('code viewer pure logic', () => {
  it('detects by extension before filename pattern and content', () => {
    expect(detectCodeLanguage('Dockerfile.ts', '#!/bin/sh\necho hi')).toEqual({
      language: 'typescript',
      confidence: 1,
      source: 'extension',
    });
    expect(detectCodeLanguage('Dockerfile', 'const value = 1')).toEqual({
      language: 'shell',
      confidence: 0.96,
      source: 'filename',
    });
    expect(detectCodeLanguage('unknown.data', 'package main\n\nfunc main() {}')).toMatchObject({
      language: 'go',
      source: 'content',
    });
  });

  it('sniffs only the first 4 KiB and remains honest for unknown text', () => {
    const markerAfterBoundary = 'x'.repeat(4096) + '\npackage main\nfunc main() {}';
    expect(detectCodeLanguage('unknown.data', markerAfterBoundary)).toEqual({
      language: 'plaintext',
      confidence: 0,
      source: 'default',
    });
    expect(detectCodeLanguage('unknown.data', 'x'.repeat(4096))).toEqual({
      language: 'plaintext',
      confidence: 0,
      source: 'default',
    });
  });

  it('covers deterministic family extensions and bounded config', () => {
    expect(detectCodeLanguage('query.sql', '')).toMatchObject({ language: 'sql', source: 'extension' });
    expect(detectCodeLanguage('view.scss', '')).toMatchObject({ language: 'scss', source: 'extension' });
    expect(detectCodeLanguage('main.kt', '')).toMatchObject({ language: 'kotlin', source: 'extension' });
    expect(normalizeCodeConfig({ showLineNumbers: false, tabSize: 8, wordWrap: true, fontSize: 99, fontFamily: 'Fira Code, monospace' })).toEqual({
      showLineNumbers: false,
      tabSize: 8,
      wordWrap: 'on',
      fontSize: 20,
      fontFamily: 'Fira Code, monospace',
    });
    expect(normalizeCodeConfig({ tabSize: 3, wordWrap: 'invalid', fontSize: 1, fontFamily: 'x; color:red' })).toEqual({
      showLineNumbers: true,
      tabSize: 4,
      wordWrap: 'off',
      fontSize: 12,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
    });
    expect(truncateCodeText('abcdef', 3)).toEqual({ text: 'abc', truncated: true, originalLength: 6 });
    expect(findCodeMatches('Alpha\nalpha', 'AL')).toEqual([
      { line: 1, column: 1, length: 2 },
      { line: 2, column: 1, length: 2 },
    ]);
  });
});

describe('serialized code viewer body', () => {
  it('serializes all helpers and executes without free module identifiers', async () => {
    expect(codeHelpers.detectCodeLanguage('x.py', '')).toMatchObject({ language: 'python' });
    expect(() => new Function(codeViewerBody)).not.toThrow();
    ensureRoot();
    const shim = installShim('print("hello")');
    const detected: unknown[] = [];
    const readyEvents: unknown[] = [];
    const cursorEvents: unknown[] = [];
    shim.handlers.code_language_detected = [(payload) => detected.push(payload)];
    shim.handlers.code_ready = [(payload) => readyEvents.push(payload)];
    shim.handlers.code_cursor_moved = [(payload) => cursorEvents.push(payload)];
    new Function(codeViewerBody)();
    await flush();
    expect(detected).toEqual([{ language: 'typescript', confidence: 1, source: 'extension' }]);
    expect(readyEvents).toEqual([{ language: 'typescript' }]);
    expect(cursorEvents).toContainEqual({ line: 1, column: 1 });
    expect(shim.getTextContent).toHaveBeenCalledTimes(1);
    expect(shim.ready).toHaveBeenCalledTimes(1);
    expect(shim.logAccess).toHaveBeenCalledWith('preview_text', expect.objectContaining({ activity: 'view', readOnly: true }));
  });

  it('renders XSS payloads as inert text and honors read-only config', async () => {
    const root = ensureRoot();
    installShim('<script>alert(1)</script>\n<img src=x onerror=alert(2)>', {
      showLineNumbers: false,
      tabSize: 2,
      wordWrap: true,
      fontSize: 20,
      fontFamily: 'Fira Code, monospace',
    });
    new Function(codeViewerBody)();
    await flush();
    expect(root.querySelectorAll('[data-code-line]')).toHaveLength(2);
    expect(root.querySelector('[data-code-gutter]')).toBeNull();
    expect(root.querySelector('[data-code-text]')?.textContent).toContain('<script>alert(1)</script>');
    expect(root.querySelector('img')).toBeNull();
    expect(root.querySelectorAll('script')).toHaveLength(0);
    const css = root.querySelector('[data-code-style]')?.textContent || '';
    expect(css).toContain('tab-size:2');
    expect(css).toContain('font-size:20px');
    expect(css).toContain('Fira Code, monospace');
  });

  it('supports search next/previous/escape and go-to-line with cursor events', async () => {
    const root = ensureRoot();
    const shim = installShim('one\ntwo one\none');
    const cursorEvents: Array<Record<string, unknown>> = [];
    shim.handlers.code_cursor_moved = [(payload) => cursorEvents.push(payload as Record<string, unknown>)];
    new Function(codeViewerBody)();
    await flush();

    window.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'f', ctrlKey: true, cancelable: true }));
    const search = root.querySelector('[data-code-search]') as HTMLElement;
    const searchInput = root.querySelector('[data-code-search-input]') as HTMLInputElement;
    expect(search.style.display).toBe('inline-flex');
    searchInput.value = 'one';
    searchInput.dispatchEvent(new window.Event('input'));
    expect(root.querySelector('[data-code-search-count]')?.textContent).toBe('1/3');
    expect(root.querySelectorAll('[data-code-match]')).toHaveLength(3);
    expect(root.querySelector('[data-current-match="true"]')?.textContent).toBe('one');
    searchInput.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Enter', cancelable: true }));
    expect(root.querySelector('[data-code-search-count]')?.textContent).toBe('2/3');
    searchInput.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Enter', shiftKey: true, cancelable: true }));
    expect(root.querySelector('[data-code-search-count]')?.textContent).toBe('1/3');
    searchInput.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Escape', cancelable: true }));
    expect(search.style.display).toBe('none');

    window.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'g', metaKey: true, cancelable: true }));
    const gotoInput = root.querySelector('[data-code-goto-input]') as HTMLInputElement;
    expect(gotoInput.parentElement?.getAttribute('style')).toContain('display: inline-flex');
    gotoInput.value = '2';
    gotoInput.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Enter', cancelable: true }));
    expect(cursorEvents).toContainEqual({ line: 2, column: 1 });
    expect(root.querySelector('[data-code-line="2"]')?.getAttribute('data-code-cursor')).toBe('true');
  });

  it('discloses bounded rendering for a large source instead of freezing the frame', async () => {
    const root = ensureRoot();
    installShim('x'.repeat(2_000_001));
    new Function(codeViewerBody)();
    await flush();
    expect(root.querySelector('[data-code-truncated]')?.textContent).toContain('2000001 characters');
    expect(root.querySelectorAll('[data-code-line]')).toHaveLength(1);
  });

  it('emits honest code_error and renders an alert when retrieval fails', async () => {
    const root = ensureRoot();
    const handlers: HandlerMap = {};
    const error = new Error('backend unavailable');
    const logAccess = vi.fn(() => Promise.resolve({ ok: true }));
    (window as unknown as { canopy: unknown }).canopy = {
      __handlers: handlers,
      __bootstrap: { fileMeta: { filename: 'broken.ts' }, config: {} },
      viewer: { getTextContent: vi.fn(() => Promise.reject(error)), logAccess },
    };
    const errors: unknown[] = [];
    handlers.code_error = [(payload) => errors.push(payload)];
    new Function(codeViewerBody)();
    await flush();
    expect(root.querySelector('[data-code-error]')?.getAttribute('role')).toBe('alert');
    expect(root.querySelector('[data-code-error]')?.textContent).toContain('TEXT_CONTENT_ERROR');
    expect(errors).toEqual([{ code: 'TEXT_CONTENT_ERROR', message: 'backend unavailable' }]);
    expect(logAccess).toHaveBeenCalledWith('error', expect.objectContaining({ errorCode: 'TEXT_CONTENT_ERROR' }));
  });
});
