/**
 * Hermes Canopy — CSV/TSV viewer body (SPEC-PL-02 §9.4, phase 6).
 *
 * This shipped body is the zero-dependency subset: it runs inside the existing
 * sandboxed srcDoc and uses only getTextContent(), the bootstrap config, the
 * frame-local __handlers registry, logAccess, and ready. It deliberately
 * defers handsontable-grade virtualization, formula/edit/paste/undo,
 * drag-range selection, and host-bundle work. Rendering is capped at
 * maxRowsForInlineRender and visibly discloses truncation.
 */

import {
  buildCsvTable,
  clampMaxRowsForInlineRender,
  clampRowHeight,
  detectDelimiter,
  filterRows,
  inferColumnTypes,
  normalizeDelimiter,
  parseCsv,
  sortRows,
  type CsvColumnType,
} from './csvViewerLogic';

/** Helper bundle handed to the body script (property names are load-bearing). */
export const csvHelpers = {
  buildCsvTable,
  clampMaxRowsForInlineRender,
  clampRowHeight,
  detectDelimiter,
  filterRows,
  inferColumnTypes,
  normalizeDelimiter,
  parseCsv,
  sortRows,
};

type CsvHelpers = typeof csvHelpers;

function csvViewerBodyScript(H: CsvHelpers): void {
  'use strict';
  var canopy = (window as unknown as { canopy?: Record<string, unknown> }).canopy;
  var boot = ((canopy && canopy.__bootstrap) || {}) as {
    fileMeta?: { id?: string; filename?: string; mimeType?: string };
    config?: Record<string, unknown>;
  };
  var config = boot.config || {};
  var viewerApi = ((canopy && canopy.viewer) || {}) as Record<string, (...args: unknown[]) => unknown>;
  var fileMeta = boot.fileMeta || {};

  function emit(event: string, payload: Record<string, unknown>): void {
    // Phase-local delivery: the shim owns the handler registry. Do not invent
    // a postMessage envelope; its mount nonce is closure-captured in the shim.
    var handlerMap =
      (canopy && (canopy as { __handlers?: Record<string, Array<(value: unknown) => void>> }).__handlers) || {};
    var handlers = handlerMap[event];
    if (!Array.isArray(handlers)) return;
    for (var index = 0; index < handlers.length; index += 1) {
      try {
        handlers[index](payload);
      } catch {
        /* one consumer cannot break the viewer */
      }
    }
  }

  function safeLog(action: string, metadata: Record<string, unknown>): void {
    if (!viewerApi || typeof viewerApi.logAccess !== 'function') return;
    try {
      Promise.resolve(viewerApi.logAccess(action, metadata)).catch(function () {
        /* access logging is observational */
      });
    } catch {
      /* synchronous host stubs are observational too */
    }
  }

  function notifyReady(): void {
    if (!viewerApi || typeof viewerApi.ready !== 'function') return;
    try {
      Promise.resolve(viewerApi.ready()).catch(function () {
        /* rendering never depends on acknowledgement */
      });
    } catch {
      /* rendering never depends on acknowledgement */
    }
  }

  function errorMessage(error: unknown, fallback: string): string {
    if (error && typeof error === 'object' && typeof (error as { message?: unknown }).message === 'string') {
      return (error as { message: string }).message;
    }
    if (typeof error === 'string' && error) return error;
    return fallback;
  }

  var hasHeader = config.csvFirstRowIsHeader !== false;
  var showFilters = config.showFilters !== false;
  var showColumnHeaders = config.showColumnHeaders !== false;
  var freezeFirstRow = config.freezeFirstRow === true || (config.freezeFirstRow === undefined && hasHeader);
  var rowHeight = H.clampRowHeight(config.rowHeights, 24);
  var maxRows = H.clampMaxRowsForInlineRender(config.maxRowsForInlineRender, 10000);
  var delimiterOverride = H.normalizeDelimiter(config.delimiter);
  var activeDelimiter = delimiterOverride || ',';

  var rootEl = document.getElementById('root') || document.body;
  rootEl.innerHTML = '';
  rootEl.setAttribute('data-csv-viewer', '');
  rootEl.setAttribute(
    'style',
    'box-sizing:border-box;margin:0;min-height:100vh;width:100vw;overflow:auto;' +
      'background:#111827;color:#f3f4f6;font:14px/1.4 system-ui,-apple-system,"Segoe UI",sans-serif;padding:16px;',
  );

  var styleEl = document.createElement('style');
  styleEl.setAttribute('data-csv-style', '');
  styleEl.textContent = [
    '[data-csv-toolbar]{display:flex;align-items:center;gap:10px;flex-wrap:wrap;margin-bottom:12px;}',
    '[data-csv-stats]{color:#cbd5e1;font-size:13px;}',
    '[data-csv-grid]{overflow:auto;border:1px solid #374151;border-radius:8px;background:#0f172a;}',
    '[data-csv-table]{border-collapse:separate;border-spacing:0;min-width:100%;white-space:nowrap;}',
    '[data-csv-table th],[data-csv-table td]{border-right:1px solid #374151;border-bottom:1px solid #374151;padding:0 10px;text-align:left;}',
    '[data-csv-table th]{background:#1f2937;color:#f9fafb;font-weight:600;}',
    '[data-csv-table th] button{display:block;}',
    '[data-csv-sort]{width:100%;padding:0;border:0;background:transparent;color:inherit;text-align:left;font:inherit;cursor:pointer;}',
    '[data-csv-sort]:focus-visible,[data-csv-cell]:focus-visible,[data-csv-filter]:focus-visible{outline:2px solid #93c5fd;outline-offset:-2px;}',
    '[data-csv-filter]{box-sizing:border-box;width:100%;min-width:90px;padding:4px 6px;background:#111827;color:#f9fafb;border:1px solid #4b5563;border-radius:4px;}',
    '[data-csv-cell]{height:' + rowHeight + 'px;max-width:480px;overflow:hidden;text-overflow:ellipsis;}',
    '[data-csv-table tr:last-child td]{border-bottom:0;}',
    '[data-csv-table th:last-child],[data-csv-table td:last-child]{border-right:0;}',
    '[data-csv-frozen]{position:sticky;top:0;z-index:2;background:#1f2937;}',
    '[data-csv-empty],[data-csv-truncated],[data-csv-error]{padding:14px 16px;border-radius:8px;}',
    '[data-csv-empty]{color:#cbd5e1;background:#1e293b;}',
    '[data-csv-truncated]{margin-top:10px;color:#fde68a;background:#422006;border:1px solid #92400e;}',
    '[data-csv-error]{color:#fecdd3;background:#3f1d24;border:1px solid #9f1239;white-space:pre-wrap;font:13px/1.5 ui-monospace,monospace;}',
    '[data-csv-caption]{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0);}',
  ].join('\n');
  rootEl.appendChild(styleEl);

  var toolbar = document.createElement('div');
  toolbar.setAttribute('data-csv-toolbar', '');
  toolbar.setAttribute('role', 'region');
  toolbar.setAttribute('aria-label', 'CSV controls');
  rootEl.appendChild(toolbar);
  var stats = document.createElement('span');
  stats.setAttribute('data-csv-stats', '');
  toolbar.appendChild(stats);
  var gridHost = document.createElement('div');
  gridHost.setAttribute('data-csv-grid', '');
  rootEl.appendChild(gridHost);
  var messages = document.createElement('div');
  rootEl.appendChild(messages);
  var errorBox = document.createElement('div');
  errorBox.setAttribute('data-csv-error', '');
  errorBox.setAttribute('role', 'alert');
  errorBox.style.display = 'none';
  messages.appendChild(errorBox);
  var truncationNotice: HTMLElement | null = null;

  function showError(code: string, message: string, row?: number, col?: number): void {
    errorBox.style.display = 'block';
    errorBox.textContent = 'csv_error [' + code + ']\n' + message;
    var payload: Record<string, unknown> = { code: code, message: message };
    if (typeof row === 'number') payload.row = row;
    if (typeof col === 'number') payload.col = col;
    emit('csv_error', payload);
    safeLog('error', {
      code: code,
      errorCode: code,
      message: message,
      row: row,
      col: col,
      fileId: fileMeta.id || (canopy && canopy.fileId) || '',
    });
  }

  var tableData: { headers: string[]; rows: string[][]; hasHeader: boolean } | null = null;
  var columnTypes: CsvColumnType[] = [];
  var filters: Record<number, string> = {};
  var sortedColumn = -1;
  var sortDirection: 'asc' | 'desc' = 'asc';

  function configuredTypes(inferred: CsvColumnType[]): CsvColumnType[] {
    var configured = config.columnTypes as unknown[];
    if (!Array.isArray(configured)) return inferred;
    return inferred.map(function (kind, index) {
      var candidate = configured[index];
      return candidate === 'text' || candidate === 'numeric' || candidate === 'date' || candidate === 'boolean' ? candidate : kind;
    });
  }

  function selectCell(row: number, col: number, value: string): void {
    emit('csv_cell_selected', { row: row, col: col, value: value });
  }

  function renderTable(): void {
    if (!tableData) return;
    if (truncationNotice) {
      truncationNotice.remove();
      truncationNotice = null;
    }
    gridHost.innerHTML = '';
    var filtered = H.filterRows(tableData.rows, filters);
    var ordered = sortedColumn >= 0 ? H.sortRows(filtered, sortedColumn, sortDirection, columnTypes[sortedColumn] || 'text') : filtered;
    var rowsToRender = ordered.slice(0, maxRows);
    var truncated = ordered.length > rowsToRender.length;
    stats.textContent = ordered.length + ' row' + (ordered.length === 1 ? '' : 's') + ' · ' + tableData.headers.length + ' column' +
      (tableData.headers.length === 1 ? '' : 's') + ' · delimiter ' + activeDelimiter;

    if (rowsToRender.length === 0) {
      var empty = document.createElement('div');
      empty.setAttribute('data-csv-empty', '');
      empty.setAttribute('role', 'status');
      empty.textContent = tableData.rows.length === 0 ? 'This spreadsheet has no data rows.' : 'No rows match the current filters.';
      gridHost.appendChild(empty);
    } else {
      var table = document.createElement('table');
      table.setAttribute('data-csv-table', '');
      table.setAttribute('aria-label', fileMeta.filename || 'Spreadsheet data');
      var caption = document.createElement('caption');
      caption.setAttribute('data-csv-caption', '');
      caption.textContent = fileMeta.filename || 'CSV data';
      table.appendChild(caption);
      if (showColumnHeaders) {
        var thead = document.createElement('thead');
        var headerRow = document.createElement('tr');
        for (var col = 0; col < tableData.headers.length; col += 1) {
          var th = document.createElement('th');
          th.scope = 'col';
          th.setAttribute('data-csv-header', String(col));
          if (freezeFirstRow) th.setAttribute('data-csv-frozen', '');
          var sortButton = document.createElement('button');
          sortButton.type = 'button';
          sortButton.setAttribute('data-csv-sort', String(col));
          sortButton.setAttribute('aria-label', 'Sort by ' + tableData.headers[col]);
          sortButton.textContent = tableData.headers[col] + (sortedColumn === col ? (sortDirection === 'asc' ? ' ↑' : ' ↓') : '');
          sortButton.addEventListener('click', function (event: Event) {
            var target = event.currentTarget as HTMLElement;
            var clicked = Number(target.getAttribute('data-csv-sort'));
            if (sortedColumn === clicked) sortDirection = sortDirection === 'asc' ? 'desc' : 'asc';
            else {
              sortedColumn = clicked;
              sortDirection = 'asc';
            }
            renderTable();
            emit('csv_sorted', { column: clicked, direction: sortDirection });
          });
          th.appendChild(sortButton);
          headerRow.appendChild(th);
        }
        thead.appendChild(headerRow);
        if (showFilters) {
          var filterRow = document.createElement('tr');
          for (var filterCol = 0; filterCol < tableData.headers.length; filterCol += 1) {
            var filterCell = document.createElement('th');
            filterCell.scope = 'col';
            var input = document.createElement('input');
            input.type = 'search';
            input.setAttribute('data-csv-filter', String(filterCol));
            input.setAttribute('aria-label', 'Filter ' + tableData.headers[filterCol]);
            input.placeholder = 'Filter…';
            input.value = filters[filterCol] || '';
            input.addEventListener('input', function (event: Event) {
              var field = event.currentTarget as HTMLInputElement;
              var filterIndex = Number(field.getAttribute('data-csv-filter'));
              filters[filterIndex] = field.value;
              renderTable();
              var matching = H.filterRows(tableData ? tableData.rows : [], filters).length;
              emit('csv_filtered', { visibleRows: matching, totalRows: tableData ? tableData.rows.length : 0 });
            });
            filterCell.appendChild(input);
            filterRow.appendChild(filterCell);
          }
          thead.appendChild(filterRow);
        }
        table.appendChild(thead);
      }
      var tbody = document.createElement('tbody');
      for (var rowIndex = 0; rowIndex < rowsToRender.length; rowIndex += 1) {
        var tr = document.createElement('tr');
        if (freezeFirstRow && !showColumnHeaders && rowIndex === 0) tr.setAttribute('data-csv-frozen', '');
        for (var cellIndex = 0; cellIndex < tableData.headers.length; cellIndex += 1) {
          var td = document.createElement('td');
          td.setAttribute('data-csv-cell', '');
          td.setAttribute('data-row', String(rowIndex));
          td.setAttribute('data-col', String(cellIndex));
          td.setAttribute('tabindex', '0');
          td.setAttribute('role', 'gridcell');
          td.setAttribute('aria-label', tableData.headers[cellIndex] + ', row ' + (rowIndex + 1));
          td.textContent = rowsToRender[rowIndex][cellIndex] || '';
          td.addEventListener('click', function (event: Event) {
            var cell = event.currentTarget as HTMLElement;
            selectCell(Number(cell.getAttribute('data-row')), Number(cell.getAttribute('data-col')), cell.textContent || '');
          });
          td.addEventListener('keydown', function (event: KeyboardEvent) {
            if (event.key === 'Enter' || event.key === ' ') {
              event.preventDefault();
              var cell = event.currentTarget as HTMLElement;
              selectCell(Number(cell.getAttribute('data-row')), Number(cell.getAttribute('data-col')), cell.textContent || '');
            }
          });
          tr.appendChild(td);
        }
        tbody.appendChild(tr);
      }
      table.appendChild(tbody);
      gridHost.appendChild(table);
    }
    if (truncated) {
      var notice = document.createElement('div');
      notice.setAttribute('data-csv-truncated', '');
      notice.setAttribute('role', 'status');
      notice.textContent = 'Showing ' + rowsToRender.length + ' of ' + ordered.length + ' matching rows. Inline rendering is capped at ' + maxRows + '; this zero-dependency viewer does not provide virtualization.';
      messages.appendChild(notice);
      truncationNotice = notice;
    }
  }

  function renderFromText(text: string): void {
    try {
      activeDelimiter = delimiterOverride || H.detectDelimiter(text);
      var parsedRows = H.parseCsv(text, delimiterOverride);
      tableData = H.buildCsvTable(parsedRows, hasHeader);
      columnTypes = configuredTypes(H.inferColumnTypes(tableData.rows));
      filters = {};
      sortedColumn = -1;
      sortDirection = 'asc';
      errorBox.style.display = 'none';
      renderTable();
      emit('csv_ready', { rowCount: tableData.rows.length, colCount: tableData.headers.length });
    } catch (error) {
      var issue = error as { code?: string; message?: string; row?: number; col?: number };
      showError(issue.code || 'CSV_PARSE_ERROR', errorMessage(error, 'The CSV parser failed on this document.'), issue.row, issue.col);
    }
  }

  safeLog('view', {
    fileId: fileMeta.id || (canopy && canopy.fileId) || '',
    filename: fileMeta.filename || '',
    mimeType: fileMeta.mimeType || '',
    delimiter: delimiterOverride || 'auto',
    firstRowIsHeader: hasHeader,
    rowHeight: rowHeight,
    maxRowsForInlineRender: maxRows,
  });
  if (viewerApi && typeof viewerApi.getTextContent === 'function') {
    Promise.resolve(viewerApi.getTextContent())
      .then(function (text) {
        renderFromText(typeof text === 'string' ? text : String(text));
      })
      .catch(function (error: unknown) {
        showError('TEXT_CONTENT_ERROR', errorMessage(error, 'The file text could not be loaded.'));
      });
  } else {
    showError('SHIM_MISSING', 'canopy.viewer.getTextContent is not available in this frame.');
  }
  notifyReady();
}

/** The full in-iframe script source (IIFE) injected after the shim. */
export const csvViewerBody: string =
  '(' +
  csvViewerBodyScript.toString() +
  ')({' +
  Object.entries(csvHelpers)
    .map(function ([name, helper]) {
      return name + ': ' + helper.toString();
    })
    .join(', ') +
  '});';
