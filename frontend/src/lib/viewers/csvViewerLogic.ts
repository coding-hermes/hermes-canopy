/**
 * Hermes Canopy — CSV/TSV viewer pure logic (SPEC-PL-02 §9.4, phase 6).
 *
 * This is the zero-dependency subset that can run in the sandboxed srcDoc:
 * delimiter sniffing, strict RFC4180-style parsing, rectangular table
 * preparation, lightweight type inference, stable sorting/filtering, and
 * bounded rendering configuration. Every exported helper is self-contained;
 * the body serializes these functions with Function.prototype.toString.
 *
 * Deferred from §9.4: handsontable-grade virtualization, formulas, editing,
 * paste/undo, drag-range selection, and host-bundle work. This phase renders
 * at most maxRowsForInlineRender rows and says so visibly.
 */

export type CsvColumnType = 'text' | 'numeric' | 'date' | 'boolean';

export interface CsvTable {
  headers: string[];
  rows: string[][];
  hasHeader: boolean;
}

export interface CsvParseIssue {
  code: 'CSV_PARSE_ERROR';
  message: string;
  row: number;
  col: number;
  line: number;
  column: number;
}

/**
 * Detects among the four CSV delimiters using the first five logical records.
 * Counts only delimiters outside quoted fields and favours a candidate whose
 * field count is consistent across records. Ties are deterministic.
 */
export function detectDelimiter(source: string): string {
  const candidates = [',', '\t', ';', '|'];
  const records: string[] = [];
  let start = 0;
  let quoted = false;
  for (let i = 0; i < source.length && records.length < 5; i += 1) {
    const ch = source[i];
    if (ch === '"') {
      if (quoted && source[i + 1] === '"') {
        i += 1;
      } else {
        quoted = !quoted;
      }
    } else if (!quoted && ch === '\n') {
      records.push(source.slice(start, i));
      start = i + 1;
    }
  }
  if (records.length < 5 && start < source.length) records.push(source.slice(start));
  if (records.length === 0 && source.length > 0) records.push(source);

  let best = ',';
  let bestScore = 0;
  for (const candidate of candidates) {
    const counts: number[] = [];
    for (const record of records.slice(0, 5)) {
      let count = 0;
      let inQuotes = false;
      for (let i = 0; i < record.length; i += 1) {
        const ch = record[i];
        if (ch === '"') {
          if (inQuotes && record[i + 1] === '"') i += 1;
          else inQuotes = !inQuotes;
        } else if (!inQuotes && ch === candidate) {
          count += 1;
        }
      }
      counts.push(count);
    }
    const nonEmpty = counts.filter((count) => count > 0);
    if (nonEmpty.length === 0) continue;
    const minimum = Math.min(...nonEmpty);
    const maximum = Math.max(...nonEmpty);
    const consistency = minimum === maximum ? 2 : 0;
    const score = nonEmpty.length * 10 + minimum * 2 + consistency;
    if (score > bestScore) {
      best = candidate;
      bestScore = score;
    }
  }
  return best;
}

/** Convert a config delimiter spelling into a single delimiter character. */
export function normalizeDelimiter(raw: unknown): string | undefined {
  if (raw === '\\t') return '\t';
  if (typeof raw !== 'string' || raw.length === 0) return undefined;
  return raw.length === 1 ? raw : undefined;
}

/**
 * Strict CSV parser. It preserves quoted newlines and empty fields, accepts
 * CRLF/LF, and throws a plain location-bearing issue on malformed input.
 */
export function parseCsv(source: string, delimiter?: string): string[][] {
  const rawDelimiter = typeof delimiter === 'string' && delimiter === '\\t' ? '\t' : delimiter;
  let separator = rawDelimiter && rawDelimiter.length === 1 ? rawDelimiter : '';
  if (!separator) {
    const candidates = [',', '\t', ';', '|'];
    const records: string[] = [];
    let start = 0;
    let quoted = false;
    for (let i = 0; i < source.length && records.length < 5; i += 1) {
      const ch = source[i];
      if (ch === '"') {
        if (quoted && source[i + 1] === '"') i += 1;
        else quoted = !quoted;
      } else if (!quoted && ch === '\n') {
        records.push(source.slice(start, i));
        start = i + 1;
      }
    }
    if (records.length < 5 && start < source.length) records.push(source.slice(start));
    if (records.length === 0 && source.length > 0) records.push(source);
    let bestScore = 0;
    separator = ',';
    for (const candidate of candidates) {
      const counts: number[] = [];
      for (const record of records) {
        let count = 0;
        let quotedRecord = false;
        for (let i = 0; i < record.length; i += 1) {
          if (record[i] === '"') {
            if (quotedRecord && record[i + 1] === '"') i += 1;
            else quotedRecord = !quotedRecord;
          } else if (!quotedRecord && record[i] === candidate) count += 1;
        }
        counts.push(count);
      }
      const nonEmpty = counts.filter((count) => count > 0);
      if (nonEmpty.length === 0) continue;
      const minimum = Math.min(...nonEmpty);
      const maximum = Math.max(...nonEmpty);
      const score = nonEmpty.length * 10 + minimum * 2 + (minimum === maximum ? 2 : 0);
      if (score > bestScore) {
        bestScore = score;
        separator = candidate;
      }
    }
  }
  const rows: string[][] = [];
  let row: string[] = [];
  let field = '';
  let inQuotes = false;
  let afterQuote = false;
  let rowNumber = 1;
  let line = 1;
  let column = 1;
  let sawInput = false;

  function issue(message: string): never {
    throw { code: 'CSV_PARSE_ERROR', message, row: rowNumber, col: column, line, column } as CsvParseIssue;
  }
  function pushField(): void {
    row.push(field);
    field = '';
    afterQuote = false;
  }
  function pushRow(): void {
    pushField();
    rows.push(row);
    row = [];
    rowNumber += 1;
  }

  for (let i = 0; i < source.length; i += 1) {
    sawInput = true;
    const ch = source[i];
    if (inQuotes) {
      if (ch === '"') {
        if (source[i + 1] === '"') {
          field += '"';
          i += 1;
          column += 2;
          continue;
        }
        inQuotes = false;
        afterQuote = true;
        column += 1;
        continue;
      }
      if (ch === '\r' && source[i + 1] === '\n') {
        field += '\r\n';
        i += 1;
        line += 1;
        column = 1;
      } else {
        field += ch;
        if (ch === '\n') {
          line += 1;
          column = 1;
        } else {
          column += 1;
        }
      }
      continue;
    }

    if (afterQuote) {
      if (ch === separator) {
        pushField();
        column += 1;
        continue;
      }
      if (ch === '\n' || ch === '\r') {
        if (ch === '\r' && source[i + 1] === '\n') i += 1;
        pushRow();
        line += 1;
        column = 1;
        continue;
      }
      issue('Unexpected character after closing quote');
    }

    if (ch === '"') {
      if (field !== '') issue('Quote must begin a field');
      inQuotes = true;
      column += 1;
    } else if (ch === separator) {
      pushField();
      column += 1;
    } else if (ch === '\n' || ch === '\r') {
      if (ch === '\r' && source[i + 1] === '\n') i += 1;
      pushRow();
      line += 1;
      column = 1;
    } else {
      field += ch;
      column += 1;
    }
  }

  if (inQuotes) issue('Unterminated quoted field');
  if (sawInput && (field !== '' || row.length > 0 || afterQuote)) pushRow();
  return rows;
}

/** Normalize every row to the source's maximum width without mutating input. */
export function normalizeRows(rows: string[][]): string[][] {
  let width = 0;
  for (const row of rows) width = Math.max(width, row.length);
  return rows.map((row) => {
    const copy = row.slice();
    while (copy.length < width) copy.push('');
    return copy;
  });
}

/**
 * Extracts headings and data rows. Header cells remain data-preserving; only
 * blank headings receive an accessible, deterministic Column N label.
 */
export function buildCsvTable(rows: string[][], firstRowIsHeader = true): CsvTable {
  let width = 0;
  for (const row of rows) width = Math.max(width, row.length);
  const rectangular = rows.map((row) => {
    const copy = row.slice();
    while (copy.length < width) copy.push('');
    return copy;
  });
  if (rectangular.length === 0) return { headers: [], rows: [], hasHeader: firstRowIsHeader };
  const headers: string[] = [];
  if (firstRowIsHeader) {
    for (let i = 0; i < width; i += 1) headers.push(rectangular[0][i] || `Column ${i + 1}`);
    return { headers, rows: rectangular.slice(1).map((row) => row.slice()), hasHeader: true };
  }
  for (let i = 0; i < width; i += 1) headers.push(`Column ${i + 1}`);
  return { headers, rows: rectangular.map((row) => row.slice()), hasHeader: false };
}

export function extractHeaders(rows: string[][], firstRowIsHeader = true): CsvTable {
  let width = 0;
  for (const row of rows) width = Math.max(width, row.length);
  const rectangular = rows.map((row) => {
    const copy = row.slice();
    while (copy.length < width) copy.push('');
    return copy;
  });
  if (rectangular.length === 0) return { headers: [], rows: [], hasHeader: firstRowIsHeader };
  const headers: string[] = [];
  if (firstRowIsHeader) {
    for (let i = 0; i < width; i += 1) headers.push(rectangular[0][i] || `Column ${i + 1}`);
    return { headers, rows: rectangular.slice(1).map((row) => row.slice()), hasHeader: true };
  }
  for (let i = 0; i < width; i += 1) headers.push(`Column ${i + 1}`);
  return { headers, rows: rectangular.map((row) => row.slice()), hasHeader: false };
}

/** Infer a conservative per-column type, ignoring empty cells. */
export function inferColumnTypes(rows: string[][]): CsvColumnType[] {
  let width = 0;
  for (const row of rows) width = Math.max(width, row.length);
  const result: CsvColumnType[] = [];
  for (let col = 0; col < width; col += 1) {
    const values = rows.map((row) => String(row[col] ?? '').trim()).filter((value) => value !== '');
    if (values.length === 0) {
      result.push('text');
      continue;
    }
    if (values.every((value) => /^(?:true|false)$/i.test(value))) {
      result.push('boolean');
      continue;
    }
    if (values.every((value) => /^[+-]?(?:(?:\d+(?:\.\d*)?)|(?:\.\d+))(?:[eE][+-]?\d+)?$/.test(value))) {
      result.push('numeric');
      continue;
    }
    if (values.every((value) => /^\d{4}-\d{2}-\d{2}(?:[T ][0-9]{2}:[0-9]{2}(?::[0-9]{2}(?:\.\d+)?)?(?:Z|[+-][0-9]{2}:?[0-9]{2})?)?$/.test(value) && Number.isFinite(Date.parse(value)))) {
      result.push('date');
      continue;
    }
    result.push('text');
  }
  return result;
}

/** Stable, non-mutating sort. Ties retain original source order. */
export function sortRows(
  rows: string[][],
  column: number,
  direction: 'asc' | 'desc' = 'asc',
  columnType: CsvColumnType = 'text',
): string[][] {
  const indexed = rows.map((row, index) => ({ row: row.slice(), index }));
  indexed.sort((left, right) => {
    const a = String(left.row[column] ?? '').trim();
    const b = String(right.row[column] ?? '').trim();
    let comparison = 0;
    if (columnType === 'numeric') comparison = (Number(a) || 0) - (Number(b) || 0);
    else if (columnType === 'date') comparison = (Date.parse(a) || 0) - (Date.parse(b) || 0);
    else if (columnType === 'boolean') comparison = Number(a.toLowerCase() === 'true') - Number(b.toLowerCase() === 'true');
    else comparison = a.localeCompare(b, undefined, { sensitivity: 'base', numeric: true });
    if (comparison === 0) return left.index - right.index;
    return direction === 'desc' ? -comparison : comparison;
  });
  return indexed.map((entry) => entry.row);
}

/** Case-insensitive AND filtering over a column→query map, non-mutating. */
export function filterRows(rows: string[][], filters: Record<number, string>): string[][] {
  const entries = Object.entries(filters).filter(([, value]) => String(value).trim() !== '');
  return rows
    .filter((row) => entries.every(([column, query]) => String(row[Number(column)] ?? '').toLocaleLowerCase().includes(String(query).toLocaleLowerCase())))
    .map((row) => row.slice());
}

/** Row height clamp: safe readable range, default 24px. */
export function clampRowHeight(raw: unknown, fallback = 24): number {
  const candidate = typeof raw === 'number' ? raw : Number(raw);
  const safeFallback = Number.isFinite(fallback) ? Math.round(fallback) : 24;
  if (!Number.isFinite(candidate)) return Math.min(80, Math.max(16, safeFallback));
  return Math.min(80, Math.max(16, Math.round(candidate)));
}

/** Inline render cap clamp: default 10,000, with a hard 100,000 ceiling. */
export function clampMaxRowsForInlineRender(raw: unknown, fallback = 10000): number {
  const candidate = typeof raw === 'number' ? raw : Number(raw);
  const safeFallback = Number.isFinite(fallback) ? Math.round(fallback) : 10000;
  if (!Number.isFinite(candidate)) return Math.min(100000, Math.max(1, safeFallback));
  return Math.min(100000, Math.max(1, Math.round(candidate)));
}
