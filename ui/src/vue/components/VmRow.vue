<template>
  <div
    class="conversation-item"
    :class="{ active }"
    data-testid="vm-row"
    @contextmenu="menu?.open($event)"
  >
    <a
      :href="`/vm/${encodeURIComponent(vm.name)}`"
      class="vm-row-link drawer-conversation-item-flex-container"
      :aria-current="active ? 'page' : undefined"
      :title="summary"
      :aria-label="summary"
      @click.exact.prevent="emit('select')"
    >
      <span class="vm-emoji" aria-hidden="true">{{ vm.emoji }}</span>
      <span class="conversation-title vm-name" role="status">{{ note || vm.name }}</span>
      <span v-if="vm.status !== 'running'" class="vm-meta">{{ vm.status }}</span>
      <i v-if="vm.public" class="pi pi-globe vm-meta" aria-hidden="true" />
      <span
        :class="`vm-status vm-status-${vm.status === 'running' ? 'up' : 'down'}`"
        aria-hidden="true"
      />
    </a>
    <div class="conversation-actions drawer-actions-row">
      <NavActionsMenu
        ref="menu"
        :items="items"
        :label="`${vm.name} actions`"
        testid="vm-actions-menu"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref } from "vue";
import type { MenuItem } from "primevue/menuitem";
import NavActionsMenu from "./NavActionsMenu.vue";
import type { ExeVM } from "../../services/exeVms";
import { errorMessage } from "../../utils/errorMessage";

const props = defineProps<{ vm: ExeVM; active: boolean }>();
const emit = defineEmits<{ select: [] }>();

const menu = ref<InstanceType<typeof NavActionsMenu> | null>(null);
// One line per VM: running and private are the norm, so only the exceptions
// show (the status word, a globe for public); the tooltip spells it all out.
const summary = computed(() =>
  [
    `${props.vm.name} · ${props.vm.status} · ${props.vm.public ? "public" : "private"}`,
    props.vm.comment,
  ]
    .filter(Boolean)
    .join("\n"),
);
// Briefly replaces the name: what was copied, or why it wasn't.
const note = ref("");
let noteTimer: ReturnType<typeof setTimeout> | undefined;
onUnmounted(() => clearTimeout(noteTimer));

async function copy(what: string, text: string) {
  try {
    await navigator.clipboard.writeText(text);
    note.value = `Copied ${what}`;
  } catch (err) {
    note.value = `Couldn't copy ${what}: ${errorMessage(err)}`;
  }
  clearTimeout(noteTimer);
  noteTimer = setTimeout(() => (note.value = ""), 2000);
}

// Read-only: a VM changes in its view's settings.
const items = computed<MenuItem[]>(() => [
  {
    label: "Open in new tab",
    icon: "pi pi-external-link",
    disabled: !props.vm.url,
    command: () => window.open(props.vm.url, "_blank", "noopener"),
  },
  {
    label: "Copy URL",
    icon: "pi pi-link",
    disabled: !props.vm.url,
    command: () => void copy("URL", props.vm.url),
  },
  {
    label: "Copy SSH command",
    icon: "pi pi-copy",
    disabled: !props.vm.ssh_command,
    command: () => void copy("SSH command", props.vm.ssh_command),
  },
]);
</script>

<style scoped>
.vm-row-link {
  display: flex;
  align-items: center;
  gap: 0.375rem;
  color: inherit;
  text-decoration: none;
}
.vm-emoji {
  flex-shrink: 0;
  width: 1.25em;
  text-align: center;
}
.vm-name {
  flex: 1;
  min-width: 0;
}
.vm-meta {
  flex-shrink: 0;
  font-size: var(--font-size-12);
  color: var(--text-tertiary);
}
.active .vm-meta {
  color: rgba(255, 255, 255, 0.8);
}
.vm-status {
  display: inline-block;
  width: 0.5rem;
  height: 0.5rem;
  flex-shrink: 0;
  border-radius: 50%;
}
.vm-status-up {
  background: #22c55e;
}
.vm-status-down {
  background: var(--text-tertiary);
}
.active .vm-status-down {
  background: rgba(255, 255, 255, 0.8);
}
</style>
