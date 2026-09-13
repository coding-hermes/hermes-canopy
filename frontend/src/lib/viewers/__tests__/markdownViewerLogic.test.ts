/**
 * Unit tests — markdown viewer pure logic (SPEC-PL-02 §9.5, phase 5).
 * Pins: GFM-subset rendering (headings, emphasis, fenced code with language
 * label, GFM tables with alignment, task lists, links, autolinks,
 * blockquotes, thematic breaks, paragraphs, inline code protection), the
 * sanitizer (script/iframe/object/style content stripping, input gate,
 * on*-attribute stripping, javascript:/data:text/html URL neutralization),
 * and the deterministic source-based counters.
 */

import { describe, expect, it } from 'vitest';

import {
  clampFontSize,
  countBlocks,
  countWords,
  escapeHtml,
  isInternalHref,
  normalizeLinkTarget,
  renderMarkdown,
  sanitizeHtml,
} from '../markdownViewerLogic';

describe('renderMarkdown — headings', () => {
  it('renders all six ATX heading levels', () => {
    const src = ['# One', '## Two', '### Three', '#### Four', '##### Five', '###### Six'].join('\n');
    const html = renderMarkdown(src);
    expect(html).toContain('<h1>One</h1>');
    expect(html).toContain('<h2>Two</h2>');
    expect(html).toContain('<h3>Three</h3>');
    expect(html).toContain('<h4>Four</h4>');
    expect(html).toContain('<h5>Five</h5>');
    expect(html).toContain('<h6>Six</h6>');
  });

  it('supports inline emphasis and code inside headings', () => {
    expect(renderMarkdown('## Title with **bold**')).toContain('<h2>Title with <strong>bold</strong></h2>');
    expect(renderMarkdown('## Title `code`')).toContain('<h2>Title <code>code</code></h2>');
  });

  it('escapes HTML-significant text in headings', () => {
    expect(renderMarkdown('# <script>alert(1)</script>')).not.toContain('<script>');
    expect(renderMarkdown('# <script>alert(1)</script>')).toContain('&lt;script&gt;');
  });

  it('leaves 7+ hashes as paragraph text', () => {
    expect(renderMarkdown('####### seven')).not.toContain('<h7');
  });
});

describe('renderMarkdown — inline styles', () => {
  it('renders bold in both spellings', () => {
    expect(renderMarkdown('this is **bold** text')).toContain('<strong>bold</strong>');
    expect(renderMarkdown('this is __bold__ text')).toContain('<strong>bold</strong>');
  });

  it('renders italic in both spellings', () => {
    expect(renderMarkdown('this is *italic* text')).toContain('<em>italic</em>');
    expect(renderMarkdown('this is _italic_ text')).toContain('<em>italic</em>');
  });
});

describe('renderMarkdown — fenced code blocks', () => {
  it('renders a fenced block with the language label preserved', () => {
    const html = renderMarkdown('```ts\nconst x = 1;\n```');
    expect(html).toContain('data-lang="ts"');
    expect(html).toContain('<span class="canopy-md-code-lang">ts</span>');
    expect(html).toContain('<pre><code>const x = 1;</code></pre>');
  });

  it('renders bare fences without a language attribute', () => {
    const html = renderMarkdown('```\nplain\n```');
    expect(html).toContain('data-lang=""');
    expect(html).toContain('<pre><code>plain</code></pre>');
  });

  it('escapes code content and never executes fence-provided markup', () => {
    const html = renderMarkdown('```html\n<script>alert(1)</script>\n```');
    expect(html).toContain('&lt;script&gt;alert(1)&lt;/script&gt;');
    expect(html).not.toMatch(/<script/);
  });

  it('treats later bare fences as new blocks, not closers of an info-fence', () => {
    const html = renderMarkdown('```\ncode\n```\n```\nmore\n```');
    expect((html.match(/<pre><code>/g) || []).length).toBe(2);
  });
});

describe('renderMarkdown — tables', () => {
  it('renders a 3-column GFM table with header and body rows', () => {
    const src = [
      '| Name | Qty | Note |',
      '| --- | --- | --- |',
      '| wrench | 2 | big |',
      '| bolt | 12 | small |',
    ].join('\n');
    const html = renderMarkdown(src);
    expect(html).toContain('<table class="canopy-md-table">');
    expect(html).toContain('<th>Name</th><th>Qty</th><th>Note</th>');
    expect(html).toContain('<td>wrench</td><td>2</td><td>big</td>');
    expect(html).toContain('<td>bolt</td><td>12</td><td>small</td>');
  });

  it('tolerates alignment syntax and edge pipes', () => {
    const src = '| L | C | R |\n| :-- | :-: | --: |\n| a | b | c |';
    const html = renderMarkdown(src);
    expect(html).toContain('style="text-align:left;"');
    expect(html).toContain('style="text-align:center;"');
    expect(html).toContain('style="text-align:right;"');
    expect(html).toContain(
      '<td style="text-align:left;">a</td><td style="text-align:center;">b</td><td style="text-align:right;">c</td>',
    );
  });

  it('inlines markdown and escapes HTML inside cells', () => {
    const src = '| K | V |\n| --- | --- |\n| **b** | <img src=x> |';
    const html = renderMarkdown(src);
    expect(html).toContain('<td><strong>b</strong></td>');
    expect(html).toContain('&lt;img src=x&gt;');
  });
});

describe('renderMarkdown — task lists', () => {
  it('renders checked and unchecked task items as disabled checkboxes', () => {
    const src = '- [ ] todo one\n- [x] done one';
    const html = renderMarkdown(src);
    expect(html).toContain('<li class="canopy-md-task"><input type="checkbox" disabled />');
    expect(html).toContain('<input type="checkbox" disabled checked />');
    expect(html).toContain('<span>todo one</span>');
    expect(html).toContain('<span>done one</span>');
  });

  it('accepts uppercase X and plain items mixed in one list', () => {
    const html = renderMarkdown('- [X] upper\n- plain item');
    expect(html).toContain('disabled checked');
    expect(html).toContain('<li>plain item</li>');
  });

  it('renders plain list items when task rendering is disabled', () => {
    const html = renderMarkdown('- [x] done', { renderTaskLists: false });
    expect(html).not.toContain('checkbox');
    expect(html).toContain('<li>[x] done</li>');
  });

  it('renders ordered lists', () => {
    const html = renderMarkdown('1. first\n2. second');
    expect(html).toContain('<ol class="canopy-md-list"><li>first</li><li>second</li></ol>');
  });
});

describe('renderMarkdown — links', () => {
  it('renders links with target and rel attributes', () => {
    const html = renderMarkdown('see [the docs](https://example.com/docs) now');
    expect(html).toContain(
      '<a href="https://example.com/docs" target="_blank" rel="noopener noreferrer">the docs</a>',
    );
  });

  it('honors linkTarget _self from options', () => {
    const html = renderMarkdown('[in](/page)', { linkTarget: '_self' });
    expect(html).toContain('target="_self"');
  });

  it('strips javascript: hrefs by dropping the attribute entirely', () => {
    const html = renderMarkdown('[click](javascript:alert(1))');
    expect(html).not.toContain('javascript:');
    expect(html).toContain('<a target="_blank"');
  });

  it('neutralizes data:text/html and tab-obfuscated schemes', () => {
    expect(renderMarkdown('[x](data:text/html;base64,PHNjcmlwdD4=)').toLowerCase()).not.toContain('data:text/html');
    // A tab inside the destination means NO link forms at all (GFM forbids
    // whitespace there), so the text falls through — no anchor, no href.
    const tabbed = renderMarkdown('[x](java\tscript:alert(1))');
    expect(tabbed).not.toContain('<a');
    expect(tabbed).not.toContain('href');
  });

  it('autolinks angle-bracket URLs', () => {
    expect(renderMarkdown('go to <https://example.com/x> now')).toContain(
      '<a href="https://example.com/x" target="_blank" rel="noopener noreferrer">https://example.com/x</a>',
    );
  });
});

describe('renderMarkdown — blockquotes, rules, paragraphs', () => {
  it('renders blockquotes with recursive inner rendering', () => {
    const html = renderMarkdown('> quoted **strong**\n> second line');
    expect(html).toContain('<blockquote>');
    expect(html).toContain('<strong>strong</strong>');
    expect(html).toContain('second line');
  });

  it('renders thematic breaks from -, *, _', () => {
    expect(renderMarkdown('---')).toContain('<hr />');
    expect(renderMarkdown('***')).toContain('<hr />');
    expect(renderMarkdown('___')).toContain('<hr />');
  });

  it('joins soft-wrapped paragraph lines and escapes their text', () => {
    expect(renderMarkdown('line one\nline two')).toContain('<p>line one line two</p>');
    expect(renderMarkdown('has <b>bold</b> text')).toContain('&lt;b&gt;bold&lt;/b&gt;');
  });
});

describe('renderMarkdown — inline code protection', () => {
  it('keeps markup-looking text literal inside inline code', () => {
    const html = renderMarkdown('use `<b>not bold</b>` here');
    expect(html).toContain('<code>&lt;b&gt;not bold&lt;/b&gt;</code>');
    expect(html).not.toContain('<b>');
  });

  it('does not treat stars inside code as emphasis', () => {
    expect(renderMarkdown('`a * b * c`')).toBe('<p><code>a * b * c</code></p>');
  });
});

describe('sanitizeHtml', () => {
  it('strips a script element and its content', () => {
    expect(sanitizeHtml('<p>ok</p><script>alert(1)</script>')).toBe('<p>ok</p>');
  });

  it('strips script loose in prose and case-insensitively', () => {
    expect(sanitizeHtml('hello <SCRIPT>alert(1)</SCRIPT> world')).toBe('hello  world');
  });

  it('strips an unclosed/paired iframe, object, embed, style, and form', () => {
    const out = sanitizeHtml('<iframe src="https://evil.test"></iframe><p>keep</p>');
    expect(out).toBe('<p>keep</p>');
    expect(sanitizeHtml('<object data="x"></object>y')).toBe('y');
    expect(sanitizeHtml('<embed src="x">z')).toBe('z');
    expect(sanitizeHtml('<style>body{}</style>q')).toBe('q');
    expect(sanitizeHtml('<form action="/steal"><input type=text></form>')).toBe('');
  });

  it('strips standalone meta, link, and base tags', () => {
    expect(sanitizeHtml('a<meta charset="x">b<link rel="x" href="y">c<base href="/">d')).toBe('abcd');
  });

  it('strips every on* event-handler attribute', () => {
    const out = sanitizeHtml('<img src="ok.png" onerror="alert(1)" alt="x">');
    expect(out).not.toContain('onerror');
    expect(out).toContain('src="ok.png"');
    expect(out).toContain('alt="x"');
    const out2 = sanitizeHtml('<a href="/x" ONCLICK="steal()">y</a>');
    expect(out2).not.toMatch(/onclick/i);
    expect(out2).toContain('href="/x"');
  });

  it('strips javascript: href/src values', () => {
    expect(sanitizeHtml('<a href="javascript:alert(1)">x</a>')).toBe('<a>x</a>');
    expect(sanitizeHtml('<a href="JaVaScRiPt:alert(1)">x</a>')).toBe('<a>x</a>');
    expect(sanitizeHtml('<img src="javascript:alert(1)">')).toBe('<img>');
  });

  it('strips data:text/html values but keeps harmless data: images', () => {
    expect(sanitizeHtml('<a href="data:text/html,<h1>hi</h1>">x</a>')).toBe('<a>x</a>');
    expect(sanitizeHtml('<img src="data:image/png;base64,AAAA">')).toContain('data:image/png');
  });

  it('strips non-disabled-checkbox inputs entirely', () => {
    expect(sanitizeHtml('<input type="text">')).toBe('');
    expect(sanitizeHtml('<input type=text>')).toBe('');
    expect(sanitizeHtml('<input type="checkbox">')).toBe('');
    expect(sanitizeHtml('<input type="checkbox" disabled>')).toBe('<input type="checkbox" disabled>');
  });

  it('keeps safe markup intact', () => {
    const safe = '<h2>T</h2><p>a <strong>b</strong></p><input type="checkbox" disabled checked>';
    expect(sanitizeHtml(safe)).toBe(safe);
  });
});

describe('countWords / countBlocks', () => {
  it('counts source words, excluding fenced code', () => {
    expect(countWords('alpha beta gamma')).toBe(3);
    expect(countWords('```\njunk junk junk junk\n```')).toBe(0);
    expect(countWords('alpha\n```\njunk junk\n```\nbeta gamma')).toBe(3);
  });

  it('deterministic on repeated runs of the same fixture', () => {
    const fixture = '# H\n\nsome words here\n\n- item one\n- item two';
    expect(countWords(fixture)).toBe(countWords(fixture));
    expect(countBlocks(fixture)).toBe(countBlocks(fixture));
  });

  it('counts paragraphs from source (heading/fence/quote/list/HR excluded)', () => {
    const src = [
      '# Heading', // not a paragraph
      '',
      'First paragraph,',
      'soft-wrapped.',
      '',
      '```',
      'not counted',
      '```',
      '',
      '> quote not counted',
      '',
      '- list item',
      '',
      '---',
      '',
      'Second paragraph',
    ].join('\n');
    expect(countBlocks(src)).toBe(2);
  });

  it('returns zero on empty and whitespace-only sources', () => {
    expect(countWords('')).toBe(0);
    expect(countBlocks('')).toBe(0);
    expect(countWords('  \n\t ')).toBe(0);
    expect(countBlocks('  \n\t ')).toBe(0);
  });
});

describe('config and click helpers', () => {
  it('clampFontSize enforces 12–20 with a NaN-safe fallback', () => {
    expect(clampFontSize(14, 14)).toBe(14);
    expect(clampFontSize(5, 14)).toBe(12);
    expect(clampFontSize(99, 14)).toBe(20);
    expect(clampFontSize(undefined, 14)).toBe(14);
    expect(clampFontSize(Number.NaN, 14)).toBe(14);
  });

  it('normalizeLinkTarget accepts only the two spec values', () => {
    expect(normalizeLinkTarget('_self', '_blank')).toBe('_self');
    expect(normalizeLinkTarget('_top', '_blank')).toBe('_blank');
    expect(normalizeLinkTarget(undefined, '_blank')).toBe('_blank');
  });

  it('isInternalHref matches fragments and root-relative paths', () => {
    expect(isInternalHref('#anchor')).toBe(true);
    expect(isInternalHref('/tree/1')).toBe(true);
    expect(isInternalHref('https://elsewhere.example')).toBe(false);
    expect(isInternalHref('mailto:x@y.z')).toBe(false);
  });

  it('escapeHtml escapes all five significant characters', () => {
    expect(escapeHtml('<a href="x">\'&\'</a>')).toBe('&lt;a href=&quot;x&quot;&gt;&#39;&amp;&#39;&lt;/a&gt;');
  });
});
