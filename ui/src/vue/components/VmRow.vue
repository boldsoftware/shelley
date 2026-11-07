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
      :title="vm.comment || vm.name"
      @click.exact.prevent="emit('select')"
    >
      <div class="conversation-title">
        <span class="vm-emoji" aria-hidden="true">{{ vm.emoji }}</span
        >{{ vm.name }}
      </div>
      <div class="conversation-preview" role="status">
        <template v-if="note">{{ note }}</template>
        <template v-else>
          <span :class="`vm-status vm-status-${vm.status === 'running' ? 'up' : 'down'}`" />
          {{ vm.status }} · {{ vm.public ? "public" : "private" }}
        </template>
      </div>
    </a>
    <div class="conversation-actions drawer-actions-row drawer-actions-top">
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
// Briefly replaces the status line: what was copied, or why it wasn't.
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
  color: inherit;
  text-decoration: none;
}
.vm-emoji {
  margin-right: 0.375rem;
}
.vm-status {
  display: inline-block;
  width: 0.5rem;
  height: 0.5rem;
  border-radius: 50%;
  margin-right: 0.25rem;
}
.vm-status-up {
  background: #22c55e;
}
.vm-status-down {
  background: var(--text-tertiary);
}
</style>
