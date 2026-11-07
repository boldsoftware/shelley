<template>
  <aside
    class="btw-inline"
    data-testid="btw-inline"
    :data-btw-exchange-id="exchange.exchange_id"
    aria-label="BTW side question"
  >
    <div class="btw-inline-connector" aria-hidden="true" />
    <button
      type="button"
      class="btw-inline-label"
      :aria-expanded="!collapsed"
      :aria-label="collapsed ? 'Expand BTW thread' : 'Collapse BTW thread'"
      :aria-controls="bodyID"
      @click="collapsed = !collapsed"
    >
      BTW
      <ToolChevron :expanded="!collapsed" />
    </button>
    <div :id="bodyID" v-show="!collapsed" class="btw-inline-body">
      <section v-for="turn in exchange.turns" :key="turn.id" class="btw-inline-turn">
        <header class="btw-inline-header">
          <div
            class="btw-inline-question msg-container-relative"
            @click="toggleActionBar($event, `question:${turn.id}`)"
            @mouseenter="hoveredActionKey = `question:${turn.id}`"
            @mouseleave="hoveredActionKey = null"
          >
            <MessageActionBar
              v-if="actionBarVisible(`question:${turn.id}`)"
              :on-copy="() => copyText(turn.question)"
            />
            {{ turn.question }}
          </div>
        </header>
        <div v-if="turnIsPending(turn)" class="btw-inline-answer btw-inline-pending" role="status">
          <span class="btw-inline-spinner" aria-hidden="true" /> Working…
        </div>
        <div
          v-else-if="turn.answer"
          class="btw-inline-answer msg-container-relative"
          @click="toggleActionBar($event, `answer:${turn.id}`)"
          @mouseenter="hoveredActionKey = `answer:${turn.id}`"
          @mouseleave="hoveredActionKey = null"
        >
          <MessageActionBar
            v-if="actionBarVisible(`answer:${turn.id}`)"
            :on-copy="() => copyText(turn.answer)"
          />
          <MarkdownContent :text="turn.answer" rewrite-localhost-links />
          <span v-if="turnIsActive(turn)" class="streaming-cursor" aria-label="Streaming">▊</span>
        </div>
        <button
          v-if="turn.tool_call_count"
          type="button"
          class="btw-inline-tool-count"
          v-tooltip.focus.top="btwToolCallTooltip(turn.tool_calls)"
          :aria-label="`${toolCallLabel(turn)}: ${btwToolCallTooltip(turn.tool_calls)}`"
          @click="showToolTooltipOnTouch"
          @mouseenter="showToolTooltipOnHover"
          @mouseleave="hideToolTooltipOnHover"
        >
          {{ toolCallLabel(turn) }}
        </button>
        <div v-if="turn.status === 'failed'" class="btw-inline-error" role="alert">
          {{ turn.error || "BTW request failed" }}
        </div>
        <div v-else-if="turn.status === 'cancelled'" class="btw-inline-cancelled" role="status">
          Cancelled
        </div>
      </section>
      <div v-if="actionError" class="btw-inline-error" role="alert">{{ actionError }}</div>
      <footer class="btw-inline-actions">
        <a
          class="btw-inline-action btw-inline-open-subagent"
          :href="`/c/${exchange.reader_slug || exchange.exchange_id}`"
          @click="openSubagent"
        >
          Open subagent
        </a>
        <button
          v-if="!isActive"
          type="button"
          class="btw-inline-action"
          :disabled="busy"
          @click="dismiss"
        >
          Dismiss
        </button>
        <button
          v-if="isActive"
          type="button"
          class="btw-inline-action"
          :disabled="busy"
          @click="cancel"
        >
          Cancel
        </button>
        <button
          v-if="exchange.status === 'failed' && exchange.retryable"
          type="button"
          class="btw-inline-action"
          :disabled="busy"
          @click="retry"
        >
          Retry
        </button>
        <button
          v-if="canOfferSummary"
          type="button"
          class="btw-inline-action"
          :disabled="busy"
          @click="summarize"
        >
          Summarize to input
        </button>
      </footer>
      <form class="btw-inline-follow-up" @submit.prevent="followUp">
        <textarea
          v-model="followUpQuestion"
          data-btw-follow-up
          rows="1"
          aria-label="Follow up"
          placeholder="Follow up"
          :disabled="isActive || busy"
          @keydown.enter.exact.prevent="followUp"
        />
        <button
          type="submit"
          class="btw-inline-action btw-inline-follow-up-submit"
          aria-label="Submit follow up"
          :disabled="!canFollowUp"
        >
          <svg
            aria-hidden="true"
            width="16"
            height="16"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
            stroke-linecap="round"
            stroke-linejoin="round"
          >
            <line x1="12" y1="19" x2="12" y2="5" />
            <polyline points="5 12 12 5 19 12" />
          </svg>
        </button>
      </form>
    </div>
  </aside>
</template>

<script setup lang="ts">
import { computed, ref, toRef } from "vue";
import type { BtwExchange, BtwTurn } from "../../types";
import { api } from "../../services/api";
import { BTW_INTERRUPTED_ERROR } from "../../services/btwProjector";
import { btwStore } from "../../services/btwStore";
import { messageStore } from "../../services/messageStore";
import { btwToolCallTooltip } from "../../utils/btwToolTally";
import { navigateToConversationSlug } from "../composables/subagentLive";
import MarkdownContent from "./MarkdownContent.vue";
import MessageActionBar from "./MessageActionBar.vue";
import ToolChevron from "./tools/ToolChevron.vue";

const props = defineProps<{ exchange: BtwExchange }>();
const exchange = toRef(props, "exchange");
const busy = ref(false);
const collapsed = ref(false);
const actionError = ref<string | null>(null);
const followUpQuestion = ref("");
const bodyID = `btw-inline-body-${props.exchange.exchange_id}`;
const pinnedActionKey = ref<string | null>(null);
const hoveredActionKey = ref<string | null>(null);

function turnIsActive(turn: BtwTurn): boolean {
  return turn.status === "active" || turn.status === "pending";
}
function turnIsPending(turn: BtwTurn): boolean {
  return turnIsActive(turn) && !turn.answer;
}
function toolCallLabel(turn: BtwTurn): string {
  const count = turn.tool_call_count || 0;
  const noun = count === 1 ? "tool call" : "tool calls";
  if (turnIsActive(turn) && turn.unresolved_tool_call_count)
    return `${count} ${noun} · ${turn.unresolved_tool_call_count} running`;
  return `${count} ${noun}`;
}
function dispatchTooltipFocus(target: EventTarget | null, type: "focus" | "blur") {
  if (target instanceof HTMLElement) target.dispatchEvent(new FocusEvent(type));
}
function showToolTooltipOnHover(event: MouseEvent) {
  dispatchTooltipFocus(event.currentTarget, "focus");
}
function showToolTooltipOnTouch(event: MouseEvent) {
  if (event.currentTarget instanceof HTMLElement) event.currentTarget.focus();
}
function hideToolTooltipOnHover(event: MouseEvent) {
  dispatchTooltipFocus(event.currentTarget, "blur");
}
const isActive = computed(
  () =>
    props.exchange.status === "active" ||
    props.exchange.status === "pending" ||
    props.exchange.turns.some(turnIsActive),
);
const canFollowUp = computed(
  () => !!followUpQuestion.value.trim() && !isActive.value && !busy.value,
);
const canOfferSummary = computed(
  () => props.exchange.status === "completed" && props.exchange.turns.some((turn) => !!turn.answer),
);

function actionBarVisible(key: string): boolean {
  return key === (hoveredActionKey.value ?? pinnedActionKey.value);
}

function toggleActionBar(event: MouseEvent, key: string) {
  const target = event.target as HTMLElement;
  if (target.closest("button, a, [data-action-bar]")) return;
  pinnedActionKey.value = pinnedActionKey.value === key ? null : key;
}

function copyText(text: string) {
  navigator.clipboard.writeText(text).catch((err) => {
    actionError.value = err instanceof Error ? err.message : "Could not copy BTW text";
  });
}

async function mutate(run: () => Promise<void>): Promise<boolean> {
  busy.value = true;
  actionError.value = null;
  try {
    await run();
  } catch (err) {
    actionError.value = err instanceof Error ? err.message : "BTW request failed";
    busy.value = false;
    return false;
  }
  try {
    await btwStore.refreshChild(props.exchange.exchange_id);
  } catch (err) {
    const message = err instanceof Error ? err.message : "refresh failed";
    actionError.value = `BTW updated, but refresh failed: ${message}`;
  } finally {
    busy.value = false;
  }
  return true;
}
function cancel() {
  void mutate(() => api.cancelConversation(props.exchange.exchange_id));
}
async function dismiss() {
  if (busy.value) return;
  busy.value = true;
  actionError.value = null;
  try {
    await btwStore.dismiss(props.exchange);
  } catch (err) {
    actionError.value = err instanceof Error ? err.message : "Failed to dismiss BTW";
  } finally {
    busy.value = false;
  }
}
function retry() {
  const turn = props.exchange.turns.at(-1);
  if (!turn) return;
  messageStore.resetTransient(props.exchange.exchange_id);
  if (turn.kind === "summary" && turn.error === BTW_INTERRUPTED_ERROR) {
    void summarize();
    return;
  }
  void mutate(() =>
    turn.error === BTW_INTERRUPTED_ERROR
      ? api
          .sendMessage(props.exchange.exchange_id, { message: turn.question })
          .then(() => undefined)
      : api.retryConversation(props.exchange.exchange_id),
  );
}
async function followUp() {
  const question = followUpQuestion.value.trim();
  if (!question || !canFollowUp.value) return;
  if (
    await mutate(() =>
      api.sendMessage(props.exchange.exchange_id, { message: question }).then(() => undefined),
    )
  ) {
    followUpQuestion.value = "";
  }
}
function openSubagent(event: MouseEvent) {
  if (
    event.defaultPrevented ||
    event.button !== 0 ||
    event.metaKey ||
    event.ctrlKey ||
    event.shiftKey ||
    event.altKey
  )
    return;
  event.preventDefault();
  navigateToConversationSlug(props.exchange.reader_slug || props.exchange.exchange_id);
}
async function summarize() {
  if (isActive.value || busy.value) return;
  busy.value = true;
  actionError.value = null;
  btwStore.requestSummary(props.exchange);
  try {
    const receipt = await api.summarizeBtwExchange(
      props.exchange.parent_conversation_id,
      props.exchange.exchange_id,
    );
    btwStore.resolveSummary(props.exchange, receipt.summary_id);
    await btwStore.refreshChild(props.exchange.exchange_id);
  } catch (err) {
    btwStore.resolveSummary(props.exchange);
    try {
      await btwStore.refreshChild(props.exchange.exchange_id);
    } catch {
      // The original failure remains the useful action error.
    }
    actionError.value = err instanceof Error ? err.message : "Failed to summarize BTW";
  } finally {
    busy.value = false;
  }
}
</script>
