// Shared render-model types for ChatInterface.vue and MessageRenderNode.vue.
import type { Message } from "../../types";
import type { CoalescedItem } from "./coalesce";

export type RenderNode =
  | { kind: "day-separator"; key: string; label: string }
  | { kind: "timestamp"; key: string; createdAt: string }
  | { kind: "token-marker"; key: string; label: string; ctx: number }
  | { kind: "message"; key: string; item: CoalescedItem }
  | { kind: "tool-pills"; key: string; items: CoalescedItem[] }
  | { kind: "tool-call"; key: string; item: CoalescedItem }
  | { kind: "carried-band"; key: string; count: number; children: RenderNode[] };

// A run of consecutive render nodes wrapped in one content-visibility:auto
// element. Granularity matters in WebKit: one giant container (the whole
// generation) can never be skipped and costs 100ms+ per frame just managing
// the containment of its ~50k-node subtree, while per-row containment
// (thousands of elements) makes every frame re-check thousands of
// viewport-relevancy candidates. Chunks of a few dozen rows hit the sweet
// spot: off-screen chunks skip layout/paint entirely and the per-frame
// bookkeeping stays trivial.
export interface RenderChunk {
  key: string;
  nodes: RenderNode[];
  // Trailing chunks render without content-visibility (see LIVE_TAIL_CHUNKS):
  // real layout from birth, so their heights are never estimates.
  live?: boolean;
  // Position in the conversation-wide chunk sequence (across generation
  // blocks). Drives tail-first mounting: chunks below the mount floor render
  // as fixed-height placeholders until the background sweep (or a
  // near-viewport reveal) reaches them. Positional rather than key-based:
  // history chunks are stable under appends, and the rare mid-history
  // restructure (generation flip, view-mode change) resets the floor anyway.
  globalIndex: number;
}

export interface GenerationBlock {
  generation: number;
  divider?: { from: number; to: number };
  sectionClass: string;
  modelBar: { key: string; model?: string | null; modelsUsed: string[] };
  systemPrompts: { key: string; message: Message }[];
  chunks: RenderChunk[];
}
