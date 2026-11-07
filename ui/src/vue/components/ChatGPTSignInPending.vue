<!-- A ChatGPT sign-in waiting on the browser. The callback goes to 127.0.0.1
     on Shelley's machine; when the browser is elsewhere that page fails to
     load, so the user pastes its address here instead. -->
<template>
  <div class="chatgpt-pending" data-testid="chatgpt-sign-in-pending">
    <p>
      {{ t("chatgptWaiting") }}
      <a :href="pending.authorize_url" target="_blank" rel="noopener noreferrer">{{
        t("chatgptOpenAgain")
      }}</a>
    </p>
    <p class="chatgpt-pending-hint">{{ t("chatgptPasteHint") }}</p>
    <form class="chatgpt-pending-paste" @submit.prevent="emit('complete', pasted)">
      <input
        v-model="pasted"
        type="text"
        :placeholder="`${pending.redirect_uri}?code=…`"
        :aria-label="t('chatgptPasteHint')"
        autocomplete="off"
        spellcheck="false"
        data-testid="chatgpt-callback-url"
      />
      <Button
        type="submit"
        size="small"
        :label="t('chatgptFinish')"
        :disabled="busy || !pasted.trim()"
      />
      <Button
        type="button"
        size="small"
        severity="secondary"
        text
        :label="t('cancel')"
        @click="emit('cancel')"
      />
    </form>
  </div>
</template>

<script setup lang="ts">
import { ref } from "vue";
import Button from "primevue/button";
import { useI18n } from "../composables/i18n";
import type { ChatGPTSignIn } from "../../services/api";

defineProps<{ pending: ChatGPTSignIn; busy: boolean }>();
const emit = defineEmits<{ (e: "complete", url: string): void; (e: "cancel"): void }>();
const { t } = useI18n();
const pasted = ref("");
</script>

<style scoped>
.chatgpt-pending {
  display: flex;
  flex-direction: column;
  gap: 0.4rem;
  padding: 0.6rem 0.75rem;
  border: 1px solid var(--blue-border);
  border-radius: 6px;
  background: var(--blue-bg);
}
.chatgpt-pending p {
  margin: 0;
  font-size: 0.875rem;
}
.chatgpt-pending-hint {
  color: var(--text-secondary);
}
.chatgpt-pending-paste {
  display: flex;
  gap: 0.4rem;
  align-items: center;
}
.chatgpt-pending-paste input {
  flex: 1;
  min-width: 0;
  padding: 0.35rem 0.5rem;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--bg-base);
  color: var(--text-primary);
  font-family: var(--font-mono);
  font-size: 0.8rem;
}
</style>
