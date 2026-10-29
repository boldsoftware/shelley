<!-- Where a project's conversations run: their own VMs copied from a base (the
     default for new projects in workspace mode), or this Shelley host. Part of
     the project editor. Shown only where usable: on a host with exe.dev
     integrations, or for a project with a VM. -->
<template>
  <template v-if="available">
  <div v-if="heading" class="project-section-heading">
    <h3>{{ Nouns }} run on</h3>
  </div>
  <fieldset class="project-vm-modes" :disabled="disabled">
    <legend class="sr-only">{{ Nouns }} run on</legend>
    <label
      v-for="option in modes"
      :key="option.value"
      class="project-choice"
      :class="{ selected: mode === option.value }"
    >
      <input
        type="radio"
        name="project-vm-mode"
        :value="option.value"
        :checked="mode === option.value"
        @change="setMode(option.value)"
      />
      <i :class="['pi', option.icon]" aria-hidden="true" />
      <span>
        <strong>{{ option.label }}</strong>
        <span v-if="option.value === 'own' && workspaceMode" class="project-badge">Recommended</span>
        <span class="project-muted">{{ option.help }}</span>
      </span>
    </label>
  </fieldset>
  <template v-if="vm && mode === 'own'">
    <ProjectVMSetup v-if="vm.setup_conversation_id" :vm="vm" @navigate="emit('navigate')" />
    <label for="project-vm-base">Base VM <span class="project-muted">(optional)</span></label>
    <input
      id="project-vm-base"
      :value="vm.base"
      placeholder="Empty: an agent creates and prepares one"
      autocomplete="off"
      :disabled="disabled"
      @input="update({ base: ($event.target as HTMLInputElement).value })"
    />
    <p v-if="!vm.base && !vm.setup_conversation_id" class="project-muted">
      Saving starts a setup {{ noun }} that creates a base VM, clones the repositories, installs
      toolchains and warms caches. New {{ nouns }} wait until it is done.
    </p>
  </template>
  <template v-if="vm">
    <label for="project-vm-integration">exe.dev integration</label>
    <input
      id="project-vm-integration"
      :value="vm.integration"
      required
      list="project-vm-integrations"
      placeholder="my-exe"
      autocomplete="off"
      :disabled="disabled"
      @input="update({ integration: ($event.target as HTMLInputElement).value })"
    />
    <datalist id="project-vm-integrations">
      <option v-for="name in integrations" :key="name" :value="name" />
    </datalist>
    <p class="project-muted">
      Creates and uses VMs on your exe.dev account through this integration. The browser,
      read_image and uploads stay on this host; agents copy files between it and the VM.
    </p>
  </template>
  <p v-if="error" class="error-line" role="alert">{{ error }}</p>
  </template>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import ProjectVMSetup from "./ProjectVMSetup.vue";
import { workspaceApi, type ProjectVM } from "../../services/workspace";
import { wording, workspaceMode } from "../../utils/workspaceMode";

const noun = wording("conversation", "task");
const nouns = wording("conversations", "tasks");
const Nouns = wording("Conversations", "Tasks");

export type VMMode = "host" | "own";
const props = defineProps<{ vm: ProjectVM | null; disabled?: boolean; heading?: boolean }>();
const emit = defineEmits<{
  (e: "update:vm", vm: ProjectVM | null): void;
  (e: "navigate"): void;
}>();
const modes: { value: VMMode; icon: string; label: string; help: string }[] = [
  {
    value: "own",
    icon: "pi-clone",
    label: "Their own VMs",
    help: `Each ${noun} gets a copy of a prepared base VM, so they never collide. Archiving a ${noun} deletes its VM.`,
  },
  {
    value: "host",
    icon: "pi-server",
    label: "This Shelley host",
    help: `${Nouns} share this machine and its checkouts.`,
  },
];
const integrations = ref<string[]>([]);
// Shown for a project with a VM stays shown after picking this host, so the
// choice can be undone on a host with no integrations.
const hadVM = !!props.vm;
const available = computed(() => hadVM || integrations.value.length > 0);
const defaultIntegration = ref("");
const error = ref("");
const mode = computed<VMMode>(() => (!props.vm ? "host" : "own"));
const loaded = workspaceApi.exeDevIntegrations().then(
  (result) => {
    integrations.value = result.integrations;
    defaultIntegration.value = result.default;
  },
  (err: unknown) => {
    error.value = err instanceof Error ? err.message : String(err);
  },
);
// A VM mode chosen before the integrations loaded gets the default then.
onMounted(() =>
  loaded.then(() => {
    if (props.vm && !props.vm.integration) update({ integration: defaultIntegration.value });
  }),
);
function update(change: Partial<ProjectVM>) {
  if (props.vm) emit("update:vm", { ...props.vm, ...change });
}
function setMode(next: VMMode) {
  const vm = props.vm;
  emit(
    "update:vm",
    next === "host"
      ? null
      : {
          base: vm?.base ?? "",
          integration: vm?.integration || defaultIntegration.value,
          setup_conversation_id: vm?.setup_conversation_id ?? "",
        },
  );
}
</script>

<style scoped>
.project-vm-modes {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  margin: 0;
  padding: 0;
  border: 0;
}
</style>
