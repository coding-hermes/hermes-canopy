/**
 * Hermes Canopy — tree export-file parsing (GAP-093).
 *
 * `parseTreeImportFile` validates a user-selected Canopy export file before
 * anything is sent to POST /api/v1/trees/import. It lives in lib, not beside
 * the FirstRunOnboarding component, because a .tsx module that also exports a
 * plain function breaks React Fast Refresh (only-export-components) — and
 * this is pure parsing with no React in it, unit-tested on its own.
 */

/**
 * Parse a user-selected export file into the body POST /trees/import
 * expects. A malformed file returns its real error and never reaches the
 * API; the component only branches on the outcome.
 */
export function parseTreeImportFile(
  text: string,
): { ok: true; data: Record<string, unknown> } | { ok: false; error: string } {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return { ok: false, error: 'The file is not valid JSON.' };
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    return { ok: false, error: 'Export files must be a JSON object (tree + nodes + edges).' };
  }
  const obj = parsed as Record<string, unknown>;
  if (typeof obj.tree !== 'object' || obj.tree === null) {
    return { ok: false, error: "The file has no 'tree' section — this does not look like a Canopy tree export." };
  }
  const tree = obj.tree as Record<string, unknown>;
  if (typeof tree.title !== 'string' || tree.title.trim().length === 0) {
    return { ok: false, error: "The export's tree section has no title — can't create a tree without a name." };
  }
  if (!Array.isArray(obj.nodes) || obj.nodes.length === 0) {
    return { ok: false, error: 'The export contains no messages (nodes) — nothing to import.' };
  }
  return { ok: true, data: obj };
}
