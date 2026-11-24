<!-- The secrets Shelley captured (docs/secrets.md), from the command palette
     or a secret's pill: show, copy or delete each. -->
<template>
  <Modal
    :is-open="isOpen"
    title="Secrets"
    class-name="modal-wide"
    @close="emit('close')"
  >
    <p class="secrets-intro">
      Shelley tries to capture secrets from chat input to avoid sending them to the LLM provider.
    </p>
    <div v-if="error" class="secrets-status" role="alert">{{ error }}</div>
    <span class="sr-only" aria-live="polite">{{ copied ? "Copied" : "" }}</span>
    <div v-if="targetGone" class="secrets-status">
      <code>{{ target }}</code> expired or was deleted.
    </div>
    <div v-if="secrets === null && !error" class="secrets-status">Loading…</div>
    <div v-else-if="secrets?.length === 0 && !targetGone" class="secrets-status">No secrets.</div>
    <table v-else-if="secrets?.length" class="secrets-table">
      <thead>
        <tr>
          <th scope="col">Secret</th>
          <th scope="col" class="hide-on-mobile">Conversation</th>
          <th scope="col" class="hide-on-mobile">Expires</th>
          <th scope="col"><span class="sr-only">Actions</span></th>
        </tr>
      </thead>
      <tbody>
        <template v-for="s in secrets" :key="s.id">
          <tr
            :class="{ 'secrets-target': s.id === target }"
            :data-row="s.id"
            data-testid="secret-row"
          >
            <th scope="row">
              <code>{{ s.id }}</code>
              <div class="secrets-narrow hide-on-desktop">
                <template v-if="s.conversation_id">
                  <a
                    :href="`/c/${encodeURIComponent(conversation(s))}`"
                    @click="openConversation($event, conversation(s))"
                    >{{ conversation(s) }}</a
                  >
                  ·
                </template>
                {{ expires(s) }}
              </div>
            </th>
            <td class="hide-on-mobile">
              <a
                v-if="s.conversation_id"
                :href="`/c/${encodeURIComponent(conversation(s))}`"
                @click="openConversation($event, conversation(s))"
                >{{ conversation(s) }}</a
              >
            </td>
            <td class="hide-on-mobile">{{ expires(s) }}</td>
            <td class="secrets-actions" @keydown.escape="cancelDelete($event, s)">
              <template v-if="confirming === s.id">
                <Button
                  size="small"
                  severity="danger"
                  label="Delete"
                  data-testid="secret-delete-confirm"
                  @click="remove(s)"
                />
                <Button
                  size="small"
                  text
                  severity="secondary"
                  label="Cancel"
                  class="secrets-cancel"
                  @click="cancelDelete(null, s)"
                />
              </template>
              <template v-else>
                <Button
                  size="small"
                  text
                  :label="revealed[s.id] === undefined ? 'Show' : 'Hide'"
                  @click="toggleShown(s)"
                />
                <Button
                  size="small"
                  text
                  :label="copied === s.id ? 'Copied' : 'Copy'"
                  @click="copyValue(s)"
                />
                <Button
                  size="small"
                  text
                  severity="danger"
                  label="Delete"
                  class="secrets-delete"
                  @click="askDelete(s)"
                />
              </template>
            </td>
          </tr>
          <tr v-if="revealed[s.id] !== undefined" class="secrets-value">
            <td colspan="4">
              <code>{{ revealed[s.id] }}</code>
            </td>
          </tr>
        </template>
      </tbody>
    </table>
  </Modal>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch } from "vue";
import Button from "primevue/button";
import Modal from "./Modal.vue";
import { secretsApi, type Secret } from "../../services/api";
import { copyPendingText } from "../../utils/clipboard";
import { navigateToConversationSlug } from "../composables/subagentLive";

const props = defineProps<{
  isOpen: boolean;
  /** The secret whose pill opened it. */
  target?: string | null;
}>();
const emit = defineEmits<{ (e: "close"): void }>();

const secrets = ref<Secret[] | null>(null);
const error = ref("");
const revealed = reactive<Record<string, string>>({});
const confirming = ref<string | null>(null);
/** The target, once deleted here: no need to say it's gone. */
const removed = ref("");
const targetGone = computed(
  () =>
    !!props.target &&
    props.target !== removed.value &&
    !!secrets.value &&
    !secrets.value.some((s) => s.id === props.target),
);
const copied = ref("");
let copiedTimer: ReturnType<typeof setTimeout> | undefined;

function conversation(s: Secret): string {
  return s.conversation_slug ?? s.conversation_id ?? "";
}

function expires(s: Secret): string {
  const min = Math.round((new Date(s.expires_at).getTime() - Date.now()) / 60_000);
  if (min < 1) return "in <1m";
  if (min < 60) return `in ${min}m`;
  return `in ${Math.round(min / 60)}h`;
}

async function run(f: () => Promise<void>) {
  error.value = "";
  try {
    await f();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}

async function toggleShown(s: Secret) {
  if (revealed[s.id] !== undefined) {
    delete revealed[s.id];
    return;
  }
  await run(async () => {
    revealed[s.id] = await secretsApi.reveal(s.id);
  });
}

async function copyValue(s: Secret) {
  await run(async () => {
    if (!(await copyPendingText(secretsApi.reveal(s.id)))) throw new Error("Could not copy");
    copied.value = s.id;
    clearTimeout(copiedTimer);
    copiedTimer = setTimeout(() => (copied.value = ""), 1500);
  });
}

/** Focuses a button in s's row once it renders. */
async function focusIn(s: Secret, selector: string) {
  await nextTick();
  document
    .querySelector<HTMLElement>(`tr[data-row="${CSS.escape(s.id)}"] ${selector}`)
    ?.focus();
}

function askDelete(s: Secret) {
  confirming.value = s.id;
  void focusIn(s, ".secrets-cancel");
}

/** Cancel, or Escape while confirming, which then leaves the modal open. */
function cancelDelete(event: KeyboardEvent | null, s: Secret) {
  if (confirming.value !== s.id) return;
  event?.stopPropagation();
  confirming.value = null;
  void focusIn(s, ".secrets-delete");
}

async function remove(s: Secret) {
  confirming.value = null;
  await run(async () => {
    await secretsApi.remove(s.id);
    if (s.id === props.target) removed.value = s.id;
    delete revealed[s.id];
    // Keep focus in the dialog: on the next row's Delete, or the close button.
    const dialog = document.querySelector(`tr[data-row="${CSS.escape(s.id)}"]`)?.closest(".p-dialog");
    const i = secrets.value?.indexOf(s) ?? 0;
    const rest = secrets.value?.filter((x) => x.id !== s.id) ?? [];
    secrets.value = rest;
    const next = rest[Math.min(i, rest.length - 1)];
    if (next) await focusIn(next, ".secrets-delete");
    else dialog?.querySelector<HTMLElement>('[aria-label="Close modal"]')?.focus();
  });
}

function openConversation(event: MouseEvent, slug: string) {
  if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0)
    return;
  event.preventDefault();
  navigateToConversationSlug(encodeURIComponent(slug));
  emit("close");
}

watch(
  () => props.isOpen,
  async (open) => {
    if (!open) {
      for (const id of Object.keys(revealed)) delete revealed[id];
      confirming.value = null;
      removed.value = "";
      return;
    }
    secrets.value = null;
    await run(async () => {
      secrets.value = await secretsApi.list();
    });
    await nextTick();
    document.querySelector(".secrets-target")?.scrollIntoView({ block: "center" });
  },
  { immediate: true },
);

onBeforeUnmount(() => clearTimeout(copiedTimer));
</script>
