<template>
  <ToolCard emoji="🔎" :complete="complete" :is-running="isRunning" :tool-invoked-at="toolInvokedAt">
    <template #summary><span class="tool-command">Search and read code</span></template>
    <div v-if="groups.length" class="source-requests">
      <template v-for="(group, i) in groups" :key="group.label">
        <hr v-if="i > 0" />
        <section>
          <h3>{{ group.label }}</h3>
          <div v-for="({ title, fields }, j) in group.items" :key="j" class="request-item">
            <h4><code>{{ title }}</code></h4>
            <ul v-if="fields.length">
              <li v-for="[key, value] in fields" :key="key"><strong>{{ key }}:</strong> <code>{{ value }}</code></li>
            </ul>
          </div>
        </section>
      </template>
    </div>
    <div v-if="complete" class="tool-section">
      <div class="tool-label">Code{{ hasError ? " (Error)" : "" }}:</div>
      <pre class="tool-code source-excerpt"><span v-for="(line, i) in lines" :key="i" :class="{ 'source-file': line.startsWith('File: '), 'source-hit': line.startsWith('› ') }">{{ line.startsWith("› ") ? line : line + "\n" }}</span></pre>
    </div>
  </ToolCard>
</template>
<script setup lang="ts">
import { computed } from "vue";
import ToolCard from "./ToolCard.vue";
import { inputFields, resultText, toolComplete, type ToolProps } from "./toolProps";
const props = defineProps<ToolProps>();
const groups = computed(() => {
  const input = inputFields(props.toolInput);
  return [["searches", "Searches", "query"], ["reads", "Reads", "path"]].flatMap(([key, label, titleKey]) => {
    const requests = input[key];
    if (!Array.isArray(requests) || !requests.length) return [];
    return [{
      label,
      items: requests.map((item: unknown) => {
        const fields = inputFields(item);
        return {
          title: fields[titleKey],
          fields: Object.entries(fields).filter(([name]) => name !== titleKey),
        };
      }),
    }];
  });
});
const result = computed(() => resultText(props.toolResult));
const lines = computed(() => result.value.replace(/\n$/, "").split("\n"));
const complete = computed(() => toolComplete(props));
</script>

<style scoped>
.source-excerpt { white-space: pre; overflow-x: auto; }
.source-file { font-weight: 700; }
.source-hit { display: block; width: max-content; min-width: 100%; background: rgba(245, 208, 70, .13); }
.source-requests {
  background: light-dark(#fff, #202124);
  color: light-dark(#202124, #f1f2f3);
  border: 1px solid light-dark(#e7e7e7, #414247);
  border-radius: 8px;
  padding: 12px 14px;
  margin-bottom: 18px;
  font: 14px/1.5 system-ui, sans-serif;
  overflow-wrap: anywhere;
}
.source-requests h3, .source-requests h4 { margin: 0; font: inherit; }
.source-requests h3 { font-weight: 650; }
.source-requests ul { margin: 2px 0 0; padding-inline-start: 1.2em; }
.source-requests code { font-size: inherit; white-space: pre-wrap; }
.request-item { margin: 6px 0 0 8px; }
.request-item + .request-item { margin-top: 10px; }
.request-item h4 code {
  background: light-dark(#f1f2f3, #37383c);
  border-radius: 4px;
  padding: 1px 4px;
  box-decoration-break: clone;
}
.source-requests hr { margin: 10px 0; border: 0; border-top: 1px solid light-dark(#e7e7e7, #414247); }
</style>
