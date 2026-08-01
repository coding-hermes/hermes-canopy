/**
 * Hermes Canopy — Topic icon mapping (UI-02)
 *
 * The mockup (docs/mockups/mockup-1.png) shows a distinct glyph per topic
 * pill — a target for "Strategy", a lightbulb for "Product Ideas", a chart
 * for "Market Research", and so on. The API carries no icon field on a
 * topic, so the glyph is derived from the title.
 *
 * The mapping is a pure function of the title, which keeps a topic's
 * identity stable: the same topic shows the same glyph on every surface
 * and across reloads, with no persisted state.
 *
 * Lives outside `TopicsRail.tsx` so that file only exports its component
 * (react-refresh/only-export-components).
 */

import {
  Hash,
  Target,
  Lightbulb,
  BarChart3,
  Users,
  Map,
  AlertTriangle,
  Rocket,
  PenTool,
  Bug,
  Code2,
  FileText,
  Calendar,
  Beaker,
  type LucideIcon,
} from 'lucide-react';

/**
 * Keyword → icon table, evaluated in order. Earlier entries win, so the
 * more specific domains are listed before the generic ones.
 */
const ICON_KEYWORDS: ReadonlyArray<readonly [RegExp, LucideIcon]> = [
  [/strateg|goal|objective|okr/i, Target],
  [/idea|brainstorm|concept|proposal/i, Lightbulb],
  [/research|analysis|market|metric|data/i, BarChart3],
  [/feedback|user|customer|interview|people/i, Users],
  [/roadmap|milestone|timeline/i, Map],
  [/risk|constraint|blocker|issue|concern/i, AlertTriangle],
  [/launch|release|ship|deploy|rollout/i, Rocket],
  [/design|ux|visual|mockup/i, PenTool],
  [/bug|defect|regression|incident/i, Bug],
  [/code|api|implementation|refactor|architect/i, Code2],
  [/spec|doc|note|report|write/i, FileText],
  [/meeting|standup|sync|retro|schedule|plan/i, Calendar],
  [/experiment|test|trial|spike|prototype/i, Beaker],
];

/** The glyph used when no keyword matches — matches "Off Topic" in the mockup. */
export const DEFAULT_TOPIC_ICON: LucideIcon = Hash;

/** Resolve the semantic glyph for a topic title. Falls back to `#`. */
export function topicIcon(title: string): LucideIcon {
  for (const [pattern, Icon] of ICON_KEYWORDS) {
    if (pattern.test(title)) return Icon;
  }
  return DEFAULT_TOPIC_ICON;
}

/**
 * Order topics for the rail: busiest subgraph first, ties broken by title.
 *
 * The API returns topics newest-first, which puts the most substantial
 * topic wherever it happened to be created — in the mockup the largest
 * topics sit at the top of the rail, and a stable, content-driven order
 * also keeps pill positions from shuffling as topics are added.
 *
 * Returns a new array; the input is not mutated.
 */
export function orderTopics<T extends { title: string; node_count?: number }>(
  topics: readonly T[],
): T[] {
  return [...topics].sort(
    (a, b) =>
      (b.node_count ?? 0) - (a.node_count ?? 0) ||
      a.title.localeCompare(b.title),
  );
}
