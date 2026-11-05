<!-- Moves one conversation's work to another machine (PUT
     /api/conversation/ID/vm): this Shelley host, a new VM of its own (a copy
     of a base, or empty), or an existing machine (an exe.dev VM, or any host
     ssh reaches). Opened from ConversationPlace. A VM of its own that the
     conversation leaves is kept until it is archived. -->
<template>
  <Modal :is-open="isOpen" :title="`Move ${noun} to`" @close="emit('close')">
    <form id="machine-form" class="project-form" @submit.prevent="save">
      <fieldset class="machine-modes" :disabled="saving">
        <legend class="sr-only">Where the {{ noun }} works</legend>
        <label
          v-for="option in shownModes"
          :key="option.value"
          class="project-choice"
          :class="{ selected: mode === option.value }"
        >
          <input v-model="mode" type="radio" name="machine-mode" :value="option.value" />
          <i :class="['pi', option.icon]" aria-hidden="true" />
          <span>
            <strong>{{ option.label }}</strong>
            <span class="project-muted">{{ option.help }}</span>
          </span>
        </label>
      </fieldset>
      <template v-if="mode === 'new'">
        <label for="machine-integration">exe.dev integration</label>
        <select id="machine-integration" v-model="integration" required :disabled="saving">
          <option v-for="name in integrations" :key="name" :value="name">{{ name }}</option>
        </select>
        <label for="machine-base">Base VM</label>
        <input
          id="machine-base"
          v-model.trim="base"
          placeholder="Optional: leave empty for a new, empty VM"
          autocomplete="off"
          spellcheck="false"
          :disabled="saving"
        />
      </template>
      <template v-if="mode === 'existing'">
        <template v-if="vias.length > 1">
          <label for="machine-via">Reached through</label>
          <select id="machine-via" v-model="via" :disabled="saving">
            <option v-for="name in vias" :key="name" :value="name">
              {{ name ? `exe.dev, via ${name}` : "ssh, with this host's SSH configuration" }}
            </option>
          </select>
        </template>
        <label for="machine-name">{{ via ? "VM" : "SSH destination" }}</label>
        <input
          id="machine-name"
          v-model.trim="name"
          required
          :placeholder="via ? 'my-vm' : 'user@host'"
          autocomplete="off"
          spellcheck="false"
          :disabled="saving"
        />
      </template>
      <label for="machine-dir">Directory</label>
      <input
        id="machine-dir"
        v-model.trim="dir"
        :required="mode === 'existing' && !via"
        :placeholder="dirPlaceholder"
        autocomplete="off"
        spellcheck="false"
        :disabled="saving"
      />
      <p v-if="keptNote" class="project-muted">{{ keptNote }}</p>
      <p v-if="error" class="error-line" role="alert">{{ error }}</p>
    </form>
    <template #footer>
      <Button :label="t('cancel')" text severity="secondary" @click="emit('close')" />
      <Button
        type="submit"
        form="machine-form"
        label="Move"
        :loading="saving"
        data-testid="machine-save"
      />
    </template>
  </Modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import Button from "primevue/button";
import Modal from "./Modal.vue";
import { useI18n } from "../composables/i18n";
import { useWorkspace } from "../composables/workspace";
import { workspaceApi } from "../../services/workspace";
import { wording } from "../../utils/workspaceMode";

type Mode = "host" | "new" | "existing";
const props = defineProps<{ isOpen: boolean; conversationId: string }>();
const emit = defineEmits<{ (e: "close"): void }>();
const { t } = useI18n();
const workspace = useWorkspace();
const noun = wording("conversation", "task");

const current = computed(() => workspace.vmFor(props.conversationId));
const integrations = ref<string[]>([]);
const mode = ref<Mode>("host");
const integration = ref("");
const base = ref("");
const via = ref("");
const name = ref("");
const dir = ref("");
const saving = ref(false);
const error = ref("");

const modes: { value: Mode; icon: string; label: string; help: string }[] = [
  {
    value: "host",
    icon: "pi-server",
    label: "This Shelley host",
    help: "Its tools run here again.",
  },
  {
    value: "new",
    icon: "pi-clone",
    label: "A new VM of its own",
    help: `A copy of a base VM, or an empty VM. Archiving the ${noun} deletes it.`,
  },
  {
    value: "existing",
    icon: "pi-users",
    label: "An existing machine",
    help: "An exe.dev VM or SSH host. Shelley never creates or deletes it.",
  },
];
// A new VM needs an exe.dev integration.
const shownModes = computed(() =>
  modes.filter((option) => option.value !== "new" || integrations.value.length > 0),
);
const vias = computed(() => [...integrations.value, ""]);
const dirPlaceholder = computed(() => {
  if (mode.value === "host") return "Default: the project's directory, or your home directory";
  if (mode.value === "existing" && !via.value) return "/home/you/project";
  return "Default: the project's directory, or /home/exedev";
});
// Leaving a VM of its own keeps it until archiving.
const keptNote = computed(() => {
  const vm = current.value;
  if (!vm || vm.shared || vm.status === "deleted") return "";
  return `${vm.vm_name} is kept until the ${noun} is archived, which deletes it.`;
});

watch(
  () => props.isOpen,
  (open) => {
    if (!open) return;
    const vm = current.value;
    mode.value = !vm ? "host" : vm.shared ? "existing" : "new";
    base.value = "";
    name.value = vm?.shared ? vm.vm_name : "";
    via.value = vm?.integration ?? "";
    integration.value = vm?.integration ?? "";
    dir.value = "";
    error.value = "";
    workspaceApi.exeDevIntegrations().then(
      (result) => {
        integrations.value = result.integrations;
        integration.value ||= result.default || result.integrations[0] || "";
        if (!vm) via.value = result.default || "";
      },
      (err: unknown) => {
        error.value = err instanceof Error ? err.message : String(err);
      },
    );
  },
  { immediate: true },
);

async function save() {
  saving.value = true;
  error.value = "";
  try {
    const vm =
      mode.value === "host"
        ? null
        : mode.value === "new"
          ? {
              base: base.value,
              managed_base_vm: false,
              shared_vm: "",
              integration: integration.value,
              setup_conversation_id: "",
            }
          : {
              base: "",
              managed_base_vm: false,
              shared_vm: name.value,
              integration: via.value,
              setup_conversation_id: "",
            };
    await workspaceApi.changeConversationVM(props.conversationId, vm, dir.value);
    await workspace.refresh();
    emit("close");
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    saving.value = false;
  }
}
</script>

<style scoped>
.machine-modes {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  margin: 0;
  padding: 0;
  border: 0;
}
</style>
