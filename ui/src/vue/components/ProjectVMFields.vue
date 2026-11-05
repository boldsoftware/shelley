<!-- Where a project's conversations run: their own VMs (copies of a base,
     new empty ones, or copies of a base an agent automanages; recommended in
     workspace mode), this Shelley host (where new projects start, so naming
     one creates no VM), or one shared machine (an exe.dev VM reached through
     an integration, or any host reached with ssh). Part of the project
     editor. Own VMs need an exe.dev integration, unless the project already
     has them. -->
<template>
  <div v-if="heading" class="project-section-heading">
    <h3>{{ Nouns }} run on</h3>
  </div>
  <fieldset class="project-vm-modes" :disabled="disabled">
    <legend class="sr-only">{{ Nouns }} run on</legend>
    <label
      v-for="option in shownModes"
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
    <label for="project-vm-base">Base VM</label>
    <!-- Automanaged, the base is the agent's: shown, not edited. -->
    <input
      id="project-vm-base"
      :value="vm.base"
      :placeholder="
        automanage ? 'Created by the setup task' : 'Optional: leave empty for new, empty VMs'
      "
      autocomplete="off"
      spellcheck="false"
      :readonly="automanage"
      :disabled="disabled"
      @input="update({ base: ($event.target as HTMLInputElement).value })"
    />
    <label class="project-checkbox">
      <input type="checkbox" :checked="automanage" :disabled="disabled" @change="setAutomanage" />
      Automanage (experimental)
    </label>
    <p class="project-muted">
      <template v-if="automanage && vm.base">
        Each {{ noun }}'s VM is a copy of {{ vm.base }}, the base an agent set up.
      </template>
      <template v-else-if="automanage">
        Saving starts a setup {{ noun }}. Its agent creates a VM through the integration, clones the
        repositories, installs toolchains and warms caches, then saves it as the base. New
        {{ nouns }} wait until it is done.
      </template>
      <template v-else-if="vm.base">
        Each {{ noun }}'s VM is a copy of this existing VM, with its checkouts, toolchains and
        caches.
      </template>
      <template v-else>
        Each {{ noun }} starts on a new, empty VM. Nothing is cloned or installed: its agent sets up
        what its work needs.
      </template>
    </p>
  </template>
  <template v-if="vm && mode === 'shared'">
    <template v-if="vias.length > 1">
      <label for="project-vm-via">Reached through</label>
      <select
        id="project-vm-via"
        :value="vm.integration"
        :disabled="disabled"
        @change="update({ integration: ($event.target as HTMLSelectElement).value })"
      >
        <option v-for="name in vias" :key="name" :value="name">
          {{ name ? `exe.dev, via ${name}` : "ssh, with this host's SSH configuration" }}
        </option>
      </select>
    </template>
    <label for="project-vm-machine">{{ vm.integration ? "VM" : "SSH destination" }}</label>
    <input
      id="project-vm-machine"
      :value="vm.shared_vm"
      required
      :placeholder="vm.integration ? 'my-vm' : 'user@host'"
      autocomplete="off"
      spellcheck="false"
      :disabled="disabled"
      @input="update({ shared_vm: ($event.target as HTMLInputElement).value })"
    />
    <p class="project-muted">
      <template v-if="!vm.integration">
        Connects with this host's SSH configuration, keys and agent, so a host or alias that
        <code>ssh</code> reaches here works, including Tailscale SSH.
      </template>
      <template v-else>An existing VM on your exe.dev account.</template>
      The browser, read_image and uploads stay on this host; agents copy files between it and the
      machine.
    </p>
  </template>
  <template v-if="vm && mode === 'own'">
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

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import ProjectVMSetup from "./ProjectVMSetup.vue";
import { workspaceApi, type ProjectVM } from "../../services/workspace";
import { wording, workspaceMode } from "../../utils/workspaceMode";

const noun = wording("conversation", "task");
const nouns = wording("conversations", "tasks");
const Nouns = wording("Conversations", "Tasks");

export type VMMode = "host" | "own" | "shared";
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
    help: `Each ${noun} gets its own VM, so they never collide. Archiving a ${noun} deletes its VM.`,
  },
  {
    value: "host",
    icon: "pi-server",
    label: "This Shelley host",
    help: `${Nouns} share this machine and its checkouts.`,
  },
  {
    value: "shared",
    icon: "pi-users",
    label: "One shared machine",
    help: `Every ${noun} works on one existing exe.dev VM or SSH host, alongside the others. Shelley never creates or deletes it.`,
  },
];
const integrations = ref<string[]>([]);
// A project on its own VMs keeps that mode after picking another, so the
// choice can be undone on a host with no integrations.
const hadOwnVMs = !!props.vm && !props.vm.shared_vm;
const shownModes = computed(() =>
  modes.filter((option) => option.value !== "own" || hadOwnVMs || integrations.value.length > 0),
);
// A shared machine is reached through any integration (including the
// project's own) or ssh (integration "").
const vias = computed(() => {
  const names = [...integrations.value];
  if (props.vm?.integration && !names.includes(props.vm.integration))
    names.push(props.vm.integration);
  return [...names, ""];
});
const defaultIntegration = ref("");
const error = ref("");
// Where conversations run follows from the VM: none means this host, a
// shared_vm means that machine (shared mode keeps it while being typed
// in, from the radio), and anything else means their own VMs.
const sharedChosen = ref(!!props.vm?.shared_vm);
const mode = computed<VMMode>(() => (!props.vm ? "host" : sharedChosen.value ? "shared" : "own"));
// The base an agent set up: turning automanage back on keeps it.
const managedBase = props.vm?.managed_base_vm ? props.vm.base : "";
const automanage = computed(() => !!props.vm?.managed_base_vm);
const loaded = workspaceApi.exeDevIntegrations().then(
  (result) => {
    integrations.value = result.integrations;
    defaultIntegration.value = result.default;
  },
  (err: unknown) => {
    error.value = err instanceof Error ? err.message : String(err);
  },
);
// Own VMs chosen before the integrations loaded get the default then; a
// shared machine without one is an SSH host.
onMounted(() =>
  loaded.then(() => {
    if (props.vm && mode.value === "own" && !props.vm.integration)
      update({ integration: defaultIntegration.value });
  }),
);
function update(change: Partial<ProjectVM>) {
  if (props.vm) emit("update:vm", { ...props.vm, ...change });
}
function setAutomanage(event: Event) {
  const on = (event.target as HTMLInputElement).checked;
  update({ managed_base_vm: on, base: on ? managedBase : "" });
}
function setMode(next: VMMode) {
  sharedChosen.value = next === "shared";
  emit(
    "update:vm",
    next === "host"
      ? null
      : {
          base: "",
          managed_base_vm: false,
          shared_vm: "",
          integration: props.vm?.integration || defaultIntegration.value,
          setup_conversation_id: "",
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
