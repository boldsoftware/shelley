<!-- The main pane for a VM picked in the sidebar's prod tab, with tabs like a
     project's: its settings, first, its web page in a frame (or why the
     page can't be framed here, with a link to open it), and a terminal. -->
<template>
  <div class="full-height flex flex-col">
    <div class="header">
      <div class="header-left">
        <Button
          class="btn-icon hide-on-desktop"
          text
          severity="secondary"
          :aria-label="t('openConversations')"
          @click="emit('open-drawer')"
        >
          <i class="pi pi-bars" aria-hidden="true" />
        </Button>
        <Button
          v-if="isDrawerCollapsed"
          class="btn-icon show-on-desktop-only"
          text
          severity="secondary"
          :aria-label="t('expandSidebar')"
          v-tooltip.top="t('expandSidebar')"
          @click="emit('toggle-drawer-collapse')"
        >
          <i class="pi pi-angle-double-right" aria-hidden="true" />
        </Button>
        <ExeBrand v-if="isDrawerCollapsed" class="show-on-desktop-only" />
        <h1 class="app-bar-title header-title header-title-yields" :title="name">
          {{ vm?.emoji }} {{ name }}
        </h1>
        <ViewTabs :tabs="tabs" :active="tab" label="VM tabs" @show="emit('show', $event)" />
      </div>
      <div v-if="tab === 'page'" class="header-actions">
        <Button
          class="btn-icon"
          text
          severity="secondary"
          aria-label="Reload"
          v-tooltip.bottom="'Reload'"
          data-testid="vm-reload"
          @click="reload"
        >
          <i class="pi pi-refresh" aria-hidden="true" />
        </Button>
        <a
          v-if="vm"
          :href="vm.url"
          target="_blank"
          rel="noopener"
          class="p-button p-button-text p-button-secondary btn-icon"
          aria-label="Open in new tab"
          v-tooltip.bottom="'Open in new tab'"
          data-testid="vm-open"
        >
          <i class="pi pi-external-link" aria-hidden="true" />
        </a>
      </div>
      <div v-else-if="tab === 'terminal'" class="header-actions">
        <Button
          class="btn-icon"
          text
          severity="secondary"
          aria-label="New session"
          v-tooltip.bottom="'New session'"
          data-testid="vm-terminal-restart"
          @click="terminalSession++"
        >
          <i class="pi pi-refresh" aria-hidden="true" />
        </Button>
      </div>
    </div>
    <VmSettings
      v-if="tab === 'settings'"
      :name="name"
      @renamed="emit('renamed', $event)"
      @deleted="emit('deleted')"
      @open="emit('open', $event)"
    />
    <div v-show="tab === 'page'" class="vm-body">
      <p v-if="loading" class="text-secondary vm-message">Loading…</p>
      <div v-else-if="error || blocked" class="vm-message" role="alert" data-testid="vm-blocked">
        <p>{{ error || blocked }}</p>
        <a v-if="vm" :href="vm.url" target="_blank" rel="noopener" class="p-button">
          Open {{ vm.url }} in a new tab
        </a>
      </div>
      <iframe
        v-else-if="vm"
        :key="frameKey"
        :src="vm.url"
        :title="`${name} web page`"
        class="vm-frame"
        data-testid="vm-frame"
      />
    </div>
    <!-- Opened by its tab, the shell lasts while this VM's view does. -->
    <VmTerminal
      v-if="terminalSession"
      v-show="tab === 'terminal'"
      :key="`${name} ${terminalSession}`"
      :name="name"
      :visible="tab === 'terminal'"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import Button from "primevue/button";
import ExeBrand from "./ExeBrand.vue";
import ViewTabs, { type ViewTab } from "./ViewTabs.vue";
import VmSettings from "./VmSettings.vue";
import VmTerminal from "./VmTerminal.vue";
import { useI18n } from "../composables/i18n";
import { exeVmsApi, exeVmsChanged, type ExeVM, type VmTab } from "../../services/exeVms";
import { errorMessage } from "../../utils/errorMessage";

const props = defineProps<{ name: string; tab: VmTab; isDrawerCollapsed: boolean }>();
const emit = defineEmits<{
  "open-drawer": [];
  "toggle-drawer-collapse": [];
  show: [tab: VmTab];
  renamed: [name: string];
  deleted: [];
  open: [name: string];
}>();
const { t } = useI18n();

const vm = ref<ExeVM | null>(null);
const blocked = ref("");
const error = ref("");
const loading = ref(false);
const frameKey = ref(0);
// Bumped for each new shell; 0 until the Terminal tab first opens.
const terminalSession = ref(0);
watch(
  () => props.tab,
  (tab) => {
    if (tab === "terminal" && !terminalSession.value) terminalSession.value = 1;
  },
  { immediate: true },
);

// Probes framing again too: a VM may have been made public or private.
async function load() {
  const name = props.name;
  loading.value = true;
  error.value = "";
  blocked.value = "";
  try {
    const [list, reason] = await Promise.all([exeVmsApi.list(false), exeVmsApi.frameBlocked(name)]);
    if (name !== props.name) return;
    vm.value = list.vms.find((v) => v.name === name) ?? null;
    blocked.value = reason;
    frameKey.value++;
  } catch (err) {
    if (name !== props.name) return;
    error.value = errorMessage(err);
  } finally {
    if (name === props.name) loading.value = false;
  }
}

function reload() {
  void load();
}

const tabs = computed<ViewTab<VmTab>[]>(() => {
  let host = props.name;
  try {
    if (vm.value) host = new URL(vm.value.url).host;
  } catch {
    // exe.dev lists the VM's URL; its name will do.
  }
  return [
    { key: "settings", icon: "pi-cog", title: "Settings" },
    { key: "page", icon: "pi-globe", title: host, tooltip: vm.value?.url, yields: true },
    { key: "terminal", icon: "pi-code", title: "Terminal", tooltip: vm.value?.ssh_command },
  ];
});
// A change in the settings may have made the page public or private.
watch(exeVmsChanged, () => void load());

watch(
  () => props.name,
  () => {
    vm.value = null;
    terminalSession.value = props.tab === "terminal" ? 1 : 0;
    void load();
  },
  { immediate: true },
);
</script>

<style scoped>
.vm-body {
  flex: 1;
  min-height: 0;
  display: flex;
}
.vm-frame {
  flex: 1;
  border: none;
  background: white;
}
.vm-message {
  margin: auto;
  max-width: 32rem;
  padding: 1rem;
  text-align: center;
  display: flex;
  flex-direction: column;
  gap: 1rem;
  align-items: center;
}
</style>
