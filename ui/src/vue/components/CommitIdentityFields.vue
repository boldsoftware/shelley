<!-- A commit identity's name and email, offering the names and emails of
     the user's GitHub accounts (logins) and exe.dev account; with prefill,
     empty fields take the first of them once they load. -->
<template>
  <div class="project-action-fields">
    <div>
      <label :for="`${id}-name`">{{ t("commitIdentityName") }}</label>
      <input
        :id="`${id}-name`"
        :value="modelValue.name"
        :list="`${id}-names`"
        :placeholder="placeholder?.name"
        :required="required || !!modelValue.email.trim()"
        :disabled="disabled"
        maxlength="254"
        autocomplete="off"
        data-testid="commit-identity-name"
        @input="update('name', $event)"
      />
      <datalist :id="`${id}-names`">
        <option v-for="name in suggestions?.names ?? []" :key="name" :value="name" />
      </datalist>
    </div>
    <div>
      <label :for="`${id}-email`">{{ t("commitIdentityEmail") }}</label>
      <input
        :id="`${id}-email`"
        :value="modelValue.email"
        type="email"
        :list="`${id}-emails`"
        :placeholder="placeholder?.email"
        :required="required || !!modelValue.name.trim()"
        :disabled="disabled"
        maxlength="254"
        autocomplete="off"
        data-testid="commit-identity-email"
        @input="update('email', $event)"
      />
      <datalist :id="`${id}-emails`">
        <option
          v-for="email in suggestions?.emails ?? []"
          :key="email.email"
          :value="email.email"
          :label="`${email.email} (${t(SOURCE_LABELS[email.source])})`"
        />
      </datalist>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, useId, watch } from "vue";
import {
  workspaceApi,
  type CommitIdentity,
  type CommitIdentitySuggestions,
} from "../../services/workspace";
import { useI18n } from "../composables/i18n";
import { prefillCommitIdentity } from "../../utils/commitIdentity";
import type { TranslationKeys } from "../../i18n/types";

const props = defineProps<{
  modelValue: CommitIdentity;
  /** GitHub logins whose public profiles are suggested. */
  logins: string[];
  prefill?: boolean;
  required?: boolean;
  placeholder?: CommitIdentity;
  disabled?: boolean;
}>();
const emit = defineEmits<{ (e: "update:modelValue", identity: CommitIdentity): void }>();
const { t } = useI18n();
const id = useId();
const SOURCE_LABELS: Record<
  CommitIdentitySuggestions["emails"][number]["source"],
  keyof TranslationKeys
> = {
  github: "commitEmailGithub",
  github_noreply: "commitEmailGithubNoreply",
  exe_dev: "commitEmailExeDev",
};
// Suggestions are offers: failing to load them leaves the fields to type in.
const suggestions = ref<CommitIdentitySuggestions | null>(null);

function update(field: keyof CommitIdentity, event: Event) {
  emit("update:modelValue", {
    ...props.modelValue,
    [field]: (event.target as HTMLInputElement).value,
  });
}
watch(
  () => props.logins.join(","),
  async () => {
    const logins = props.logins;
    const result = await workspaceApi.commitIdentitySuggestions(logins).catch(() => null);
    if (logins !== props.logins) return; // superseded
    suggestions.value = result;
  },
  { immediate: true },
);
watch(
  () => [props.prefill, suggestions.value] as const,
  ([prefill, loaded]) => {
    if (!prefill || !loaded) return;
    const filled = prefillCommitIdentity(props.modelValue, loaded);
    if (filled.name !== props.modelValue.name || filled.email !== props.modelValue.email)
      emit("update:modelValue", filled);
  },
  { immediate: true },
);
</script>
