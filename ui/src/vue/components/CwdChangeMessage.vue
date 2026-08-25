<!-- Sub-component of Message.vue. Renders the notice recorded when the user
     moves a conversation's working directory from the status readout (see
     recordCwdChangeNotice, and the cwdChange predicate in types.ts).

     The row is a user-role message because the agent has to read it: it holds
     the old directory in its system prompt and in every tool result so far, and
     would go on resolving relative paths against it otherwise. But the user
     didn't type it, so rendering it as a chat bubble would put words in their
     mouth. It gets the same compact status treatment as the gitinfo and
     modelchange markers instead, reusing .message-gitinfo for that shape. -->
<template>
  <div class="message message-gitinfo msg-cwdchange-container" data-testid="message-cwdchange">
    <span class="msg-cwdchange-icon" aria-hidden="true">📁</span>
    <span class="msg-cwdchange-text">
      <template v-if="from">
        working directory
        <code class="msg-cwdchange-path" :title="from">{{ tildifyPath(from) }}</code>
        <span class="msg-cwdchange-arrow" aria-hidden="true">→</span>
        <code class="msg-cwdchange-path" :title="to">{{ tildifyPath(to) }}</code>
      </template>
      <template v-else>
        working directory set to
        <code class="msg-cwdchange-path" :title="to">{{ tildifyPath(to) }}</code>
      </template>
    </span>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { Message as MessageType } from "../../types";
import { cwdChange } from "../../types";
import { tildifyPath } from "../../utils/tildify";

const props = defineProps<{ message: MessageType }>();

const change = computed(() => cwdChange(props.message) || { from: "", to: "" });
const from = computed(() => change.value.from);
const to = computed(() => change.value.to);
</script>
