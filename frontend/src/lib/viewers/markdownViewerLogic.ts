/**
 * Hermes Canopy — markdown viewer pure logic (SPEC-PL-02 §9.5, phase 5;
 * zero-dependency GFM subset, same precedent as the §9.6 JSON phase).
 *
 * Unit-testable helpers consumed by markdownViewerBody.ts (the in-iframe
 * shell). Everything that can be tested by vitest without a DOM lives here:
 *   - renderMarkdown: GFM-subset source → HTML string (ATX headings, bold,
 *     italic, strikethrough, inline code, fenced code blocks with language
 *     label, ordered/unordered lists, task-list checkboxes, GFM tables with
 *     alignment, links, angle-bracket autolinks, blockquotes, thematic
 *     breaks, paragraphs).
 *   - sanitizeHtml: conservative denylist sanitizer standing in for the
 *     spec's DOMPurify pass until a host-side bundle exists (the sandboxed
 *     srcDoc body cannot import npm packages — no bundler runs inside the
 *     frame). Strips script/iframe/object/embed/style/form elements and
 *     their content, meta/link/base tags, every non-disabled-checkbox
 *     <input>, all on* event-handler attributes, and javascript:/vbscript:/
 *     data:text/html URL values.
 *   - countWords / countBlocks: deterministic SOURCE-based counters for the
 *     §9.5 markdown_rendered payload (wordCount, paragraphCount).
 *   - clampFontSize / normalizeLinkTarget / isInternalHref: config and
 *     delegated-click helpers.
 *
 * Serialization contract: every exported function is FULLY self-contained
 * (no free calls to sibling exports) so the helper bundle that
 * markdownViewerBody.ts assembles via Function.prototype.toString runs each
 * function verbatim inside the iframe — the exact source the unit tests
 * exercise. Free module-scope identifiers would not survive serialization.
 *
 * DEFERRED from spec §9.5 (host-library features; no bundler inside the
 * sandboxed srcDoc): KaTeX math, mermaid diagrams, syntax highlighting
 * (rehype-highlight), footnotes (remark-gfm), setext headings, nested
 * lists, reference-style links, link titles, and raw inline HTML
 * pass-through — raw HTML in the markdown source is rendered as ESCAPED
 * TEXT, never executed.
 */

// ── Escaping ─────────────────────────────────────────────────────

/**
 * Escapes the five HTML-significant characters. Used for every piece of
 * user text before it enters the output HTML (raw HTML in the source never
 * becomes markup).
 */
export function escapeHtml(text: string): string {
  return String(text)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

// ── Render options ───────────────────────────────────────────────

export interface MarkdownRenderOptions {
  /** Render `- [ ]` / `- [x]` items as disabled checkboxes (default true). */
  renderTaskLists?: boolean;
  /** target attribute for generated links (default '_blank'). */
  linkTarget?: '_blank' | '_self';
}

// ── GFM-subset renderer ──────────────────────────────────────────

/**
 * Renders a zero-dependency GFM subset of the markdown source to an HTML
 * string. All user text is HTML-escaped; the only markup in the output is
 * the markup this function itself generates, so the result is safe by
 * construction (sanitizeHtml still runs over it as defense in depth).
 * Dangerous link schemes (javascript:/vbscript:/data:text/html) are
 * neutralized by dropping the href attribute entirely.
 */
export function renderMarkdown(source: string, options?: MarkdownRenderOptions): string {
  var renderTaskLists = !options || options.renderTaskLists !== false;
  var linkTarget = options && options.linkTarget === '_self' ? '_self' : '_blank';

  var FENCE_RE = /^(\s{0,3})(```+|~~~+)(\s*)$/;
  var FENCE_INFO_RE = /^(\s{0,3})(```+|~~~+)\s*(\S+)\s*$/;
  var HEADING_RE = /^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/;
  var HR_RE = /^\s{0,3}((?:-[ \t]*){3,}|(?:\*[ \t]*){3,}|(?:_[ \t]*){3,})$/;
  var QUOTE_RE = /^\s{0,3}>/;
  var UL_ITEM_RE = /^\s{0,3}[-*+]\s+/;
  var OL_ITEM_RE = /^\s{0,3}\d+[.)]\s+/;
  var DIVIDER_RE = /^\s{0,3}\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/;
  var TASK_RE = /^\[([ xX])\]\s+(.*)$/;

  function esc(text: string): string {
    return String(text)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  /**
   * Returns the href attribute value for a link, or '' when the scheme is
   * dangerous. Input is ALREADY escaped text (called on escaped source), so
   * it is returned as-is — no second escape pass.
   */
  function hrefAttribute(escapedHref: string): string {
    var stripped = String(escapedHref).replace(/[\s\u0000-\u001f]+/g, '');
    if (/^(javascript|vbscript):/i.test(stripped)) return '';
    if (/^data:text\/html/i.test(stripped)) return '';
    return escapedHref;
  }

  /** Bold / italic / strikethrough over one escaped, tag-free segment. */
  function emphasis(segment: string): string {
    var s = segment;
    s = s.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
    s = s.replace(/__([^_]+)__/g, '<strong>$1</strong>');
    s = s.replace(/(^|[^*\w])\*([^*\n]+)\*(?!\*)/g, '$1<em>$2</em>');
    s = s.replace(/(^|[^_\w])_([^_\n]+)_(?!_)/g, '$1<em>$2</em>');
    s = s.replace(/~~([^~]+)~~/g, '<del>$1</del>');
    return s;
  }

  /**
   * Inline rendering: escapes everything, protects inline code spans,
   * autolinks angle-bracket URLs, renders [label](href) links, applies
   * emphasis outside generated tags, restores code spans last.
   */
  function inline(raw: string): string {
    var codeSpans: string[] = [];
    var text = String(raw).replace(/`([^`]+)`/g, function (_match: string, code: string) {
      codeSpans.push(code);
      return '\u0000C' + (codeSpans.length - 1) + '\u0000';
    });
    // Raw inline HTML renders as escaped text — never as markup.
    text = esc(text);
    // Autolinks: <https://…>
    text = text.replace(/&lt;(https?:\/\/(?:[^&\s]|&amp;)+)&gt;/g, function (_match: string, url: string) {
      return '<a href="' + url + '" target="' + linkTarget + '" rel="noopener noreferrer">' + url + '</a>';
    });
    // Links: [label](href)
    text = text.replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, function (_match: string, label: string, href: string) {
      var hrefAttr = hrefAttribute(href);
      var anchor =
        '<a' + (hrefAttr ? ' href="' + hrefAttr + '"' : '') + ' target="' + linkTarget + '" rel="noopener noreferrer">';
      return anchor + emphasis(label) + '</a>';
    });
    // Emphasis outside already-generated tags (odd split segments are tags).
    text = text
      .split(/(<[^>]+>)/g)
      .map(function (segment: string, index: number) {
        return index % 2 === 1 ? segment : emphasis(segment);
      })
      .join('');
    // Restore inline code spans (their content stays escaped, unprocessed).
    text = text.replace(/\u0000C(\d+)\u0000/g, function (_match: string, index: string) {
      return '<code>' + esc(codeSpans[Number(index)]) + '</code>';
    });
    return text;
  }

  /** Splits a table row into trimmed cells (edge pipes optional). */
  function splitRow(row: string): string[] {
    var cells = String(row)
      .trim()
      .replace(/^\|/, '')
      .replace(/\|$/, '')
      .split('|');
    return cells.map(function (cell: string) {
      return cell.trim();
    });
  }

  /** GFM column alignment from a delimiter cell (':--' | ':--:' | '--:'). */
  function cellAlignment(cell: string): string {
    var c = cell.trim();
    if (c.charAt(0) === ':' && c.charAt(c.length - 1) === ':') return 'center';
    if (c.charAt(0) === ':') return 'left';
    if (c.charAt(c.length - 1) === ':') return 'right';
    return '';
  }

  var lines = String(source).replace(/\r\n?/g, '\n').split('\n');
  var htmlParts: string[] = [];
  var i = 0;

  while (i < lines.length) {
    var line = lines[i];
    if (line.trim() === '') {
      i += 1;
      continue;
    }

    // Fenced code block (``` or ~~~, with optional info-string language).
    var fenceInfo = FENCE_INFO_RE.exec(line);
    var fenceBare = FENCE_RE.exec(line);
    if (fenceInfo || fenceBare) {
      var marker = fenceInfo ? fenceInfo[2] : (fenceBare as RegExpExecArray)[2];
      var fenceChar = marker.charAt(0);
      var fenceLen = marker.length;
      var lang = fenceInfo ? fenceInfo[3] : '';
      var codeLines: string[] = [];
      i += 1;
      while (i < lines.length) {
        var closeInfo = FENCE_INFO_RE.exec(lines[i]);
        var closeBare = FENCE_RE.exec(lines[i]);
        var closeMarker = closeInfo ? closeInfo[2] : closeBare ? closeBare[2] : '';
        if (
          closeMarker !== '' &&
          closeMarker.charAt(0) === fenceChar &&
          closeMarker.length >= fenceLen &&
          (closeInfo === null || lang === '') // info strings only on the opener
        ) {
          i += 1;
          break;
        }
        codeLines.push(lines[i]);
        i += 1;
      }
      htmlParts.push(
        '<div class="canopy-md-code" data-lang="' +
          esc(lang) +
          '"><span class="canopy-md-code-lang">' +
          esc(lang) +
          '</span><pre><code>' +
          esc(codeLines.join('\n')) +
          '</code></pre></div>',
      );
      continue;
    }

    // ATX heading.
    var heading = HEADING_RE.exec(line);
    if (heading) {
      var level = heading[1].length;
      htmlParts.push('<h' + level + '>' + inline(heading[2]) + '</h' + level + '>');
      i += 1;
      continue;
    }

    // Thematic break.
    if (HR_RE.test(line)) {
      htmlParts.push('<hr />');
      i += 1;
      continue;
    }

    // Blockquote (one level stripped, inner blocks re-rendered).
    if (QUOTE_RE.test(line)) {
      var quoteLines: string[] = [];
      while (i < lines.length && QUOTE_RE.test(lines[i])) {
        quoteLines.push(lines[i].replace(/^\s{0,3}>\s?/, ''));
        i += 1;
      }
      htmlParts.push('<blockquote>' + renderMarkdown(quoteLines.join('\n'), options) + '</blockquote>');
      continue;
    }

    // GFM table: header row + delimiter row (+ body rows).
    if (line.indexOf('|') !== -1 && i + 1 < lines.length && DIVIDER_RE.test(lines[i + 1])) {
      var headerCells = splitRow(line);
      var aligns = splitRow(lines[i + 1]).map(cellAlignment);
      i += 2;
      var bodyRows: string[][] = [];
      while (i < lines.length && lines[i].trim() !== '' && lines[i].indexOf('|') !== -1) {
        bodyRows.push(splitRow(lines[i]));
        i += 1;
      }
      var tableHtml = '<table class="canopy-md-table"><thead><tr>';
      for (var c = 0; c < headerCells.length; c += 1) {
        var thAlign = aligns[c] ? ' style="text-align:' + aligns[c] + ';"' : '';
        tableHtml += '<th' + thAlign + '>' + inline(headerCells[c]) + '</th>';
      }
      tableHtml += '</tr></thead>';
      if (bodyRows.length > 0) {
        tableHtml += '<tbody>';
        for (var r = 0; r < bodyRows.length; r += 1) {
          tableHtml += '<tr>';
          for (var d = 0; d < headerCells.length; d += 1) {
            var tdAlign = aligns[d] ? ' style="text-align:' + aligns[d] + ';"' : '';
            tableHtml += '<td' + tdAlign + '>' + inline(bodyRows[r][d] || '') + '</td>';
          }
          tableHtml += '</tr>';
        }
        tableHtml += '</tbody>';
      }
      tableHtml += '</table>';
      htmlParts.push(tableHtml);
      continue;
    }

    // Lists (unordered / ordered; flat subset, indented lazy continuation).
    var isUl = UL_ITEM_RE.test(line);
    var isOl = !isUl && OL_ITEM_RE.test(line);
    if (isUl || isOl) {
      var listTag = isUl ? 'ul' : 'ol';
      var itemRe = isUl ? UL_ITEM_RE : OL_ITEM_RE;
      var items: string[] = [];
      var current: string | null = null;
      while (i < lines.length) {
        var candidate = lines[i];
        if (candidate.trim() === '') {
          // Blank line: the list continues only if the next line is another item.
          if (i + 1 < lines.length && itemRe.test(lines[i + 1])) {
            i += 1;
            continue;
          }
          break;
        }
        if (itemRe.test(candidate)) {
          if (current !== null) items.push(current);
          current = candidate.replace(itemRe, '');
          i += 1;
        } else if (/^\s{2,}\S/.test(candidate) && current !== null) {
          current += ' ' + candidate.trim();
          i += 1;
        } else {
          break;
        }
      }
      if (current !== null) items.push(current);
      var listHtml = '<' + listTag + ' class="canopy-md-list">';
      for (var itemIndex = 0; itemIndex < items.length; itemIndex += 1) {
        var item = items[itemIndex];
        var task = renderTaskLists ? TASK_RE.exec(item) : null;
        if (task) {
          var checked = task[1].toLowerCase() === 'x';
          listHtml +=
            '<li class="canopy-md-task"><input type="checkbox" disabled' +
            (checked ? ' checked' : '') +
            ' /><span>' +
            inline(task[2]) +
            '</span></li>';
        } else {
          listHtml += '<li>' + inline(item) + '</li>';
        }
      }
      listHtml += '</' + listTag + '>';
      htmlParts.push(listHtml);
      continue;
    }

    // Paragraph: gather until a blank line or another block starter.
    var paraLines: string[] = [];
    while (
      i < lines.length &&
      lines[i].trim() !== '' &&
      !FENCE_INFO_RE.test(lines[i]) &&
      !FENCE_RE.test(lines[i]) &&
      !HEADING_RE.test(lines[i]) &&
      !HR_RE.test(lines[i]) &&
      !QUOTE_RE.test(lines[i]) &&
      !UL_ITEM_RE.test(lines[i]) &&
      !OL_ITEM_RE.test(lines[i])
    ) {
      paraLines.push(lines[i].trim());
      i += 1;
    }
    if (paraLines.length === 0) {
      i += 1; // safety net: never loop forever on an unrecognized line
      continue;
    }
    htmlParts.push('<p>' + inline(paraLines.join(' ')) + '</p>');
  }

  return htmlParts.join('\n');
}

// ── Sanitizer (conservative denylist standing in for DOMPurify) ──

/**
 * Strips the dangerous element classes (with their content when paired),
 * every on* event-handler attribute, and javascript:/vbscript:/
 * data:text/html URL values from href/src-class attributes. <input> is
 * allowed ONLY as a disabled checkbox (the task-list markup this viewer
 * itself generates) — every other input class is stripped. This is a
 * conservative denylist subset, NOT a full allowlist sanitizer; it stands
 * in for the spec's DOMPurify pass until a host-side bundle exists.
 */
export function sanitizeHtml(html: string): string {
  var out = String(html);
  // Tag-shape regex shared by the passes below. Attribute values are
  // consumed as quoted spans so a `>` INSIDE a quoted value (e.g.
  // href="data:text/html,<h1>") cannot smuggle the tag past the cleaner;
  // the lazy quantifier ends the tag at the first `>` outside quotes.
  var TAG_RE = /<([a-zA-Z][a-zA-Z0-9-]*)((?:"[^"]*"|'[^']*'|[^>])*?)(\/?)>/g;
  // 1. Dangerous elements WITH their content (paired open/close).
  out = out.replace(
    /<(script|style|iframe|object|embed|form)\b((?:"[^"]*"|'[^']*'|[^>])*?)>([\s\S]*?)<\/\1\s*>/gi,
    '',
  );
  // 2. Any remaining dangerous/pointless tags (unpaired or void).
  out = out.replace(/<\/?(script|style|iframe|object|embed|form|meta|link|base)\b((?:"[^"]*"|'[^']*'|[^>])*?)>/gi, '');
  // 3. Inputs: only disabled checkboxes survive (task-list markup).
  out = out.replace(/<input\b((?:"[^"]*"|'[^']*'|[^>])*?)>/gi, function (tag: string) {
    var isCheckbox = /\btype\s*=\s*("checkbox"|'checkbox'|checkbox\b)/i.test(tag);
    var isDisabled = /\bdisabled(\s|=|>)/i.test(tag);
    return isCheckbox && isDisabled ? tag : '';
  });
  // 4. on* attributes and dangerous URL schemes on every remaining tag.
  out = out.replace(TAG_RE, function (
    _full: string,
    name: string,
    attrs: string,
    selfClose: string,
  ) {
    var cleaned = attrs
      // every event handler attribute (on*)
      .replace(/\s+on[a-zA-Z]+\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+)/gi, '')
      // URL-carrying attributes with a dangerous scheme
      .replace(
        /\s+(?:href|src|xlink:href|action|formaction|background|poster)\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+)/gi,
        function (attr: string) {
          var valueMatch = /\s*=\s*(?:"([\s\S]*)"|'([\s\S]*)'|([^\s>]+))/.exec(attr);
          var raw = String((valueMatch && (valueMatch[1] || valueMatch[2] || valueMatch[3])) || '');
          var stripped = raw.replace(/[\s\u0000-\u001f]+/g, '');
          if (/^(javascript|vbscript):/i.test(stripped)) return '';
          if (/^data:text\/html/i.test(stripped)) return '';
          return attr;
        },
      );
    return '<' + name + cleaned + (selfClose || '') + '>';
  });
  return out;
}

// ── Source-based counters (§9.5 markdown_rendered payload) ───────

/**
 * Counts whitespace-separated words in the SOURCE, with fenced code blocks
 * excluded. Deterministic pure function of the source string.
 */
export function countWords(source: string): number {
  var withoutFences = String(source)
    .replace(/```[\s\S]*?(```|$)/g, ' ')
    .replace(/~~~[\s\S]*?(~~~|$)/g, ' ');
  var tokens = withoutFences.split(/\s+/).filter(function (token: string) {
    return token !== '';
  });
  return tokens.length;
}

/**
 * Counts top-level PARAGRAPHS in the source (the spec §9.5 payload names
 * this paragraphCount). Runs of non-blank lines not claimed by a heading,
 * fence, thematic break, blockquote, list, or table each count as one
 * paragraph. Nested content inside quotes/lists is not counted
 * (top-level only). Deterministic pure function of the source string.
 */
export function countBlocks(source: string): number {
  var lines = String(source).replace(/\r\n?/g, '\n').split('\n');
  var FENCE = /^\s{0,3}(```+|~~~+)/;
  var FENCE_INFO = /^\s{0,3}(```+|~~~+)\s*\S+/;
  var HEADING = /^\s{0,3}#{1,6}\s+/;
  var HR = /^\s{0,3}((?:-[ \t]*){3,}|(?:\*[ \t]*){3,}|(?:_[ \t]*){3,})$/;
  var QUOTE = /^\s{0,3}>/;
  var LIST = /^\s{0,3}(?:[-*+]|\d+[.)])\s+/;
  var DIVIDER = /^\s{0,3}\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/;
  var paragraphs = 0;
  var i = 0;
  while (i < lines.length) {
    var line = lines[i];
    if (line.trim() === '') {
      i += 1;
      continue;
    }
    if (FENCE.test(line)) {
      var openMatch = (FENCE_INFO.test(line) ? FENCE_INFO : FENCE).exec(line);
      var marker = openMatch ? openMatch[1] : '```';
      var fenceChar = marker.charAt(0);
      var fenceLen = marker.length;
      i += 1;
      while (i < lines.length) {
        var closeMatch = /^\s{0,3}(```+|~~~+)\s*$/.exec(lines[i]);
        if (closeMatch && closeMatch[1].charAt(0) === fenceChar && closeMatch[1].length >= fenceLen) {
          i += 1;
          break;
        }
        i += 1;
      }
      continue;
    }
    if (HEADING.test(line) || HR.test(line) || QUOTE.test(line) || LIST.test(line)) {
      i += 1;
      continue;
    }
    if (line.indexOf('|') !== -1 && i + 1 < lines.length && DIVIDER.test(lines[i + 1])) {
      i += 2;
      while (i < lines.length && lines[i].trim() !== '' && lines[i].indexOf('|') !== -1) i += 1;
      continue;
    }
    paragraphs += 1;
    i += 1;
    while (
      i < lines.length &&
      lines[i].trim() !== '' &&
      !FENCE.test(lines[i]) &&
      !HEADING.test(lines[i]) &&
      !HR.test(lines[i]) &&
      !QUOTE.test(lines[i]) &&
      !LIST.test(lines[i])
    ) {
      i += 1;
    }
  }
  return paragraphs;
}

// ── Config / click helpers ───────────────────────────────────────

/** Clamps a configured font size into the spec's 12–20 range; NaN-safe. */
export function clampFontSize(value: unknown, fallback: number): number {
  if (typeof value !== 'number' || !isFinite(value)) return fallback;
  return Math.min(20, Math.max(12, Math.round(value)));
}

/** Accepts only '_blank' / '_self'; anything else falls back. */
export function normalizeLinkTarget(value: unknown, fallback: '_blank' | '_self'): '_blank' | '_self' {
  return value === '_self' || value === '_blank' ? value : fallback;
}

/**
 * True when a rendered link's href is internal ('#' fragment or a
 * root-relative '/…' path) — those get preventDefault() so navigation is a
 * host-side decision in a later phase. Protocol-relative '//host' also
 * matches the '/' rule; preventDefault is the safe direction for it.
 */
export function isInternalHref(href: string): boolean {
  var trimmed = String(href).trim();
  return trimmed.charAt(0) === '#' || trimmed.charAt(0) === '/';
}
