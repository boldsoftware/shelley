// Shared thinking-level constants/types. These were exported from
// ThinkingLevelPicker.tsx in React, but the esbuild Vue SFC plugin does not
// surface plain-<script> exports from a .vue with a <script setup> block, so
// the values live in this .ts module and are imported by both
// ThinkingLevelPicker.vue and ChatInterface.vue.
export type ThinkingLevel = "off" | "minimal" | "low" | "medium" | "high" | "xhigh";

export const DEFAULT_THINKING_LEVEL: ThinkingLevel = "medium";

export const THINKING_LEVELS: { value: ThinkingLevel; label: string }[] = [
  { value: "off", label: "off" },
  { value: "minimal", label: "minimal" },
  { value: "low", label: "low" },
  { value: "medium", label: "medium" },
  { value: "high", label: "high" },
  { value: "xhigh", label: "xhigh" },
];
