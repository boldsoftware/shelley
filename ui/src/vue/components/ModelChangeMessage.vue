<!-- Sub-component of Message.vue. Renders a "modelchange" marker: recorded
     when a conversation's settings change (the model picker, /model, a profile
     switch, the tools modal, the Compact in Place button), or the /model
     command's informational output. User-visible only; never sent to the LLM.

     A settings change gets chips for what changed: the profile, old → new
     model, reasoning, tools turned on (+) and off (−), the compaction nudge,
     and the system prompt. Purely informational output (bare /model status,
     errors, "already using") falls back to the plain text notice. -->
<template>
  <div
    v-if="isSwitch"
    class="message message-gitinfo msg-modelchange-container msg-modelchange-switch"
    data-testid="message-modelchange"
    role="status"
    :aria-label="text"
  >
    <span class="msg-modelchange-icon">🤖</span>
    <span v-if="data.profile_to" class="msg-modelchange-group">
      <span class="msg-modelchange-label">profile</span>
      <span class="msg-modelchange-chip msg-modelchange-to">{{ data.profile_to }}</span>
    </span>
    <span v-if="modelChanged" class="msg-modelchange-group">
      <span class="msg-modelchange-chip msg-modelchange-from">{{ fromName }}</span>
      <span class="msg-modelchange-arrow" aria-hidden="true">→</span>
      <span class="msg-modelchange-chip msg-modelchange-to">{{ toName }}</span>
    </span>
    <span v-if="reasoningChanged" class="msg-modelchange-group">
      <span class="msg-modelchange-label">reasoning</span>
      <span class="msg-modelchange-chip">{{ reasoningTo }}</span>
    </span>
    <span v-if="tools.length" class="msg-modelchange-group">
      <span class="msg-modelchange-label">tools</span>
      <span
        v-for="tool in tools"
        :key="tool.name"
        :class="`msg-modelchange-chip msg-modelchange-tool-${tool.on ? 'on' : 'off'}`"
        >{{ tool.on ? "+" : "−" }}{{ tool.name }}</span
      >
    </span>
    <span v-if="data.compact_nudge_tokens" class="msg-modelchange-group">
      <span class="msg-modelchange-label">compaction nudge</span>
      <span class="msg-modelchange-chip">{{ data.compact_nudge_tokens / 1000 }}k</span>
    </span>
    <span v-if="data.system_prompt_changed" class="msg-modelchange-chip">new system prompt</span>
  </div>
  <div
    v-else
    class="message message-gitinfo msg-modelchange-container"
    data-testid="message-modelchange"
    role="status"
  >
    <span class="msg-modelchange-icon">🤖</span>
    <span class="msg-modelchange-text">{{ text }}</span>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { Message as MessageType } from "../../types";
import { prettyModelName } from "../../utils/modelNames";

const props = defineProps<{ message: MessageType }>();

interface ModelChangeData {
  from?: string;
  to?: string;
  from_display?: string;
  to_display?: string;
  reasoning_to?: string;
  profile_to?: string;
  tools_on?: string[];
  tools_off?: string[];
  compact_nudge_tokens?: number;
  system_prompt_changed?: boolean;
  text?: string;
}

const data = computed<ModelChangeData>(() => {
  if (!props.message.user_data) return {};
  try {
    return typeof props.message.user_data === "string"
      ? JSON.parse(props.message.user_data)
      : (props.message.user_data as ModelChangeData);
  } catch {
    return {};
  }
});

const text = computed(() => data.value.text || "Model changed");

// A model switch records a non-empty `to`; a reasoning change records
// reasoning_to; and so on. Informational markers (bare /model, errors) have
// none of them.
const modelChanged = computed(() => !!data.value.to);
const reasoningChanged = computed(() => !!data.value.reasoning_to);
const tools = computed(() => [
  ...(data.value.tools_on || []).map((name) => ({ name, on: true })),
  ...(data.value.tools_off || []).map((name) => ({ name, on: false })),
]);
const isSwitch = computed(
  () =>
    modelChanged.value ||
    reasoningChanged.value ||
    !!data.value.profile_to ||
    tools.value.length > 0 ||
    !!data.value.compact_nudge_tokens ||
    !!data.value.system_prompt_changed,
);

// The server's display name is the id unless the model was given a name;
// prettify ids the way the model picker does.
function modelName(id = "", display = ""): string {
  return display && display !== id ? display : prettyModelName(id);
}
const fromName = computed(() => modelName(data.value.from, data.value.from_display));
const toName = computed(() => modelName(data.value.to, data.value.to_display));
const reasoningTo = computed(() => data.value.reasoning_to || "");
</script>
