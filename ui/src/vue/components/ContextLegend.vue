<!-- Legend for the context graph: what each color is, and how many tokens it
     holds at one call (the hovered one, else the latest). Hovering or
     focusing a row names what the category is made of. -->
<template>
  <div class="context-legend" role="list" aria-label="Estimated context mix">
    <div
      v-for="category in categories"
      :key="category.key"
      class="context-legend-item"
      role="listitem"
    >
      <button
        type="button"
        class="token-cost-legend-row context-legend-row"
        v-tooltip.focus.top="categoryHint(category.key, point)"
        :aria-label="`${category.label}: ${formatTokenCount(categoryTokens(point, category.key))}${rewarmLabel(category.key)}. ${categoryHint(category.key, point)}`"
        @mouseenter="showTooltipOnHover"
        @mouseleave="hideTooltipOnHover"
      >
        <span class="token-cost-chip" :style="{ background: category.color }" aria-hidden="true" />
        <span class="token-cost-legend-label context-legend-label">{{ category.label }}</span>
        <span class="token-cost-legend-tokens context-legend-tokens">{{
          formatTokenCount(categoryTokens(point, category.key))
        }}</span>
        <span v-if="rewarm(category.key) !== null" class="context-legend-rewarm">{{
          formatUsd(rewarm(category.key)!)
        }}</span>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { formatTokenCount, formatUsd, type ModelCost } from "../../utils/tokenCostGraph";
import { categoryHint, categoryTokens, rewarmUsd, type ContextCategory } from "./contextCategories";
import type { Point } from "./contextComposition";

const props = defineProps<{
  categories: ContextCategory[];
  point: Point;
  /** Pricing by model; with it, each row shows its cache-write (rewarm) cost. */
  costs?: Record<string, ModelCost | null | undefined>;
}>();

function rewarm(key: string): number | null {
  return props.costs ? rewarmUsd(props.point, props.costs, key) : null;
}

function rewarmLabel(key: string): string {
  const usd = rewarm(key);
  return usd === null ? "" : `, ${formatUsd(usd)} to rewarm`;
}

// The tooltip directive opens on focus; hover borrows that path.
function dispatchTooltipFocus(target: EventTarget | null, type: "focus" | "blur") {
  if (target instanceof HTMLElement) target.dispatchEvent(new FocusEvent(type));
}

function showTooltipOnHover(event: MouseEvent) {
  dispatchTooltipFocus(event.currentTarget, "focus");
}

function hideTooltipOnHover(event: MouseEvent) {
  dispatchTooltipFocus(event.currentTarget, "blur");
}
</script>
