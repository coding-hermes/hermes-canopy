/**
 * Hermes Canopy — trees-page list helpers.
 *
 * Pure list/payload functions the Trees page and its dialog use. They live in
 * lib rather than in the page module so TreesPage.tsx exports only its
 * component (React Fast Refresh) and these stay unit-testable without
 * rendering a route.
 */

/**
 * Append an incoming page of trees to the existing list, deduplicating by id.
 * The create/delete handlers mutate the list and load-more can refetch a
 * cursor boundary, so duplicates are possible — keep the first occurrence
 * (the already-loaded/newer one) and preserve existing order.
 */
export function appendTrees<T extends { id: string }>(existing: T[], incoming: T[]): T[] {
  const seen = new Set(existing.map((t) => t.id));
  const merged = existing.slice();
  for (const tree of incoming) {
    if (!seen.has(tree.id)) {
      seen.add(tree.id);
      merged.push(tree);
    }
  }
  return merged;
}

/**
 * Build the POST /trees body from dialog fields.
 *
 * The backend (GAP-008 contract) requires a root message with non-empty
 * content AND a valid nodeType ('message' | 'announcement') — a title-only
 * payload 400s with VALIDATION_ERROR 'root message content is required'.
 * Returns `null` when the required fields are missing (callers show a
 * validation error instead of hitting the API).
 */
export function buildCreateTreePayload(
  title: string,
  description: string,
  rootContent: string,
): Record<string, unknown> | null {
  if (!title.trim() || !rootContent.trim()) return null;
  const body: Record<string, unknown> = {
    title: title.trim(),
    rootMessage: {
      content: rootContent.trim(),
      contentFormat: 'markdown',
      nodeType: 'message',
    },
  };
  if (description.trim()) body.description = description.trim();
  return body;
}
