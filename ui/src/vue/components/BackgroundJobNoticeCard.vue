<!-- Background job completions are user messages to the model but bash tool
     results to the reader. The same card renders persisted and queued notices,
     including ones recorded before the outcome fields were stored. -->
<template>
  <BashTool
    :tool-input="{ command: source.checkIn?.command ?? outcome?.command ?? '' }"
    :tool-result="[{ ID: '', Type: 2, Text: outcome?.tail ?? text }]"
    :has-error="!!outcome && outcome.exitCode !== 0"
    :return-state="returnState"
  />
</template>

<script setup lang="ts">
import { computed } from "vue";
import {
  type BackgroundJobMessageSource,
  parseLegacyBackgroundJobNotice,
} from "../../utils/messageSource";
import BashTool from "./tools/BashTool.vue";

const props = defineProps<{ source: BackgroundJobMessageSource; text: string }>();
const outcome = computed(
  () =>
    props.source.outcome ??
    parseLegacyBackgroundJobNotice(props.text, props.source.backgroundJobId),
);
const returnState = computed<"finished" | "failed" | "lost" | "running" | "unknown">(() => {
  if (props.source.checkIn) return "running";
  if (!outcome.value) return "unknown";
  if (outcome.value.exitCode === null) return "lost";
  return outcome.value.exitCode === 0 ? "finished" : "failed";
});
</script>
