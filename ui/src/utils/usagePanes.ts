// The graphs stacked in the context usage popup. Each can be switched on or
// off independently; the ones that are on share one x axis.

export type UsagePane = "context" | "cumulative" | "incremental";

/** Top-to-bottom order of the stack, whatever order panes were switched on. */
export const USAGE_PANES: readonly UsagePane[] = ["context", "cumulative", "incremental"];

// Context and cumulative cost get the same room; the per-call strip, whose
// spikes read fine at a glance, gets a third of it.
const FULL_PANE_HEIGHT = 120;
export const PANE_HEIGHT: Record<UsagePane, number> = {
  context: FULL_PANE_HEIGHT,
  cumulative: FULL_PANE_HEIGHT,
  incremental: FULL_PANE_HEIGHT / 3,
};

/** Panes stored by an earlier visit, in stack order. Anything unreadable (no
 *  entry, bad JSON, wrong shape) means the default, every pane; unknown names
 *  are dropped, and an empty list is a valid choice (all switched off). */
export function parseStoredPanes(raw: string | null): UsagePane[] {
  if (raw === null) return [...USAGE_PANES];
  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch {
    return [...USAGE_PANES];
  }
  if (!Array.isArray(value)) return [...USAGE_PANES];
  return USAGE_PANES.filter((pane) => value.includes(pane));
}

export function serializePanes(panes: readonly UsagePane[]): string {
  return JSON.stringify(USAGE_PANES.filter((pane) => panes.includes(pane)));
}

export interface PaneBox {
  pane: UsagePane;
  top: number;
  height: number;
  bottom: number;
}

/** Vertical boxes for the visible panes, in stack order, starting at top and
 *  separated by gap, each scale times its PANE_HEIGHT tall. */
export function layoutPanes(
  visible: readonly UsagePane[],
  top: number,
  gap: number,
  scale: number,
): PaneBox[] {
  const boxes: PaneBox[] = [];
  let y = top;
  for (const pane of USAGE_PANES) {
    if (!visible.includes(pane)) continue;
    const height = PANE_HEIGHT[pane] * scale;
    boxes.push({ pane, top: y, height, bottom: y + height });
    y += height + gap;
  }
  return boxes;
}
