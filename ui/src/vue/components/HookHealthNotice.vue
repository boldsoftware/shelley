<template>
  <div class="resource-notice hook-health-notice" data-testid="hook-health-notice">
    <span class="resource-notice-icon" aria-hidden="true">!</span>
    <span class="resource-notice-text">
      <strong role="status" data-testid="hook-health-title">{{
        t("hookFailureTitle").replace("{hook}", "end-of-turn")
      }}</strong>
      <span class="resource-notice-detail">{{
        t(status.failures === 1 ? "hookFailureCountOne" : "hookFailureCountMany").replace(
          "{count}",
          String(status.failures),
        )
      }}</span>
    </span>
    <button
      type="button"
      class="resource-notice-link"
      data-testid="hook-health-troubleshoot"
      :disabled="busy"
      @click="emit('troubleshoot', status)"
    >
      {{ t("troubleshootWithShelley") }}
    </button>
    <button
      type="button"
      class="btn-icon resource-notice-dismiss"
      :aria-label="t('dismiss')"
      data-testid="hook-health-dismiss"
      :disabled="busy"
      @click="emit('dismiss', status)"
    >
      ×
    </button>
  </div>
</template>

<script setup lang="ts">
import type { HookHealthStatus } from "../../types";
import { useI18n } from "../composables/i18n";

defineProps<{ status: HookHealthStatus; busy: boolean }>();
const emit = defineEmits<{
  dismiss: [status: HookHealthStatus];
  troubleshoot: [status: HookHealthStatus];
}>();
const { t } = useI18n();
</script>
