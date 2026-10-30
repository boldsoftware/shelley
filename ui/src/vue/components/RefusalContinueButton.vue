<template>
  <div class="error-retry-row">
    <ModelPicker
      v-if="context"
      class="refusal-model-picker"
      :disabled="pending"
      :models="context.models.value"
      :selected-model="context.selectedModel.value"
      :thinking-level="context.thinkingLevel.value"
      :disabled-model="refusalModel"
      :trigger-label="pending ? 'Continuing\u2026' : 'Choose another model'"
      :show-recent="false"
      :show-reasoning="false"
      :catalog-actions="false"
      panel-class="refusal-model-picker-panel"
      aria-label-prefix="Continue with another model"
      data-testid="refusal-continue-button"
      @select-model="continueWithModel"
    />
    <span v-if="error" class="error-retry-error">{{ error }}</span>
  </div>
</template>

<script setup lang="ts">
import { inject, ref } from "vue";
import { api } from "../../services/api";
import ModelPicker from "./ModelPicker.vue";
import { RefusalContinueKey } from "./refusalContinue";

const props = defineProps<{ conversationId: string; refusalModel: string }>();
const context = inject(RefusalContinueKey);

const pending = ref(false);
const error = ref<string | null>(null);

async function continueWithModel(model: string) {
  if (pending.value) return;
  pending.value = true;
  error.value = null;
  try {
    await api.continueConversation(props.conversationId, model);
    window.setTimeout(() => (pending.value = false), 10000);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
    pending.value = false;
  }
}
</script>
