<template>
  <section class="slack-integration">
    <div class="slack-destination">
      <label for="slack-hook">{{ t("slackDestination") }}</label>
      <select
        id="slack-hook"
        :value="hookKey(integration)"
        :disabled="sending"
        @change="selectHook"
      >
        <option v-for="hook in sortedHooks" :key="hookKey(hook)" :value="hookKey(hook)">
          {{ hookLabel(hook) }}
        </option>
      </select>
    </div>

    <p class="slack-access">{{ t("slackWebhookTestDescription") }}</p>
    <form @submit.prevent="emit('send')">
      <label for="slack-message">{{ t("testMessage") }}</label>
      <div class="slack-controls">
        <input
          id="slack-message"
          v-model="draft"
          maxlength="200"
          :disabled="sending"
          @input="emit('clear-result')"
        />
        <Button
          type="submit"
          :label="sending ? t('sending') : t('slackTestIntegration')"
          :disabled="sending || !draft.trim()"
        />
      </div>
    </form>
    <div v-if="result" class="integrations-status" :role="failed ? 'alert' : 'status'">
      {{ result }}
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from "vue";
import Button from "primevue/button";
import type { AttachedIntegration } from "../../services/api";
import { useI18n } from "../composables/i18n";

const props = defineProps<{
  integration: AttachedIntegration;
  hooks: AttachedIntegration[];
  message: string;
  sending: boolean;
  result: string;
  failed: boolean;
}>();
const emit = defineEmits<{
  (e: "select-hook", hook: AttachedIntegration): void;
  (e: "update:message", message: string): void;
  (e: "send" | "clear-result"): void;
}>();
const { t } = useI18n();
const draft = computed({ get: () => props.message, set: (value) => emit("update:message", value) });
const sortedHooks = computed(() =>
  [...props.hooks].sort((a, b) => hookLabel(a).localeCompare(hookLabel(b))),
);

function hookKey(hook: AttachedIntegration) {
  return `${hook.team ? "team" : "personal"}:${hook.name}`;
}

function hookLabel(hook: AttachedIntegration) {
  return `${hook.name} · ${t(hook.team ? "team" : "personal")}`;
}

function selectHook(event: Event) {
  if (props.sending) return;
  const key = (event.target as HTMLSelectElement).value;
  const hook = props.hooks.find((hook) => hookKey(hook) === key);
  if (hook) emit("select-hook", hook);
}
</script>

<style scoped>
.slack-integration {
  margin: 0.65rem 0 1rem;
}
.slack-integration label {
  display: block;
  font-size: 0.85rem;
  font-weight: 500;
  margin-bottom: 0.4rem;
}
.slack-integration input,
.slack-integration select {
  min-width: 0;
  padding: 0.5rem 0.65rem;
  border: 1px solid var(--border);
  border-radius: 0.375rem;
  background: var(--bg-base);
  color: var(--text-primary);
  font: inherit;
}
.slack-integration select {
  width: 100%;
}
.slack-access {
  font-size: 0.85rem;
  color: var(--text-secondary);
  margin: 0.65rem 0 1rem;
}
.slack-controls {
  display: flex;
  gap: 0.5rem;
}
.slack-controls input {
  flex: 1;
}
.slack-controls .p-button {
  flex-shrink: 0;
}
@media (max-width: 600px) {
  .slack-controls {
    flex-direction: column;
  }
}
</style>
