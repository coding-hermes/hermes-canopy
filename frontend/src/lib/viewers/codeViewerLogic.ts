/**
 * Hermes Canopy — dependency-free code viewer logic (SPEC-PL-02 §9.3, phase 7).
 *
 * These helpers are deliberately conservative: detection never executes or
 * parses source, and content sniffing is limited to the first 4 KiB. They are
 * serialized into codeViewerBody.ts, so every helper must remain self-contained
 * and free of module-scope runtime identifiers.
 *
 * Deferred from §9.3: Monaco, syntax highlighting, minimap, semantic editor
 * actions, multi-cursor, replace, comments, formatting, command palette,
 * definition lookup, and host-bundle editor features. This phase is read-only.
 */

export type CodeLanguage =
  | 'plaintext'
  | 'python'
  | 'javascript'
  | 'typescript'
  | 'go'
  | 'rust'
  | 'c'
  | 'cpp'
  | 'java'
  | 'ruby'
  | 'php'
  | 'shell'
  | 'json'
  | 'xml'
  | 'yaml'
  | 'toml'
  | 'html'
  | 'css'
  | 'scss'
  | 'sql'
  | 'markdown'
  | 'kotlin'
  | 'swift'
  | 'dart'
  | 'lua'
  | 'r'
  | 'julia'
  | 'elixir'
  | 'elm';

export type CodeDetectionSource = 'extension' | 'filename' | 'content' | 'default';

export interface CodeLanguageDetection {
  language: CodeLanguage;
  confidence: number;
  source: CodeDetectionSource;
}

export interface CodeViewerConfig {
  showLineNumbers: boolean;
  tabSize: 2 | 4 | 8;
  wordWrap: 'off' | 'on' | 'wordWrapColumn' | 'bounded';
  fontSize: number;
  fontFamily: string;
}

/**
 * Detect a language in the required order: extension, filename pattern, then
 * a small content sample. Unknown input intentionally remains plaintext.
 */
export function detectCodeLanguage(filename: string, content: string): CodeLanguageDetection {
  const name = typeof filename === 'string' ? filename.trim() : '';
  const lowerName = name.toLowerCase();
  const extension = lowerName.match(/\.([a-z0-9]+)$/)?.[1] || '';
  const extensions: Record<string, CodeLanguage> = {
    py: 'python',
    pyw: 'python',
    js: 'javascript',
    jsx: 'javascript',
    mjs: 'javascript',
    cjs: 'javascript',
    ts: 'typescript',
    tsx: 'typescript',
    go: 'go',
    rs: 'rust',
    c: 'c',
    h: 'c',
    cc: 'cpp',
    cpp: 'cpp',
    cxx: 'cpp',
    hpp: 'cpp',
    java: 'java',
    rb: 'ruby',
    rake: 'ruby',
    php: 'php',
    sh: 'shell',
    bash: 'shell',
    zsh: 'shell',
    fish: 'shell',
    json: 'json',
    jsonc: 'json',
    xml: 'xml',
    xsd: 'xml',
    xsl: 'xml',
    xslt: 'xml',
    yaml: 'yaml',
    yml: 'yaml',
    toml: 'toml',
    html: 'html',
    htm: 'html',
    css: 'css',
    scss: 'scss',
    sql: 'sql',
    md: 'markdown',
    markdown: 'markdown',
    mdx: 'markdown',
    kt: 'kotlin',
    kts: 'kotlin',
    swift: 'swift',
    dart: 'dart',
    lua: 'lua',
    r: 'r',
    rmd: 'r',
    jl: 'julia',
    ex: 'elixir',
    exs: 'elixir',
    elm: 'elm',
  };
  if (extension && extensions[extension]) return { language: extensions[extension], confidence: 1, source: 'extension' };

  const filenamePatterns: Array<[RegExp, CodeLanguage]> = [
    [/^(dockerfile)(\..*)?$/, 'shell'],
    [/^(makefile|gnumakefile)(\..*)?$/, 'shell'],
    [/^(rakefile|gemfile|guardfile|podfile)(\..*)?$/, 'ruby'],
    [/^(jenkinsfile|vagrantfile|justfile)$/, 'shell'],
    [/^\.?(bashrc|bash_profile|zshrc|profile)$/, 'shell'],
    [/^(readme|changelog|license)(\..*)?$/, 'markdown'],
  ];
  const baseName = lowerName.slice(lowerName.lastIndexOf('/') + 1);
  for (const [pattern, language] of filenamePatterns) {
    if (pattern.test(baseName)) return { language, confidence: 0.96, source: 'filename' };
  }

  // Do not inspect beyond this boundary: a language marker later in a large
  // file must not change a detection made from the bounded prefix.
  const sample = typeof content === 'string' ? content.slice(0, 4096) : '';
  const trimmed = sample.replace(/^\uFEFF/, '').trim();
  if (/^#!\s*\/.*\bpython(?:[0-9.]*)?\b/.test(trimmed)) return { language: 'python', confidence: 0.92, source: 'content' };
  if (/^#!\s*\/.*\b(?:ba|z|fi|dash|korn)?sh\b|^#!\s*\/usr\/bin\/env\s+(?:ba)?sh\b/.test(trimmed)) {
    return { language: 'shell', confidence: 0.92, source: 'content' };
  }
  if (/^#!\s*\/.*\bruby\b/.test(trimmed)) return { language: 'ruby', confidence: 0.9, source: 'content' };
  if (/^#!\s*\/.*\b(?:node|deno)\b/.test(trimmed)) return { language: 'javascript', confidence: 0.88, source: 'content' };
  if (/^<\?xml\b/i.test(trimmed)) return { language: 'xml', confidence: 0.94, source: 'content' };
  if (/^<(!doctype\s+html|html\b)/i.test(trimmed)) return { language: 'html', confidence: 0.94, source: 'content' };
  if ((trimmed[0] === '{' || trimmed[0] === '[') && /["'][^"']+["']\s*:/.test(trimmed)) {
    return { language: 'json', confidence: 0.82, source: 'content' };
  }
  if (/^(?:---\s*(?:\n|$))|^\s*[A-Za-z_][\w.-]*\s*:\s*(?:[^:=]|$)/m.test(trimmed) && !/;\s*$/.test(trimmed)) {
    return { language: 'yaml', confidence: 0.72, source: 'content' };
  }
  if (/^\s*\[[A-Za-z0-9_.-]+\]\s*(?:\n|$)/m.test(trimmed) && /=/.test(trimmed)) {
    return { language: 'toml', confidence: 0.78, source: 'content' };
  }
  if (/^\s*(?:#{1,6}\s|```|>\s|[-*+]\s+\[[ xX]\])/.test(trimmed)) {
    return { language: 'markdown', confidence: 0.78, source: 'content' };
  }
  if (/^\s*(?:SELECT|INSERT\s+INTO|UPDATE\s+\w+\s+SET|DELETE\s+FROM|CREATE\s+(?:TABLE|VIEW|INDEX)|WITH\s+\w+\s+AS)\b/i.test(trimmed)) {
    return { language: 'sql', confidence: 0.82, source: 'content' };
  }
  if (/^\s*package\s+[A-Za-z_][\w.]*\s*(?:\n|$)/m.test(trimmed) && /\bfunc\s+\w+\s*\(/.test(trimmed)) {
    return { language: 'go', confidence: 0.9, source: 'content' };
  }
  if (/\b(?:fn\s+main|use\s+std::|let\s+mut\s+\w+|impl\s+\w+)\b/.test(trimmed)) {
    return { language: 'rust', confidence: 0.82, source: 'content' };
  }
  if (/^\s*#include\s*[<"]|\bstd::\w+|\bint\s+main\s*\(/m.test(trimmed)) {
    return { language: /\bstd::\w+/.test(trimmed) ? 'cpp' : 'c', confidence: 0.78, source: 'content' };
  }
  if (/^\s*(?:import\s+|export\s+|const\s+|let\s+|var\s+|function\s+|class\s+|console\.log\s*\(|(?:async\s+)?\w+\s*=>)/m.test(trimmed)) {
    const typescript = /\b(?:interface|type)\s+[A-Za-z_$]|:\s*(?:string|number|boolean|unknown|void)\b|\bas\s+[A-Za-z_$]/.test(trimmed);
    return { language: typescript ? 'typescript' : 'javascript', confidence: 0.72, source: 'content' };
  }
  if (/^\s*(?:def\s+\w+|class\s+\w+.*:)|\b(?:puts|require)\s+["']/.test(trimmed)) return { language: 'ruby', confidence: 0.7, source: 'content' };
  if (/^\s*(?:<\?php|namespace\s+[A-Za-z])|\$[A-Za-z_][\w]*\s*=/.test(trimmed)) return { language: 'php', confidence: 0.75, source: 'content' };
  if (/\b(?:fun\s+main|val\s+\w+|data\s+class)\b/.test(trimmed)) return { language: 'kotlin', confidence: 0.7, source: 'content' };
  if (/\b(?:import\s+Foundation|struct\s+\w+\s*:\s*|func\s+\w+\s*\()/.test(trimmed)) return { language: 'swift', confidence: 0.68, source: 'content' };
  if (/\b(?:void|bool|Future<.*>)\s+main\s*\(|\bimport\s+["']dart:/.test(trimmed)) return { language: 'dart', confidence: 0.7, source: 'content' };
  if (/^\s*(?:local\s+function|function\s+\w+|--\s)/m.test(trimmed)) return { language: 'lua', confidence: 0.7, source: 'content' };
  if (/^\s*(?:library\s*\(|c\s*\(|<-\s*)|\b(?:data\.frame|ggplot)\s*\(/m.test(trimmed)) return { language: 'r', confidence: 0.7, source: 'content' };
  if (/\b(?:function\s+\w+\s*\(|using\s+[A-Za-z]|println\()/.test(trimmed)) return { language: 'julia', confidence: 0.66, source: 'content' };
  if (/\b(?:defmodule|defp?|use\s+[A-Z][A-Za-z0-9.]*)\b/.test(trimmed)) return { language: 'elixir', confidence: 0.7, source: 'content' };
  if (/^\s*(?:module\s+\w+|import\s+Html|type\s+\w+\s*=)/m.test(trimmed)) return { language: 'elm', confidence: 0.7, source: 'content' };
  if (/[{}]\s*[A-Za-z.#:[\]-]+\s*[{;]/.test(trimmed) && /:\s*[^:;{}]+;/.test(trimmed)) {
    return { language: /\$[A-Za-z_-]+|@mixin|@include/.test(trimmed) ? 'scss' : 'css', confidence: 0.68, source: 'content' };
  }
  return { language: 'plaintext', confidence: 0, source: 'default' };
}

/** Normalize the intentionally small, read-only viewer configuration surface. */
export function normalizeCodeConfig(config: Record<string, unknown> | undefined): CodeViewerConfig {
  const raw = config || {};
  const rawTab = Number(raw.tabSize);
  const tabSize: 2 | 4 | 8 = rawTab === 2 || rawTab === 8 ? rawTab : 4;
  const rawWrap = raw.wordWrap;
  let wordWrap: CodeViewerConfig['wordWrap'] = 'off';
  if (rawWrap === true || rawWrap === 'on' || rawWrap === 'wordWrapColumn' || rawWrap === 'bounded') wordWrap = rawWrap === true ? 'on' : rawWrap;
  const rawFontSize = Number(raw.fontSize);
  const fontSize = Number.isFinite(rawFontSize) ? Math.min(20, Math.max(12, Math.round(rawFontSize))) : 14;
  const fallback = 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace';
  const candidate = typeof raw.fontFamily === 'string' ? raw.fontFamily.trim() : '';
  const safeFamily = /^[A-Za-z0-9 .,'"()_-]{1,180}$/.test(candidate) && !/(?:url|var|expression|important)/i.test(candidate) && candidate.includes(',')
    ? candidate
    : fallback;
  return {
    showLineNumbers: raw.showLineNumbers !== false,
    tabSize,
    wordWrap,
    fontSize,
    fontFamily: safeFamily,
  };
}

/** Bound the rendered source so a giant file cannot freeze the iframe. */
export function truncateCodeText(source: string, maxChars = 2_000_000): { text: string; truncated: boolean; originalLength: number } {
  const text = typeof source === 'string' ? source : String(source);
  const candidate = Number(maxChars);
  const limit = Number.isFinite(candidate) ? Math.max(1, Math.floor(candidate)) : 2_000_000;
  return text.length > limit
    ? { text: text.slice(0, limit), truncated: true, originalLength: text.length }
    : { text, truncated: false, originalLength: text.length };
}

/** Return case-insensitive literal matches with 1-based line and column data. */
export function findCodeMatches(source: string, query: string): Array<{ line: number; column: number; length: number }> {
  const text = typeof source === 'string' ? source : String(source);
  const needle = typeof query === 'string' ? query : String(query);
  if (needle.length === 0) return [];
  const lowerText = text.toLocaleLowerCase();
  const lowerNeedle = needle.toLocaleLowerCase();
  const matches: Array<{ line: number; column: number; length: number }> = [];
  let from = 0;
  while (from <= lowerText.length - lowerNeedle.length) {
    const index = lowerText.indexOf(lowerNeedle, from);
    if (index < 0) break;
    const before = text.slice(0, index);
    const lastNewline = before.lastIndexOf('\n');
    matches.push({ line: (before.match(/\n/g) || []).length + 1, column: index - lastNewline, length: needle.length });
    from = index + Math.max(1, lowerNeedle.length);
  }
  return matches;
}
