<!-- The text of a tool pill: while the tool is running and reporting output,
     the last line of that output; otherwise the headline inferred from the
     tool's input.

     The two are mutually exclusive by design. Showing both spends the pill's
     width on the weaker of the two: the headline is guessed from command
     tokens, so for anything that isn't a simple `prog arg` invocation it is
     noise ("for 20 — %s\n" for a shell loop around printf), while the output
     is the direct answer to "what is this doing right now".

     Owning both halves in one component (rather than a v-if in ToolPillsRow)
     keeps a progress event re-rendering only the pill whose output grew — see
     composables/toolProgress.ts for why that matters.

     The output is aria-hidden, like the sibling spinner: the pill is a button
     whose accessible name comes from its aria-label, and that name should stay
     the durable headline rather than churn with output on every stream tick
     (~2x/sec). The tradeoff is that while running, the visible text and the
     accessible name no longer share any words; the hover title repeats the
     headline to compensate, and a screen reader gets the output itself from
     the tool's detail view, which the pill opens. -->
<template>
  <span v-if="output" class="tool-pill-output" aria-hidden="true" :title="outputTitle">{{
    output
  }}</span>
  <span v-else class="tool-pill-text">{{ headline }}</span>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { useToolStreamingOutput } from "../composables/toolProgress";
import { lastLine } from "../../utils/lastLine";

const props = defineProps<{
  headline: string;
  /** Undefined once the tool has a result: only running tools have output. */
  toolUseId?: string;
}>();

const streamingOutput = useToolStreamingOutput(() => props.toolUseId);
// lastLine's truncation is only a sanity bound; the CSS clip in
// .tool-pill-output is tighter, which is what the title tooltip is for.
const output = computed(() => lastLine(streamingOutput.value ?? ""));
// The hovered pill names what is running as well as where it has got to: the
// headline is otherwise invisible for as long as output keeps arriving.
const outputTitle = computed(() => `${props.headline}\n${output.value}`);
</script>
