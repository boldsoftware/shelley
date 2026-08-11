<template>
  <Modal
    :is-open="isOpen"
    :title="t('projectExperience')"
    class-name="modal-xwide"
    @close="closeModal"
  >
    <div class="experience-tabs" role="tablist">
      <button
        v-for="item in tabs"
        :key="item.id"
        :class="{ active: tab === item.id }"
        @click="tab = item.id"
      >
        {{ item.icon }} {{ t(item.labelKey) }}
      </button>
    </div>

    <div v-if="!cwd" class="experience-empty">{{ t("selectConversationForExperience") }}</div>
    <div v-else-if="error" class="experience-error">{{ error }}</div>

    <section v-if="cwd && tab === 'memory'" class="experience-section">
      <div class="experience-toolbar">
        <InputText
          v-model="query"
          :placeholder="t('searchProjectMemory')"
          fluid
          @keyup.enter="loadMemories"
        />
        <Button :label="t('search')" severity="secondary" @click="loadMemories" />
        <Button
          :label="editing ? t('cancel') : t('addMemory')"
          @click="editing ? resetMemoryForm() : startMemory()"
        />
      </div>
      <form v-if="editing" class="experience-form" @submit.prevent="saveMemory">
        <div class="experience-form-row">
          <Select
            v-model="draft.kind"
            :options="memoryKinds"
            option-label="label"
            option-value="value"
            class="experience-kind"
          />
          <InputText v-model="draft.title" :placeholder="t('shortTitle')" fluid maxlength="200" />
        </div>
        <Textarea
          v-model="draft.content"
          :placeholder="t('verifiedReusableProjectKnowledge')"
          fluid
          rows="5"
          maxlength="4000"
        />
        <label class="experience-confidence"
          >{{ t("confidence") }} {{ draft.confidence.toFixed(2) }}
          <input v-model.number="draft.confidence" type="range" min="0.1" max="1" step="0.05" />
        </label>
        <div class="experience-form-actions">
          <Button type="submit" :label="t('saveMemory')" :loading="saving" />
        </div>
      </form>
      <div v-if="loading" class="experience-empty">{{ t("loading") }}</div>
      <div v-else-if="memories.length === 0" class="experience-empty">
        {{ t("noProjectMemories") }}
      </div>
      <div v-else class="experience-list">
        <article v-for="memory in memories" :key="memory.id" class="experience-card">
          <div class="experience-card-head">
            <div>
              <span class="experience-kind-badge">{{ kindLabel(memory.kind) }}</span>
              <strong>{{ memory.title }}</strong>
            </div>
            <div class="experience-card-actions">
              <Button text size="small" :label="t('edit')" @click="startMemory(memory)" />
              <Button
                v-if="deletePending !== memory.id"
                text
                size="small"
                severity="danger"
                :label="t('delete_')"
                @click="deletePending = memory.id"
              />
              <Button
                v-else
                size="small"
                severity="danger"
                :label="t('confirmDeleteShort')"
                @click="deleteMemory(memory.id)"
              />
            </div>
          </div>
          <p>{{ memory.content }}</p>
          <div class="experience-meta">
            {{ t("confidence") }} {{ memory.confidence.toFixed(2) }} ·
            {{ formatDate(memory.updated_at) }}
          </div>
        </article>
      </div>
    </section>

    <section v-if="cwd && tab === 'journal'" class="experience-section">
      <form v-if="conversationId" class="experience-form" @submit.prevent="saveCheckpoint">
        <InputText
          v-model="checkpoint.summary"
          :placeholder="t('checkpointSummary')"
          fluid
          maxlength="1000"
        />
        <InputText
          v-model="checkpoint.goal"
          :placeholder="t('currentGoal')"
          fluid
          maxlength="2000"
        />
        <InputText
          v-model="checkpoint.next"
          :placeholder="t('nextAction')"
          fluid
          maxlength="2000"
        />
        <div class="experience-form-actions">
          <Button type="submit" :label="t('saveCheckpoint')" :loading="saving" />
        </div>
      </form>
      <div v-else class="experience-empty">{{ t("openConversationForJournal") }}</div>
      <div v-if="loading" class="experience-empty">{{ t("loading") }}</div>
      <div v-else-if="conversationId && checkpoints.length === 0" class="experience-empty">
        {{ t("noCheckpoints") }}
      </div>
      <div v-else class="experience-list">
        <article v-for="item in checkpoints" :key="item.id" class="experience-card">
          <div class="experience-card-head">
            <strong>{{ item.summary }}</strong
            ><span class="experience-kind-badge">{{ checkpointTypeLabel(item.event_type) }}</span>
          </div>
          <div v-if="item.state.goal" class="experience-state">
            <b>{{ t("goalLabel") }}</b> {{ item.state.goal }}
          </div>
          <div v-if="item.state.next" class="experience-state">
            <b>{{ t("nextLabel") }}</b> {{ item.state.next }}
          </div>
          <div class="experience-meta">{{ formatDate(item.created_at) }}</div>
        </article>
      </div>
    </section>

    <section v-if="cwd && tab === 'dream'" class="experience-section">
      <div class="experience-dream-action">
        <div>
          <strong>{{ t("consolidateThisTask") }}</strong>
          <p>{{ t("dreamDescription") }}</p>
        </div>
        <div class="experience-card-actions">
          <Button :label="t('refresh')" severity="secondary" @click="loadDreams" />
          <Button
            :label="t('runDream')"
            :disabled="!conversationId || dreamRequested"
            :loading="dreamRequested"
            @click="runDream"
          />
        </div>
      </div>
      <div v-if="dreamRequested" class="experience-notice">{{ t("dreamRequested") }}</div>
      <div v-if="loading" class="experience-empty">{{ t("loading") }}</div>
      <div v-else-if="dreams.length === 0" class="experience-empty">{{ t("noDreamRuns") }}</div>
      <div v-else class="experience-list">
        <article v-for="item in dreams" :key="item.id" class="experience-card">
          <div class="experience-card-head">
            <strong>{{ t("dream") }}</strong
            ><span>{{ item.memory_count }} {{ t("memories") }}</span>
          </div>
          <p>{{ item.summary }}</p>
          <div class="experience-meta">{{ formatDate(item.created_at) }}</div>
        </article>
      </div>
    </section>
  </Modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import Modal from "./Modal.vue";
import Button from "primevue/button";
import InputText from "primevue/inputtext";
import Textarea from "primevue/textarea";
import Select from "primevue/select";
import { api, type DreamRun, type ExperienceMemory, type TaskCheckpoint } from "../../services/api";
import { useI18n } from "../composables/i18n";

type Tab = "memory" | "journal" | "dream";
const props = defineProps<{ isOpen: boolean; cwd: string; conversationId: string | null }>();
const emit = defineEmits<{ (e: "close"): void }>();
const { t, locale } = useI18n();
const tabs = [
  { id: "memory" as Tab, labelKey: "memory" as const, icon: "🧠" },
  { id: "journal" as Tab, labelKey: "taskJournal" as const, icon: "📋" },
  { id: "dream" as Tab, labelKey: "dream" as const, icon: "💭" },
];
const memoryKinds = computed(() =>
  (["fact", "decision", "preference", "lesson"] as ExperienceMemory["kind"][]).map((value) => ({
    value,
    label: kindLabel(value),
  })),
);
const tab = ref<Tab>("memory");
const loading = ref(false);
const saving = ref(false);
const error = ref<string | null>(null);
const query = ref("");
const memories = ref<ExperienceMemory[]>([]);
const checkpoints = ref<TaskCheckpoint[]>([]);
const dreams = ref<DreamRun[]>([]);
const editing = ref(false);
const editingId = ref<string | null>(null);
const deletePending = ref<string | null>(null);
const dreamRequested = ref(false);
const draft = reactive({
  kind: "lesson" as ExperienceMemory["kind"],
  title: "",
  content: "",
  confidence: 1,
});
const checkpoint = reactive({ summary: "", goal: "", next: "" });

function kindLabel(kind: ExperienceMemory["kind"]): string {
  const keys = {
    fact: "kindFact",
    decision: "kindDecision",
    preference: "kindPreference",
    lesson: "kindLesson",
  } as const;
  return t(keys[kind]);
}
function checkpointTypeLabel(type: string): string {
  if (type === "manual") return t("manualCheckpoint");
  if (type === "checkpoint") return t("agentCheckpoint");
  return type;
}
function formatDate(value: string) {
  const locales = {
    en: "en-US",
    ja: "ja-JP",
    fr: "fr-FR",
    ru: "ru-RU",
    es: "es-ES",
    "zh-CN": "zh-CN",
    "zh-TW": "zh-TW",
    upgoer5: "en-US",
    vi: "vi-VN",
  } as const;
  return new Date(value).toLocaleString(locales[locale.value]);
}
function closeModal() {
  dreamRequested.value = false;
  emit("close");
}
function resetMemoryForm() {
  editing.value = false;
  editingId.value = null;
  draft.kind = "lesson";
  draft.title = "";
  draft.content = "";
  draft.confidence = 1;
}
function startMemory(memory?: ExperienceMemory) {
  editing.value = true;
  editingId.value = memory?.id ?? null;
  draft.kind = memory?.kind ?? "lesson";
  draft.title = memory?.title ?? "";
  draft.content = memory?.content ?? "";
  draft.confidence = memory?.confidence ?? 1;
}
async function loadMemories() {
  if (!props.cwd) return;
  loading.value = true;
  error.value = null;
  try {
    memories.value = await api.getExperienceMemories(props.cwd, query.value);
  } catch (e) {
    error.value = String(e);
  } finally {
    loading.value = false;
  }
}
async function loadJournal() {
  if (!props.conversationId) {
    checkpoints.value = [];
    return;
  }
  loading.value = true;
  try {
    checkpoints.value = await api.getTaskCheckpoints(props.conversationId);
  } catch (e) {
    error.value = String(e);
  } finally {
    loading.value = false;
  }
}
async function loadDreams() {
  if (!props.cwd) return;
  loading.value = true;
  try {
    dreams.value = await api.getDreamRuns(props.cwd);
  } catch (e) {
    error.value = String(e);
  } finally {
    loading.value = false;
  }
}
async function saveMemory() {
  saving.value = true;
  error.value = null;
  try {
    if (editingId.value)
      await api.updateExperienceMemory(props.cwd, { id: editingId.value, ...draft });
    else
      await api.createExperienceMemory({
        cwd: props.cwd,
        conversation_id: props.conversationId ?? undefined,
        ...draft,
      });
    resetMemoryForm();
    await loadMemories();
  } catch (e) {
    error.value = String(e);
  } finally {
    saving.value = false;
  }
}
async function deleteMemory(id: string) {
  saving.value = true;
  try {
    await api.deleteExperienceMemory(props.cwd, id);
    deletePending.value = null;
    await loadMemories();
  } catch (e) {
    error.value = String(e);
  } finally {
    saving.value = false;
  }
}
async function saveCheckpoint() {
  if (!props.conversationId) return;
  saving.value = true;
  error.value = null;
  try {
    await api.createTaskCheckpoint({
      conversation_id: props.conversationId,
      summary: checkpoint.summary,
      state: { goal: checkpoint.goal, next: checkpoint.next },
    });
    checkpoint.summary = "";
    checkpoint.goal = "";
    checkpoint.next = "";
    await loadJournal();
  } catch (e) {
    error.value = String(e);
  } finally {
    saving.value = false;
  }
}
async function runDream() {
  if (!props.conversationId) return;
  dreamRequested.value = true;
  try {
    await api.sendMessage(props.conversationId, { message: t("dreamAgentPrompt"), queue: true });
  } catch (e) {
    error.value = String(e);
    dreamRequested.value = false;
  }
}
async function loadTab() {
  error.value = null;
  if (tab.value === "memory") await loadMemories();
  else if (tab.value === "journal") await loadJournal();
  else await loadDreams();
}
watch(
  [() => props.isOpen, tab, () => props.cwd, () => props.conversationId],
  () => {
    if (props.isOpen) void loadTab();
  },
  { immediate: true },
);
</script>

<style scoped>
.experience-tabs {
  display: flex;
  gap: 0.4rem;
  border-bottom: 1px solid var(--border);
  margin-bottom: 1rem;
}
.experience-tabs button {
  padding: 0.65rem 0.9rem;
  border: 0;
  border-bottom: 2px solid transparent;
  background: transparent;
  color: var(--text-secondary);
  cursor: pointer;
}
.experience-tabs button.active {
  color: var(--text-primary);
  border-bottom-color: var(--primary);
}
.experience-section,
.experience-list {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}
.experience-toolbar,
.experience-form-row,
.experience-card-head,
.experience-dream-action {
  display: flex;
  gap: 0.65rem;
  align-items: center;
  justify-content: space-between;
}
.experience-toolbar > :first-child {
  flex: 1;
}
.experience-form {
  display: flex;
  flex-direction: column;
  gap: 0.7rem;
  padding: 0.8rem;
  border: 1px solid var(--border);
  border-radius: 0.5rem;
}
.experience-form-row > :last-child {
  flex: 1;
}
.experience-kind {
  min-width: 9rem;
}
.experience-form-actions {
  text-align: right;
}
.experience-confidence {
  display: flex;
  gap: 0.7rem;
  align-items: center;
  font-size: 0.85rem;
  color: var(--text-secondary);
}
.experience-confidence input {
  flex: 1;
}
.experience-card {
  border: 1px solid var(--border);
  border-radius: 0.5rem;
  padding: 0.8rem;
}
.experience-card p {
  white-space: pre-wrap;
  margin: 0.55rem 0;
}
.experience-card-actions {
  display: flex;
  gap: 0.25rem;
}
.experience-kind-badge {
  font-size: 0.72rem;
  padding: 0.15rem 0.35rem;
  border-radius: 0.3rem;
  background: var(--bg-hover);
  color: var(--text-secondary);
}
.experience-meta,
.experience-state {
  color: var(--text-secondary);
  font-size: 0.8rem;
}
.experience-state {
  margin-top: 0.4rem;
}
.experience-empty {
  padding: 2rem;
  text-align: center;
  color: var(--text-secondary);
}
.experience-error {
  color: var(--error-text);
  margin-bottom: 0.7rem;
}
.experience-notice {
  padding: 0.65rem;
  background: var(--bg-hover);
  border-radius: 0.4rem;
}
.experience-dream-action {
  border: 1px solid var(--border);
  border-radius: 0.5rem;
  padding: 1rem;
}
.experience-dream-action p {
  color: var(--text-secondary);
  margin: 0.25rem 0 0;
}
@media (max-width: 700px) {
  .experience-toolbar,
  .experience-form-row,
  .experience-dream-action {
    align-items: stretch;
    flex-direction: column;
  }
  .experience-card-head {
    align-items: flex-start;
  }
  .experience-card-actions {
    flex-wrap: wrap;
  }
}
</style>
