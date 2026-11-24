<template>
  <Modal :is-open="true" :title="t('editTags')" @close="close">
    <div class="project-form">
      <div v-if="tags.length" class="conversation-tags">
        <span v-for="(tag, index) in tags" :key="index" class="conversation-tag conversation-tag-removable">
          <span class="conversation-tag-hash" aria-hidden="true">#</span>
          <span class="conversation-tag-text">{{ tag }}</span>
          <small v-if="suggested.has(tag)">New</small>
          <button
            type="button"
            class="conversation-tag-remove"
            :aria-label="`${t('removeTag')}: ${tag}`"
            :disabled="saving"
            @click="remove(index)"
          >
            <i class="pi pi-times" aria-hidden="true" />
          </button>
        </span>
      </div>
      <p v-else class="project-muted tag-command-empty">No tags yet.</p>
      <form class="tag-command-entry" @submit.prevent="add">
        <input v-model="input" aria-label="New tag" placeholder="Add a tag…" :disabled="saving" />
        <Button type="submit" label="Add" severity="secondary" outlined :disabled="saving || !input.trim()" />
      </form>
      <p v-if="generating" class="project-muted" role="status">Finding tags from your chapters and recent messages…</p>
      <p v-else-if="notice" class="project-muted" role="status">{{ notice }}</p>
      <p v-if="error" class="error-line" role="alert">{{ error }}</p>
    </div>
    <template #footer>
      <Button
        class="tag-command-suggest"
        :label="generating ? 'Generating…' : 'Suggest tags'"
        severity="secondary"
        text
        :loading="generating"
        :disabled="saving"
        @click="autoTag"
      />
      <Button :label="t('cancel')" text severity="secondary" :disabled="saving" @click="close" />
      <Button :label="t('save')" :loading="saving" :disabled="generating" @click="save" />
    </template>
  </Modal>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref } from "vue";
import Button from "primevue/button";
import { api, apiFetch, sendJSON } from "../../services/api";
import type { Conversation } from "../../types";
import { foldTag } from "../../utils/tagFilter";
import { diffTags } from "../../utils/tagCommand";
import { useI18n } from "../composables/i18n";
import Modal from "./Modal.vue";

const { t } = useI18n();
const props = defineProps<{ conversationId: string; initialTags: string[]; knownTags: string[] }>();
const emit = defineEmits<{ close: []; saved: [conversation: Conversation] }>();
const tags = ref([...props.initialTags]);
const suggested = ref(new Set<string>());
const input = ref("");
const generating = ref(false);
const saving = ref(false);
const error = ref("");
const notice = ref("");
let generation: AbortController | undefined;

function close() {
  if (!saving.value) emit("close");
}
onBeforeUnmount(() => generation?.abort());

function add() {
  const tag = input.value.trim().replace(/^#+/, "").trim();
  if (tag && !tags.value.some((current) => foldTag(current) === foldTag(tag))) tags.value.push(tag);
  input.value = "";
}

function remove(index: number) {
  suggested.value.delete(tags.value[index]);
  tags.value.splice(index, 1);
}

async function autoTag() {
  if (generating.value || saving.value) return;
  const controller = new AbortController();
  generation = controller;
  generating.value = true;
  error.value = "";
  notice.value = "";
  try {
    const response = await apiFetch(
      `/api/conversation/${encodeURIComponent(props.conversationId)}/tag-suggestions`,
      "Failed to suggest tags",
      sendJSON("POST", { existing_tags: tags.value, known_tags: props.knownTags }, controller.signal),
    );
    const result: { tags: string[] } = await response.json();
    if (controller.signal.aborted) return;
    const held = new Set(tags.value.map(foldTag));
    let added = 0;
    for (const tag of result.tags) {
      const key = foldTag(tag);
      if (!key || held.has(key)) continue;
      held.add(key);
      tags.value.push(tag);
      suggested.value.add(tag);
      added++;
    }
    if (!added) notice.value = "No new tags suggested.";
  } catch (err) {
    if (!controller.signal.aborted) error.value = err instanceof Error ? err.message : String(err);
  } finally {
    generation = undefined;
    generating.value = false;
  }
}

async function save() {
  if (saving.value || generating.value) return;
  add();
  const edits = diffTags(props.initialTags, tags.value);
  if (!edits.length) {
    close();
    return;
  }
  saving.value = true;
  error.value = "";
  try {
    emit("saved", await api.editConversationTags(props.conversationId, edits));
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    saving.value = false;
  }
}
</script>

<style scoped>
.tag-command-entry {
  display: flex;
  gap: 0.5rem;
}

.tag-command-entry input {
  flex: 1;
  min-width: 0;
}

.conversation-tag {
  padding: 0.2rem 0.4rem;
  border-radius: 4px;
  background: var(--bg-secondary);
}

.conversation-tag small {
  margin-left: 0.25rem;
  color: var(--text-secondary);
}

.conversation-tag-remove {
  opacity: 0.7;
}

.tag-command-empty {
  margin: 0.2rem 0;
}

.tag-command-suggest {
  margin-right: auto;
}
</style>
