<!-- write_secret (claudetool/secret.go): a secret, as a pill, written to a
     file. The value is never in the input, the result or the display. -->
<template>
  <ToolCard emoji="🔑" :complete="complete" :is-running="isRunning" :tool-invoked-at="toolInvokedAt">
    <template #summary>
      <span class="tool-command">
        <SecretPill v-if="input.id" :id="input.id" /> → {{ target }}
      </span>
    </template>
    <ToolSection
      v-if="complete"
      label="Result"
      :text="result || '(no output)'"
      :has-error="hasError"
      :execution-time="executionTime"
    />
  </ToolCard>
</template>

<script setup lang="ts">
import { computed } from "vue";
import SecretPill from "../SecretPill.vue";
import ToolCard from "./ToolCard.vue";
import ToolSection from "./ToolSection.vue";
import { inputFields, resultText, toolComplete, type ToolProps } from "./toolProps";

const props = defineProps<ToolProps>();

const input = computed(() =>
  inputFields<{ id: string; path: string; ssh_target: string }>(props.toolInput),
);
const shown = computed(() => (props.display ?? null) as { machine?: string; path?: string } | null);
// Once written, the display's machine and absolute path; before, the input's.
const target = computed(() => {
  if (shown.value?.path) return `${shown.value.machine}:${shown.value.path}`;
  return `${input.value.ssh_target ? `${input.value.ssh_target}:` : ""}${input.value.path ?? "..."}`;
});
const result = computed(() => resultText(props.toolResult));
const complete = computed(() => toolComplete(props));
</script>
