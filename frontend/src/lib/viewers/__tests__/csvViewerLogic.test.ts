import { describe, expect, it } from 'vitest';

import {
  buildCsvTable,
  clampMaxRowsForInlineRender,
  clampRowHeight,
  detectDelimiter,
  filterRows,
  inferColumnTypes,
  normalizeRows,
  parseCsv,
  sortRows,
} from '../csvViewerLogic';

describe('CSV delimiter detection and parsing', () => {
  it.each([
    ['comma', 'name,age\nAda,36\nGrace,28', ','],
    ['tab', 'name\tage\nAda\t36\nGrace\t28', '\t'],
    ['semicolon', 'name;age\nAda;36\nGrace;28', ';'],
    ['pipe', 'name|age\nAda|36\nGrace|28', '|'],
  ])('detects %s records', (_name, source, expected) => {
    expect(detectDelimiter(source)).toBe(expected);
  });

  it('ignores candidate delimiters inside quoted fields', () => {
    expect(detectDelimiter('name,notes\nAda,"likes ; and | and tabs"\nGrace,"still, comma"')).toBe(',');
    expect(parseCsv('name,notes\nAda,"likes ; and |"')).toEqual([
      ['name', 'notes'],
      ['Ada', 'likes ; and |'],
    ]);
  });

  it('honors an explicit delimiter rather than autodetection', () => {
    expect(parseCsv('a;b\n1;2', ';')).toEqual([
      ['a', 'b'],
      ['1', '2'],
    ]);
    expect(parseCsv('a\tb\n1\t2', '\\t')).toEqual([
      ['a', 'b'],
      ['1', '2'],
    ]);
  });

  it('handles escaped quotes, empty fields, CRLF, and quoted newlines', () => {
    expect(parseCsv('a,b,c\r\n"say ""hi""",,"line 1\r\nline 2"\r\n')).toEqual([
      ['a', 'b', 'c'],
      ['say "hi"', '', 'line 1\r\nline 2'],
    ]);
  });

  it('preserves blank records and trailing empty fields', () => {
    expect(parseCsv('a,b\n\n,c\n')).toEqual([
      ['a', 'b'],
      [''],
      ['', 'c'],
    ]);
  });

  it('reports an honest row/column location for malformed quotes', () => {
    let issue: unknown;
    try {
      parseCsv('ok,value\n1,"unclosed');
    } catch (error) {
      issue = error;
    }
    expect(issue).toEqual(expect.objectContaining({ code: 'CSV_PARSE_ERROR', message: expect.stringContaining('Unterminated') }));
    expect((issue as { row: number }).row).toBe(2);
    expect((issue as { col: number }).col).toBeGreaterThan(1);
    expect((issue as { line: number }).line).toBe(2);
  });
});

describe('CSV table preparation', () => {
  it('normalizes ragged rows without changing the source', () => {
    const source = [['a'], ['b', 'c', 'd']];
    const copy = source.map((row) => row.slice());
    expect(normalizeRows(source)).toEqual([['a', '', ''], ['b', 'c', 'd']]);
    expect(source).toEqual(copy);
  });

  it('extracts a header by default and supplies accessible names for blanks', () => {
    expect(buildCsvTable([['Name', ''], ['Ada', '36']])).toEqual({
      headers: ['Name', 'Column 2'],
      rows: [['Ada', '36']],
      hasHeader: true,
    });
  });

  it('keeps the first row as data when header mode is disabled', () => {
    expect(buildCsvTable([['Ada', '36'], ['Grace', '28']], false)).toEqual({
      headers: ['Column 1', 'Column 2'],
      rows: [['Ada', '36'], ['Grace', '28']],
      hasHeader: false,
    });
  });

  it('returns a stable rectangular empty table', () => {
    expect(buildCsvTable([])).toEqual({ headers: [], rows: [], hasHeader: true });
    expect(buildCsvTable([['a'], ['b', 'c']], false).rows).toEqual([['a', ''], ['b', 'c']]);
  });
});

describe('CSV inference, sorting, and filtering', () => {
  it('infers conservative numeric, date, boolean, and text columns', () => {
    expect(
      inferColumnTypes([
        ['1', '2024-01-01', 'TRUE', 'hello'],
        ['2.50', '2024-02-03', 'false', '007'],
        ['', '2024-03-04', '', 'world'],
      ]),
    ).toEqual(['numeric', 'date', 'boolean', 'text']);
    expect(inferColumnTypes([['12'], ['12.5']])).toEqual(['numeric']);
    expect(inferColumnTypes([['2024-99-99']])).toEqual(['text']);
  });

  it('sorts stably and does not mutate rows', () => {
    const source = [['b', '2'], ['a', '1'], ['b', '1']];
    const copy = source.map((row) => row.slice());
    expect(sortRows(source, 0, 'asc')).toEqual([['a', '1'], ['b', '2'], ['b', '1']]);
    expect(sortRows(source, 1, 'desc', 'numeric')).toEqual([['b', '2'], ['a', '1'], ['b', '1']]);
    expect(source).toEqual(copy);
  });

  it('filters each requested column case-insensitively without mutation', () => {
    const source = [['Ada', 'Engineer'], ['Grace', 'Mathematician'], ['ada', 'Writer']];
    const copy = source.map((row) => row.slice());
    expect(filterRows(source, { 0: 'ADA' })).toEqual([['Ada', 'Engineer'], ['ada', 'Writer']]);
    expect(filterRows(source, { 0: 'ada', 1: 'eng' })).toEqual([['Ada', 'Engineer']]);
    expect(filterRows(source, { 0: '' })).toEqual(source);
    expect(source).toEqual(copy);
  });
});

describe('CSV viewer config clamps', () => {
  it('clamps row height to 16–80px and defaults invalid values to 24px', () => {
    expect(clampRowHeight(1)).toBe(16);
    expect(clampRowHeight(100)).toBe(80);
    expect(clampRowHeight('bad')).toBe(24);
    expect(clampRowHeight(Number.NaN)).toBe(24);
  });

  it('clamps inline rows to 1–100000 and defaults invalid values to 10000', () => {
    expect(clampMaxRowsForInlineRender(0)).toBe(1);
    expect(clampMaxRowsForInlineRender(200000)).toBe(100000);
    expect(clampMaxRowsForInlineRender('bad')).toBe(10000);
    expect(clampMaxRowsForInlineRender(Number.NaN)).toBe(10000);
  });
});
