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

export interface GenerationBlock {
  generation: number;
  divider?: { from: number; to: number };
  sectionClass: string;
  modelBar: { key: string; model?: string | null };
  systemPrompts: { key: string; message: Message }[];
  nodes: RenderNode[];
}
