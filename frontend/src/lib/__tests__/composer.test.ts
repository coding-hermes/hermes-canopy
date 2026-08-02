/**
 * Unit tests — composer helpers (UI-06 floating composer bar)
 *
 * These cover the two boundaries the composer bar owns: the copy the input
 * shows for each permission/reply state, and the snake_case body the
 * node-create endpoint actually parses. The wire shape is asserted against
 * the Go struct tags in internal/handler/node_handler.go — a camelCase
 * regression there is a silent 400, so it is pinned key by key here.
 */

import { describe, it, expect } from 'vitest';
import {
  DEFAULT_PLACEHOLDER,
  REPLY_PLACEHOLDER,
  VIEW_ONLY_PLACEHOLDER,
  MAX_CONTENT_BYTES,
  GENERIC_SEND_ERROR,
  composerPlaceholder,
  buildCreateNodeBody,
  buildSendMetadata,
  insertAtCursor,
  byteLength,
  describeSendError,
} from '../composer';

const PARENT = '018f3a4c-9d2e-7c31-b8a6-1f2e3d4c5b6a';

// ─── Placeholder derivation ────────────────────────────────────────────

describe('composerPlaceholder', () => {
  it('uses the exact mockup copy by default', () => {
    expect(composerPlaceholder()).toBe('Message... use @mention or #topic');
    expect(DEFAULT_PLACEHOLDER).toBe('Message... use @mention or #topic');
  });

  it('returns the default for an empty state object', () => {
    expect(composerPlaceholder({})).toBe(DEFAULT_PLACEHOLDER);
  });

  it('switches to reply copy when a reply target is armed', () => {
    expect(composerPlaceholder({ isReply: true })).toBe(REPLY_PLACEHOLDER);
  });

  it('mentions view-only mode for viewers', () => {
    expect(composerPlaceholder({ readOnly: true })).toBe(
      VIEW_ONLY_PLACEHOLDER,
    );
  });

  it('prefers view-only over reply — a viewer cannot reply either', () => {
    expect(composerPlaceholder({ readOnly: true, isReply: true })).toBe(
      VIEW_ONLY_PLACEHOLDER,
    );
  });

  it('keeps the @mention and #topic affordances discoverable', () => {
    expect(DEFAULT_PLACEHOLDER).toContain('@mention');
    expect(DEFAULT_PLACEHOLDER).toContain('#topic');
    expect(REPLY_PLACEHOLDER).toContain('@mention');
    expect(REPLY_PLACEHOLDER).toContain('#topic');
  });
});

// ─── Wire payload ──────────────────────────────────────────────────────

describe('buildCreateNodeBody', () => {
  it('emits snake_case keys only — the Go handler parses no others', () => {
    const body = buildCreateNodeBody({ content: 'hello' });
    expect(Object.keys(body).sort()).toEqual([
      'content',
      'content_format',
      'node_type',
      'parent_id',
    ]);
  });

  it('defaults to a markdown message node at the root', () => {
    expect(buildCreateNodeBody({ content: 'hello' })).toEqual({
      parent_id: null,
      content: 'hello',
      content_format: 'markdown',
      node_type: 'message',
    });
  });

  it('carries the reply target through as parent_id', () => {
    const body = buildCreateNodeBody({
      content: 'a reply',
      parentId: PARENT,
    });
    expect(body.parent_id).toBe(PARENT);
  });

  it('sends null parent_id for a root message', () => {
    expect(buildCreateNodeBody({ content: 'root' }).parent_id).toBeNull();
    expect(
      buildCreateNodeBody({ content: 'root', parentId: null }).parent_id,
    ).toBeNull();
    expect(
      buildCreateNodeBody({ content: 'root', parentId: undefined }).parent_id,
    ).toBeNull();
  });

  it('treats a blank parentId as no parent rather than an invalid UUID', () => {
    expect(
      buildCreateNodeBody({ content: 'root', parentId: '   ' }).parent_id,
    ).toBeNull();
  });

  it('trims the content before sending', () => {
    expect(buildCreateNodeBody({ content: '  spaced  ' }).content).toBe(
      'spaced',
    );
  });

  it('preserves interior newlines and markdown', () => {
    const content = '# Title\n\n- one\n- two';
    expect(buildCreateNodeBody({ content }).content).toBe(content);
  });

  it('honours explicit format and node type overrides', () => {
    const body = buildCreateNodeBody({
      content: 'x',
      contentFormat: 'plain',
      nodeType: 'synthesis',
    });
    expect(body.content_format).toBe('plain');
    expect(body.node_type).toBe('synthesis');
  });

  it('omits metadata when there is none', () => {
    expect(buildCreateNodeBody({ content: 'x' })).not.toHaveProperty(
      'metadata',
    );
    expect(
      buildCreateNodeBody({ content: 'x', metadata: {} }),
    ).not.toHaveProperty('metadata');
    expect(
      buildCreateNodeBody({ content: 'x', metadata: null }),
    ).not.toHaveProperty('metadata');
  });

  it('includes metadata when the message carries context', () => {
    const body = buildCreateNodeBody({
      content: 'x',
      metadata: { pinnedNodeIds: ['n1'] },
    });
    expect(body.metadata).toEqual({ pinnedNodeIds: ['n1'] });
  });

  it('rejects empty and whitespace-only content', () => {
    expect(() => buildCreateNodeBody({ content: '' })).toThrow(/empty/i);
    expect(() => buildCreateNodeBody({ content: '   \n\t ' })).toThrow(
      /empty/i,
    );
  });

  it('rejects content past the backend 64KB ceiling', () => {
    expect(() =>
      buildCreateNodeBody({ content: 'a'.repeat(MAX_CONTENT_BYTES + 1) }),
    ).toThrow(/too long/i);
  });

  it('accepts content exactly at the ceiling', () => {
    const body = buildCreateNodeBody({
      content: 'a'.repeat(MAX_CONTENT_BYTES),
    });
    expect(body.content).toHaveLength(MAX_CONTENT_BYTES);
  });

  it('measures the ceiling in bytes, not code units', () => {
    // 'é' is two UTF-8 bytes, so half the ceiling in characters overflows.
    expect(() =>
      buildCreateNodeBody({ content: 'é'.repeat(MAX_CONTENT_BYTES / 2 + 1) }),
    ).toThrow(/too long/i);
  });

  it('serialises to JSON the handler can decode', () => {
    const json = JSON.parse(
      JSON.stringify(buildCreateNodeBody({ content: 'hi', parentId: PARENT })),
    );
    expect(json).toEqual({
      parent_id: PARENT,
      content: 'hi',
      content_format: 'markdown',
      node_type: 'message',
    });
  });
});

describe('byteLength', () => {
  it('counts ASCII as one byte each', () => {
    expect(byteLength('hello')).toBe(5);
  });

  it('counts multi-byte characters by their UTF-8 width', () => {
    expect(byteLength('é')).toBe(2);
    expect(byteLength('🌳')).toBe(4);
  });

  it('is zero for an empty string', () => {
    expect(byteLength('')).toBe(0);
  });
});

// ─── Send metadata ─────────────────────────────────────────────────────

describe('buildSendMetadata', () => {
  it('returns null when nothing is attached or pinned', () => {
    expect(buildSendMetadata({})).toBeNull();
    expect(buildSendMetadata({ files: [], pinnedNodeIds: [] })).toBeNull();
  });

  it('describes attachments without their bytes — no upload API exists', () => {
    const meta = buildSendMetadata({
      files: [{ name: 'a.png', size: 12, type: 'image/png' }],
    });
    expect(meta).toEqual({
      attachments: [{ name: 'a.png', size: 12, type: 'image/png' }],
    });
  });

  it('records pinned context node ids', () => {
    expect(buildSendMetadata({ pinnedNodeIds: ['n1', 'n2'] })).toEqual({
      pinnedNodeIds: ['n1', 'n2'],
    });
  });

  it('combines attachments and pinned context', () => {
    const meta = buildSendMetadata({
      files: [{ name: 'a.txt', size: 1, type: 'text/plain' }],
      pinnedNodeIds: ['n1'],
    });
    expect(Object.keys(meta ?? {}).sort()).toEqual([
      'attachments',
      'pinnedNodeIds',
    ]);
  });

  it('copies the pinned list rather than aliasing the caller array', () => {
    const ids = ['n1'];
    const meta = buildSendMetadata({ pinnedNodeIds: ids });
    ids.push('n2');
    expect(meta?.pinnedNodeIds).toEqual(['n1']);
  });
});

// ─── Cursor insertion ──────────────────────────────────────────────────

describe('insertAtCursor', () => {
  it('inserts at an empty input with no leading space', () => {
    expect(insertAtCursor('', 0, 0, '@')).toEqual({ text: '@', cursor: 1 });
  });

  it('separates a trigger from a preceding word', () => {
    expect(insertAtCursor('hi', 2, 2, '@')).toEqual({
      text: 'hi @',
      cursor: 4,
    });
  });

  it('does not double a space that is already there', () => {
    expect(insertAtCursor('hi ', 3, 3, '@')).toEqual({
      text: 'hi @',
      cursor: 4,
    });
  });

  it('treats a newline as sufficient separation', () => {
    expect(insertAtCursor('hi\n', 3, 3, '#')).toEqual({
      text: 'hi\n#',
      cursor: 3 + 1,
    });
  });

  it('inserts mid-string and keeps the tail', () => {
    expect(insertAtCursor('ab cd', 3, 3, '#')).toEqual({
      text: 'ab #cd',
      cursor: 4,
    });
  });

  it('replaces the current selection', () => {
    expect(insertAtCursor('hello world', 6, 11, '@')).toEqual({
      text: 'hello @',
      cursor: 7,
    });
  });

  it('handles a multi-character insert such as an emoji', () => {
    const out = insertAtCursor('nice', 4, 4, '🌳');
    expect(out.text).toBe('nice 🌳');
    expect(out.cursor).toBe(out.text.length);
  });

  it('clamps a cursor past the end of the text', () => {
    expect(insertAtCursor('ab', 99, 99, '@')).toEqual({
      text: 'ab @',
      cursor: 4,
    });
  });

  it('clamps a negative cursor to the start', () => {
    expect(insertAtCursor('ab', -5, -5, '@')).toEqual({
      text: '@ab',
      cursor: 1,
    });
  });

  it('tolerates a reversed selection range', () => {
    expect(insertAtCursor('abcd', 3, 1, '@')).toEqual({
      text: 'abc @d',
      cursor: 5,
    });
  });

  it('falls back to the start when the cursor is NaN', () => {
    expect(insertAtCursor('ab', Number.NaN, Number.NaN, '@')).toEqual({
      text: '@ab',
      cursor: 1,
    });
  });
});

// ─── Error description ─────────────────────────────────────────────────

describe('describeSendError', () => {
  it('uses the server message the API helper unwrapped', () => {
    expect(describeSendError(new Error('content must not be empty'))).toBe(
      'content must not be empty',
    );
  });

  it('accepts a bare string rejection', () => {
    expect(describeSendError('boom')).toBe('boom');
  });

  it('never renders [object Object] for a structured throw', () => {
    const msg = describeSendError({ code: 'NOPE' });
    expect(msg).toBe(GENERIC_SEND_ERROR);
    expect(msg).not.toContain('[object Object]');
  });

  it('falls back for empty or missing messages', () => {
    expect(describeSendError(new Error('   '))).toBe(GENERIC_SEND_ERROR);
    expect(describeSendError(undefined)).toBe(GENERIC_SEND_ERROR);
    expect(describeSendError(null)).toBe(GENERIC_SEND_ERROR);
  });
});
