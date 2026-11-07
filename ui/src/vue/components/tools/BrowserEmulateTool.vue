<!-- browser emulate_* actions: the device, size and media in the header and
     the details. -->
<template>
  <ToolCard
    emoji="📱"
    :complete="complete"
    :is-running="isRunning"
    :tool-invoked-at="toolInvokedAt"
  >
    <template #summary>
      <span class="tool-command">{{ summary }}</span>
    </template>
    <ToolSection label="Action" :text="input.action || '(none)'" />
    <ToolSection v-if="input.device" label="Device" :text="input.device" />
    <ToolSection
      v-if="input.width !== undefined && input.height !== undefined"
      label="Dimensions"
      :text="`${input.width} × ${input.height}`"
    />
    <ToolSection v-if="input.media" label="Media" :text="input.media" />
    <ToolSection
      v-if="complete && output"
      label="Output"
      :text="output"
      :has-error="hasError"
      :execution-time="executionTime"
    />
  </ToolCard>
</template>

<script setup lang="ts">
import { computed } from "vue";
import ToolCard from "./ToolCard.vue";
import ToolSection from "./ToolSection.vue";
import { firstResultText, inputFields, toolComplete, type ToolProps } from "./toolProps";

const props = defineProps<ToolProps>();

const input = computed(() =>
  inputFields<{
    action: string;
    device: string;
    width: number;
    height: number;
    enabled: boolean;
    media: string;
  }>(props.toolInput),
);
const output = computed(() => firstResultText(props.toolResult));
const complete = computed(() => toolComplete(props));

const summary = computed(() => {
  const i = input.value;
  const summaryParts: string[] = [i.action ?? ""];
  if (i.device) summaryParts.push(i.device);
  if (i.width && i.height) summaryParts.push(`${i.width}×${i.height}`);
  if (i.media) summaryParts.push(i.media);
  if (i.enabled !== undefined) summaryParts.push(i.enabled ? "on" : "off");
  return summaryParts.filter(Boolean).join(" ") || "emulate";
});
</script>
