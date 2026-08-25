<!-- Stacked cumulative token-cost graph shown in the context usage popup.
     X axis: LLM calls or wall-clock time (toggle); in time mode, idle time
     between turns is collapsed into fixed-width gaps. Y axis: cumulative
     dollars per (model, token-band) segment — every segment has its own color
     — falling back to raw token counts when no model in the conversation has
     known pricing.

     Subagent cost and "other" (indirect) LLM usage — compaction
     summarization, LLM-backed tools, slug generation, … — are not part of the
     graph. Other-usage rows arrive via the otherUsageRows prop (aggregated
     client-side from message other_usage_data) and subagent cost is fetched
     separately; subagents appear as another breakdown row and both are
     included in the total below the table. -->
<template>
  <div class="token-cost-graph">
    <div v-if="loading" class="token-cost-graph-note">Loading pricing…</div>
    <template v-else-if="stack && stack.n > 0">
      <div class="token-cost-controls">
        <button
          :class="{ 'token-cost-toggle-active': xMode === 'calls' }"
          class="token-cost-toggle"
          @click="xMode = 'calls'"
        >
          calls
        </button>
        <button
          :class="{ 'token-cost-toggle-active': xMode === 'time' }"
          class="token-cost-toggle"
          @click="xMode = 'time'"
        >
          time
        </button>
      </div>
      <svg
        :viewBox="`0 0 ${W} ${H}`"
        class="token-cost-graph-svg"
        @mousemove="onMove"
        @mouseleave="hoverIndex = null"
      >
        <line
          v-for="t in ticks"
          :key="`tick-${t}`"
          :x1="PADL"
          :y1="yAt(t)"
          :x2="W - PADR"
          :y2="yAt(t)"
          class="token-cost-gridline"
        />
        <path v-for="(d, s) in segPaths" :key="s" :d="d" :fill="stack.segments[s].color" />
        <line
          v-for="i in genStarts"
          :key="`gen-${i}`"
          :x1="xAt(i)"
          :y1="PADT"
          :x2="xAt(i)"
          :y2="H - PADB"
          class="token-cost-gen-line"
        />
        <line :x1="PADL" :y1="PADT" :x2="PADL" :y2="H - PADB" class="token-cost-axis" />
        <line :x1="PADL" :y1="H - PADB" :x2="W - PADR" :y2="H - PADB" class="token-cost-axis" />
        <line
          v-if="hoverIndex !== null && stack.n > 1"
          :x1="xAt(hoverIndex)"
          :y1="PADT"
          :x2="xAt(hoverIndex)"
          :y2="H - PADB"
          class="token-cost-hover-line"
        />
        <text
          v-for="t in ticks"
          :key="`ticklabel-${t}`"
          :x="PADL - 3"
          :y="yAt(t) + 3"
          text-anchor="end"
          class="token-cost-label"
        >
          {{ tickLabel(t) }}
        </text>
        <text :x="(PADL + W - PADR) / 2" :y="H - 4" text-anchor="middle" class="token-cost-label">
          {{ xAxisLabel }}
        </text>
      </svg>
      <div class="token-cost-hover-readout">
        <template v-if="hoverEntry">
          <div>
            call {{ hoverIndex! + 1 }} of {{ stack.n
            }}<template v-if="hoverGeneration"> (gen {{ hoverGeneration }})</template
            ><template v-if="hoverTime"> · {{ hoverTime }}</template> · cumulative {{ hoverTotal }}
          </div>
          <div v-if="hoverEntry.snippet" class="token-cost-hover-snippet">
            {{ hoverEntry.snippet }}
          </div>
        </template>
        <template v-else>
          <div>{{ hintText }}</div>
        </template>
      </div>
      <div class="token-cost-legend">
        <template v-for="mu in stack.perModel" :key="mu.model">
          <div class="token-cost-model-row">
            <span class="token-cost-model-name">{{ mu.model }}</span>
            <span v-if="mu.priced" class="token-cost-legend-cost">{{
              formatUsd(mu.totalCost)
            }}</span>
            <span v-else-if="mu.reportedUsd > 0" class="token-cost-legend-cost">
              {{ formatUsd(mu.reportedUsd) }} reported
            </span>
            <span v-else class="token-cost-legend-unit">no pricing</span>
          </div>
          <div v-for="t in rowsFor(mu)" :key="t.band.key" class="token-cost-legend-row">
            <span class="token-cost-chip" :style="{ backgroundColor: t.color }" />
            <span class="token-cost-legend-label">{{ t.band.label }}</span>
            <span class="token-cost-legend-tokens">{{ formatTokenCount(t.tokens) }}</span>
            <span v-if="mu.priced" class="token-cost-legend-unit">
              @ {{ formatUnitPrice(t.unitUsdPerMtok) }}
            </span>
            <span v-if="mu.priced" class="token-cost-legend-cost">{{ formatUsd(t.cost) }}</span>
          </div>
        </template>
        <template v-if="otherBreakdown && otherBreakdown.perPurpose.length > 0">
          <div class="token-cost-model-row">
            <span class="token-cost-model-name">Other (indirect)</span>
            <span v-if="otherKnownUsd > 0" class="token-cost-legend-cost">{{
              formatUsd(otherKnownUsd)
            }}</span>
            <span
              v-else-if="otherBreakdown.totals.unpricedCalls === 0"
              class="token-cost-legend-cost"
              >{{ formatUsd(0) }}</span
            >
            <span v-else class="token-cost-legend-unit">no pricing</span>
          </div>
          <div
            v-for="p in otherBreakdown.perPurpose"
            :key="p.purpose"
            class="token-cost-legend-row"
          >
            <span class="token-cost-legend-label">{{ p.purpose }}</span>
            <span class="token-cost-legend-tokens"
              >{{ p.llmCalls }} {{ p.llmCalls === 1 ? "call" : "calls" }}</span
            >
            <span class="token-cost-legend-tokens">{{ formatTokenCount(p.tokens) }}</span>
            <span v-if="otherPurposeKnownUsd(p) > 0" class="token-cost-legend-cost">{{
              formatUsd(otherPurposeKnownUsd(p))
            }}</span>
            <span v-else-if="p.priced" class="token-cost-legend-cost">{{ formatUsd(0) }}</span>
            <span v-else class="token-cost-legend-unit">no pricing</span>
          </div>
        </template>
        <template v-if="subagentUsage && subagentUsage.llm_calls > 0">
          <div class="token-cost-model-row" data-testid="subagent-cost-row">
            <span class="token-cost-model-name">Subagents</span>
            <span v-if="subagentKnownUsd > 0" class="token-cost-legend-cost">{{
              formatUsd(subagentKnownUsd)
            }}</span>
            <span v-else-if="subagentUsage.unpriced_calls === 0" class="token-cost-legend-cost">{{
              formatUsd(0)
            }}</span>
            <span v-else class="token-cost-legend-unit">no pricing</span>
          </div>
        </template>
        <div
          v-if="!subagentLoading && showCostSummary"
          class="token-cost-model-row token-cost-total-row"
          data-testid="token-cost-total"
        >
          <span class="token-cost-model-name">Total</span>
          <span class="token-cost-legend-cost">≈{{ formatUsd(costSummary.totalUsd) }}</span>
        </div>
      </div>
      <div v-if="stack.weighted && fetchFailed" class="token-cost-graph-note">
        Pricing lookup failed for some models.
      </div>
      <div v-if="stack.weighted && stack.reportedCostUsd > 0" class="token-cost-graph-note">
        Provider-reported direct cost: {{ formatUsd(stack.reportedCostUsd) }}.
      </div>
      <div v-if="!stack.weighted" class="token-cost-graph-note">
        <template v-if="fetchFailed"> Pricing lookup failed — showing raw token counts. </template>
        <template v-else
          >Raw token counts — no pricing known.<template v-if="stack.reportedCostUsd > 0">
            Provider-reported {{ formatUsd(stack.reportedCostUsd) }}.</template
          ></template
        >
      </div>
    </template>
    <div v-else-if="!loading" class="token-cost-graph-note">No direct usage data yet.</div>
    <div
      v-if="
        !loading &&
        !stack &&
        ((otherBreakdown && otherBreakdown.perPurpose.length > 0) ||
          (subagentUsage && subagentUsage.llm_calls > 0))
      "
      class="token-cost-legend"
    >
      <template v-if="otherBreakdown && otherBreakdown.perPurpose.length > 0">
        <div class="token-cost-model-row">
          <span class="token-cost-model-name">Other (indirect)</span>
          <span v-if="otherKnownUsd > 0" class="token-cost-legend-cost">{{
            formatUsd(otherKnownUsd)
          }}</span>
          <span
            v-else-if="otherBreakdown.totals.unpricedCalls === 0"
            class="token-cost-legend-cost"
            >{{ formatUsd(0) }}</span
          >
          <span v-else class="token-cost-legend-unit">no pricing</span>
        </div>
        <div v-for="p in otherBreakdown.perPurpose" :key="p.purpose" class="token-cost-legend-row">
          <span class="token-cost-legend-label">{{ p.purpose }}</span>
          <span class="token-cost-legend-tokens"
            >{{ p.llmCalls }} {{ p.llmCalls === 1 ? "call" : "calls" }}</span
          >
          <span class="token-cost-legend-tokens">{{ formatTokenCount(p.tokens) }}</span>
          <span v-if="otherPurposeKnownUsd(p) > 0" class="token-cost-legend-cost">{{
            formatUsd(otherPurposeKnownUsd(p))
          }}</span>
          <span v-else-if="p.priced" class="token-cost-legend-cost">{{ formatUsd(0) }}</span>
          <span v-else class="token-cost-legend-unit">no pricing</span>
        </div>
      </template>
      <div
        v-if="subagentUsage && subagentUsage.llm_calls > 0"
        class="token-cost-model-row"
        data-testid="subagent-cost-row"
      >
        <span class="token-cost-model-name">Subagents</span>
        <span v-if="subagentKnownUsd > 0" class="token-cost-legend-cost">{{
          formatUsd(subagentKnownUsd)
        }}</span>
        <span v-else-if="subagentUsage.unpriced_calls === 0" class="token-cost-legend-cost">{{
          formatUsd(0)
        }}</span>
        <span v-else class="token-cost-legend-unit">no pricing</span>
      </div>
      <div
        v-if="!subagentLoading && showCostSummary"
        class="token-cost-model-row token-cost-total-row"
        data-testid="token-cost-total"
      >
        <span class="token-cost-model-name">Total</span>
        <span class="token-cost-legend-cost">≈{{ formatUsd(costSummary.totalUsd) }}</span>
      </div>
    </div>
    <div v-if="!loading && subagentLoading" class="token-cost-graph-note">
      Loading total including subagents…
    </div>
    <div
      v-if="!loading && showCostSummary && costSummary.unpricedCalls > 0"
      class="token-cost-graph-note token-cost-graph-warning"
    >
      {{ costSummary.unpricedCalls }}
      {{ costSummary.unpricedCalls === 1 ? "call has" : "calls have" }} no model pricing; provider
      reports are included when available, but the total may be incomplete.
    </div>
    <div
      v-if="!loading && subagentFetchFailed"
      class="token-cost-graph-note token-cost-graph-warning"
    >
      Subagent cost unavailable; total may be incomplete.
    </div>
    <div v-if="!loading && !stack && fetchFailed" class="token-cost-graph-note">
      Pricing lookup failed.
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import {
  modelCostsApi,
  subagentUsageApi,
  type ModelCostDTO,
  type SubagentUsageDTO,
} from "../../services/api";
import {
  buildCostSummary,
  buildOtherUsageBreakdown,
  buildTokenCostStack,
  callXLayout,
  formatDuration,
  formatTokenCount,
  formatUsd,
  generationStarts,
  timeXLayout,
  yTicks,
  type ModelUsage,
  type OtherPurposeUsage,
  type OtherUsageBreakdown,
  type OtherUsageRow,
  type TokenCostStack,
  type UsageEntry,
  type XLayout,
} from "../../utils/tokenCostGraph";

const props = defineProps<{
  entries: UsageEntry[];
  otherUsageRows?: OtherUsageRow[];
  conversationId?: string | null;
  active?: boolean;
}>();

const W = 280;
const H = 150;
const PADL = 32;
const PADR = 6;
const PADT = 6;
const PADB = 18;

const loading = ref(false);
const fetchFailed = ref(false);
const costs = ref<Record<string, ModelCostDTO | null>>({});

// "Other" (indirect) LLM usage — compaction, LLM-backed tools, slug
// generation, … — arrives pre-aggregated via the otherUsageRows prop. It has
// no per-call timeline, so it stays out of the graph and only appears in the
// breakdown and the note line; its models join the pricing batch below.

// (model, url) pairs to price: the graph's entries plus other-usage models,
// deduped by model name (first-seen URL wins).
const distinctModels = computed(() => {
  const seen = new Map<string, string>();
  for (const e of props.entries) {
    if (e.model && !seen.has(e.model)) seen.set(e.model, e.url || "");
  }
  for (const row of props.otherUsageRows ?? []) {
    if (row.model && !seen.has(row.model)) seen.set(row.model, row.url || "");
  }
  return seen;
});

let pricingRequest = 0;
watch(
  distinctModels,
  async (models) => {
    const request = ++pricingRequest;
    if (models.size === 0) {
      costs.value = {};
      fetchFailed.value = false;
      loading.value = false;
      return;
    }
    loading.value = Object.keys(costs.value).length === 0;
    try {
      const nextCosts = await modelCostsApi.lookup(
        Array.from(models).map(([model, url]) => ({ model, url })),
      );
      if (request !== pricingRequest) return;
      costs.value = nextCosts;
      fetchFailed.value = false;
    } catch (e) {
      if (request !== pricingRequest) return;
      console.warn("model costs lookup failed", e);
      fetchFailed.value = true;
    } finally {
      if (request === pricingRequest) loading.value = false;
    }
  },
  { immediate: true },
);

const stack = computed<TokenCostStack | null>(() =>
  props.entries.length > 0 ? buildTokenCostStack(props.entries, costs.value) : null,
);

// Subagent usage is aggregated server-side (a recursive query over descendant
// conversations) and shown as a separate subtotal, not in the graph. Refresh
// on parent activity and while the popup is open so a running child's spend
// does not leave the emphasized total stale.
const subagentUsage = ref<SubagentUsageDTO | null>(null);
const subagentLoading = ref(false);
const subagentFetchFailed = ref(false);
let subagentRequest = 0;
let subagentAbort: AbortController | null = null;

function cancelSubagentUsage(): void {
  subagentAbort?.abort();
  subagentAbort = null;
  subagentLoading.value = false;
}

async function loadSubagentUsage(showLoading: boolean): Promise<void> {
  const id = props.conversationId;
  if (!id) {
    cancelSubagentUsage();
    subagentUsage.value = null;
    return;
  }
  // Polls never stack. Conversation changes cancel explicitly before loading.
  if (subagentAbort) return;
  const controller = new AbortController();
  subagentAbort = controller;
  const request = ++subagentRequest;
  if (showLoading) subagentLoading.value = true;
  try {
    const usage = await subagentUsageApi.get(id, controller.signal);
    if (request !== subagentRequest) return;
    subagentUsage.value = usage;
    subagentFetchFailed.value = false;
  } catch (e) {
    if ((e as Error).name === "AbortError") return;
    console.warn("subagent usage lookup failed", e);
    if (request === subagentRequest) subagentFetchFailed.value = true;
  } finally {
    if (subagentAbort === controller) subagentAbort = null;
    if (request === subagentRequest) subagentLoading.value = false;
  }
}

watch(
  () => props.conversationId,
  () => {
    cancelSubagentUsage();
    subagentFetchFailed.value = false;
    subagentUsage.value = null;
    void loadSubagentUsage(true);
  },
  { immediate: true },
);

watch(
  () =>
    [
      props.entries.length,
      (props.otherUsageRows ?? []).reduce((sum, row) => sum + row.llm_calls, 0),
    ] as const,
  () => {
    if (subagentUsage.value) void loadSubagentUsage(false);
  },
);

const SUBAGENT_REFRESH_MS = 5_000;
let subagentRefreshTimer: number | null = null;
function stopSubagentRefresh(): void {
  if (subagentRefreshTimer === null) return;
  window.clearInterval(subagentRefreshTimer);
  subagentRefreshTimer = null;
}
watch(
  () => props.active,
  (active) => {
    stopSubagentRefresh();
    if (!active) {
      cancelSubagentUsage();
      return;
    }
    if (!subagentLoading.value) void loadSubagentUsage(subagentUsage.value === null);
    subagentRefreshTimer = window.setInterval(
      () => void loadSubagentUsage(false),
      SUBAGENT_REFRESH_MS,
    );
  },
  { immediate: true },
);
onBeforeUnmount(() => {
  stopSubagentRefresh();
  cancelSubagentUsage();
});

const subagentKnownUsd = computed(() => {
  const sub = subagentUsage.value;
  return sub ? sub.estimated_usd + sub.unpriced_reported_usd : 0;
});

const otherBreakdown = computed<OtherUsageBreakdown | null>(() => {
  const rows = props.otherUsageRows;
  if (!rows || rows.length === 0) return null;
  return buildOtherUsageBreakdown(rows, costs.value);
});

const otherKnownUsd = computed(() => {
  const totals = otherBreakdown.value?.totals;
  return totals ? totals.estimatedUsd + totals.reportedUnpricedUsd : 0;
});

function otherPurposeKnownUsd(purpose: OtherPurposeUsage): number {
  return purpose.estimatedUsd + purpose.reportedUnpricedUsd;
}

const costSummary = computed(() => {
  const s = stack.value;
  const other = otherBreakdown.value?.totals;
  const subagents = subagentUsage.value;
  const conversationUnpricedCalls = s
    ? props.entries.filter((entry) => !entry.model || !costs.value[entry.model]).length
    : 0;
  const conversationUnpricedReportedUsd = s
    ? s.perModel.filter((model) => !model.priced).reduce((sum, model) => sum + model.reportedUsd, 0)
    : 0;
  const conversationEstimatedUsd = s?.weighted ? s.maxY : 0;
  return buildCostSummary(conversationEstimatedUsd + conversationUnpricedReportedUsd, {
    conversationUnpricedCalls,
    other: other
      ? {
          estimatedUsd: other.estimatedUsd,
          reportedUnpricedUsd: other.reportedUnpricedUsd,
          unpricedCalls: other.unpricedCalls,
        }
      : undefined,
    subagents: subagents
      ? {
          estimatedUsd: subagents.estimated_usd,
          reportedUnpricedUsd: subagents.unpriced_reported_usd,
          unpricedCalls: subagents.unpriced_calls,
        }
      : undefined,
  });
});

const showCostSummary = computed(() => !!stack.value?.weighted || costSummary.value.totalUsd > 0);

const plotW = W - PADL - PADR;
const plotH = H - PADT - PADB;

const xMode = ref<"calls" | "time">("calls");

const layout = computed<XLayout>(() =>
  xMode.value === "time" ? timeXLayout(props.entries) : callXLayout(stack.value?.n ?? 0),
);

function xAt(i: number): number {
  return PADL + layout.value.xs[i] * plotW;
}

function yAt(v: number): number {
  const maxY = stack.value?.maxY || 1;
  return PADT + plotH * (1 - v / maxY);
}

// One path per (model, band) segment; each turn is a separate subpath so
// idle time between turns renders as a gap in time mode. Zero-width turns
// (single call, or all calls sharing one second-granularity timestamp)
// become narrow slabs so they stay visible.
const segPaths = computed<string[]>(() => {
  const s = stack.value;
  const lay = layout.value;
  if (!s || s.n === 0 || s.maxY === 0) return [];
  const px = (i: number) => (PADL + lay.xs[i] * plotW).toFixed(1);
  const lower = (si: number, i: number) => (si === 0 ? 0 : s.layers[si - 1][i]);
  return s.segments.map((_, si) => {
    let d = "";
    for (const [a, b] of lay.turns) {
      if (lay.xs[a] === lay.xs[b]) {
        const x = PADL + lay.xs[a] * plotW;
        const hw = Math.max(1, plotW * 0.006);
        const x0 = Math.max(PADL, x - hw).toFixed(1);
        const x1 = Math.min(W - PADR, x + hw).toFixed(1);
        const yT = yAt(s.layers[si][b]).toFixed(1);
        const yB = yAt(lower(si, b)).toFixed(1);
        d += `M${x0},${yT}L${x1},${yT}L${x1},${yB}L${x0},${yB}Z`;
        continue;
      }
      const top: string[] = [];
      for (let i = a; i <= b; i++) top.push(`${px(i)},${yAt(s.layers[si][i]).toFixed(1)}`);
      const bottom: string[] = [];
      for (let i = b; i >= a; i--) bottom.push(`${px(i)},${yAt(lower(si, i)).toFixed(1)}`);
      d += `M${top.join("L")}L${bottom.join("L")}Z`;
    }
    return d;
  });
});

const ticks = computed<number[]>(() => yTicks(stack.value?.maxY ?? 0));

function tickLabel(v: number): string {
  if (!stack.value?.weighted) return formatTokenCount(v);
  if (v >= 10) return `$${Math.round(v)}`;
  if (v >= 1) return `$${parseFloat(v.toFixed(1))}`;
  if (v >= 0.01) return `$${parseFloat(v.toFixed(2))}`;
  return formatUsd(v);
}

const xAxisLabel = computed(() => {
  const s = stack.value;
  if (!s) return "";
  if (xMode.value === "calls") return `LLM call number (${s.n} calls)`;
  const lay = layout.value;
  const dur = lay.activeMs > 0 ? `${formatDuration(lay.activeMs)} active` : "time";
  return lay.turns.length > 1 ? `${dur} · gaps = idle between turns` : dur;
});

const hintText = computed(() => {
  const parts: string[] = [];
  if (genStarts.value.length) parts.push("Dashed lines mark new generations (compactions).");
  if (xMode.value === "time" && layout.value.turns.length > 1)
    parts.push("Idle time between turns is not to scale.");
  return parts.join(" ");
});

// Rows top-to-bottom mirror the band stacking order within the model.
function rowsFor(mu: ModelUsage) {
  return [...mu.rows].reverse();
}

/** "$50/M", "$12.50/M", "$0.008/M" — unit price per million tokens. */
function formatUnitPrice(usdPerMtok: number): string {
  let s: string;
  if (Number.isInteger(usdPerMtok)) s = String(usdPerMtok);
  else if (usdPerMtok >= 0.1) s = usdPerMtok.toFixed(2);
  else s = usdPerMtok.toPrecision(2).replace(/\.?0+$/, "");
  return `$${s}/M`;
}

const hoverIndex = ref<number | null>(null);

// A shrinking entries list (e.g. switching conversations) could leave a stale
// out-of-range index behind.
watch(
  () => stack.value?.n,
  () => (hoverIndex.value = null),
);

function onMove(ev: MouseEvent) {
  const s = stack.value;
  if (!s || s.n === 0) return;
  const svg = ev.currentTarget as SVGSVGElement;
  const rect = svg.getBoundingClientRect();
  const px = ((ev.clientX - rect.left) / rect.width) * W;
  const frac = (px - PADL) / plotW;
  const xs = layout.value.xs;
  let best = 0;
  let bestD = Infinity;
  for (let i = 0; i < xs.length; i++) {
    const d = Math.abs(xs[i] - frac);
    if (d < bestD) {
      bestD = d;
      best = i;
    }
  }
  hoverIndex.value = best;
}

const hoverEntry = computed<UsageEntry | null>(() => {
  const s = stack.value;
  const i = hoverIndex.value;
  if (!s || i === null || i >= s.n) return null;
  return props.entries[i];
});

const genStarts = computed(() => generationStarts(props.entries));

// Generation number shown in the hover readout, only when the conversation
// actually spans multiple generations.
const hoverGeneration = computed<number | null>(() => {
  if (genStarts.value.length === 0) return null;
  return hoverEntry.value?.generation ?? null;
});

// Wall-clock time of the hovered call, shown in time mode.
const hoverTime = computed(() => {
  const ts = hoverEntry.value?.timestamp;
  if (xMode.value !== "time" || !ts) return "";
  return new Date(ts).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
});

const hoverTotal = computed(() => {
  const s = stack.value;
  const i = hoverIndex.value;
  if (!s || i === null || i >= s.n || s.segments.length === 0) return "";
  const top = s.layers[s.segments.length - 1][i];
  return s.weighted ? formatUsd(top) : `${formatTokenCount(top)} tok`;
});
</script>
