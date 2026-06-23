<!-- Sub-component of Message.vue (ported from DistillStatusMessage in
     components/Message.tsx). Compact conversation distillation/compaction
     status. Preserves the .message.message-gitinfo.msg-distill-container[.error]
     container and the distill-in-progress / distill-complete / distill-error
     testids and the inline spinner. -->
<template>
  <div :class="containerClass">
    <span v-if="isInProgress" data-testid="distill-in-progress">
      <span class="spinner spinner-small msg-spinner-inline" />
      {{ gerund }} conversation{{ sourceSlug ? ` "${sourceSlug}"` : "" }}…
    </span>
    <span v-if="status === 'complete'" data-testid="distill-complete">
      {{ pastParticiple }} from{{ sourceSlug ? ` "${sourceSlug}"` : " prior conversation" }}
    </span>
    <span v-if="isError" data-testid="distill-error">
      {{ noun }} failed{{ sourceSlug ? ` for "${sourceSlug}"` : "" }}
    </span>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { Message as MessageType } from "../../types";

const props = defineProps<{ message: MessageType }>();

const parsed = computed(() => {
  let status = "in_progress";
  let sourceSlug = "";
  let method = "default";
  if (props.message.user_data) {
    try {
      const userData =
        typeof props.message.user_data === "string"
          ? JSON.parse(props.message.user_data)
          : props.message.user_data;
      status = userData.distill_status || "in_progress";
      sourceSlug = userData.source_slug || "";
      method = userData.distill_method || "default";
    } catch {
      // ignore parse errors
    }
  }
  return { status, sourceSlug, method };
});

const status = computed(() => parsed.value.status);
const sourceSlug = computed(() => parsed.value.sourceSlug);
const isInProgress = computed(() => status.value === "in_progress");
const isError = computed(() => status.value === "error");
const isCompact = computed(() => parsed.value.method === "compact");
// Verb/noun vary by strategy: "compact" vs "distill".
const gerund = computed(() => (isCompact.value ? "Compacting" : "Distilling"));
const pastParticiple = computed(() => (isCompact.value ? "Compacted" : "Distilled"));
const noun = computed(() => (isCompact.value ? "Compaction" : "Distillation"));

const containerClass = computed(() =>
  isError.value
    ? "message message-gitinfo msg-distill-container error"
    : "message message-gitinfo msg-distill-container",
);
</script>
