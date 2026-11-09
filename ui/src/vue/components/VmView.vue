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
          aria-label="Reconnect"
          v-tooltip.bottom="'Reconnect'"
          data-testid="vm-terminal-restart"
          @click="reconnect"
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
    <!-- Opened by its tab: the VM's web terminal sessions
         (<vm>.xterm.exe.xyz's tabs). Each one picked stays attached while
         this VM's view lasts, so switching back to it is instant. -->
    <div v-if="terminalOpen" v-show="tab === 'terminal'" class="vm-terminals">
      <div class="vm-sessions" role="tablist" aria-label="Terminal sessions">
        <span v-for="s in sessions" :key="s" class="vm-session" :data-selected="s === session">
          <button
            type="button"
            role="tab"
            class="vm-session-name"
            :aria-selected="s === session"
            data-testid="vm-terminal-session"
            @click="session = s"
          >
            {{ s }}
          </button>
          <button
            type="button"
            class="vm-session-close"
            :aria-label="`Close session ${s}`"
            v-tooltip.bottom="`Close session ${s}`"
            data-testid="vm-terminal-close"
            @click="closeSession(s)"
          >
            <i class="pi pi-times" aria-hidden="true" />
          </button>
        </span>
        <Button
          class="btn-icon"
          text
          severity="secondary"
          size="small"
          aria-label="New session"
          v-tooltip.bottom="'New session'"
          data-testid="vm-terminal-new"
          @click="newSession"
        >
          <i class="pi pi-plus" aria-hidden="true" />
        </Button>
        <span v-if="sessionsError" class="text-secondary" role="alert">{{ sessionsError }}</span>
      </div>
      <VmTerminal
        v-for="s in attached"
        :key="`${name} ${s} ${reconnects}`"
        :name="name"
        :session="s"
        :visible="tab === 'terminal' && s === session"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import Button from "primevue/button";
import ExeBrand from "./ExeBrand.vue";
import { mintSessionName } from "../../utils/terminalSessionName";
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
// The terminal: false until its tab first opens.
const terminalOpen = ref(false);
// The VM's web terminal sessions, as last listed, and the one attached.
const sessions = ref<string[]>([]);
const session = ref("");
// The sessions picked since the terminal opened, each still attached.
const attached = ref<string[]>([]);
const sessionsError = ref("");
// Bumped for each reconnect.
const reconnects = ref(0);

// The session last attached on each VM, which its terminal opens on while
// it lasts, as the web terminal's does.
const lastSessionKey = (name: string) => `shelley:vm-terminal-session:${name}`;
watch(session, (s) => {
  if (!s) return;
  localStorage.setItem(lastSessionKey(props.name), s);
  if (!attached.value.includes(s)) attached.value = [...attached.value, s];
});

// Lists the sessions; unless one is attached, attaches to the last one
// attached here, else the newest, else a new one.
async function loadSessions() {
  const name = props.name;
  sessionsError.value = "";
  try {
    const list = await exeVmsApi.terminalSessions(name);
    if (name !== props.name) return;
    if (session.value && !list.includes(session.value)) list.push(session.value);
    sessions.value = list;
    const last = localStorage.getItem(lastSessionKey(name)) ?? "";
    session.value ||= list.includes(last) ? last : (list.at(-1) ?? mintSessionName(list));
  } catch (err) {
    if (name !== props.name) return;
    sessionsError.value = errorMessage(err);
  }
}

function newSession() {
  const name = mintSessionName(sessions.value);
  sessions.value = [...sessions.value, name];
  session.value = name;
}

// Ends session s; closing the attached one attaches to the newest left.
async function closeSession(s: string) {
  const name = props.name;
  sessionsError.value = "";
  try {
    await exeVmsApi.closeTerminalSession(name, s);
  } catch (err) {
    if (name === props.name) sessionsError.value = errorMessage(err);
    return;
  }
  if (name !== props.name) return;
  sessions.value = sessions.value.filter((x) => x !== s);
  attached.value = attached.value.filter((x) => x !== s);
  if (session.value === s) session.value = sessions.value.at(-1) ?? "";
}

function reconnect() {
  reconnects.value++;
  void loadSessions();
}

// Opens the terminal the first time its tab shows.
function openTerminal() {
  if (props.tab !== "terminal" || terminalOpen.value) return;
  terminalOpen.value = true;
  void loadSessions();
}
watch(() => props.tab, openTerminal);

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
    terminalOpen.value = false;
    sessions.value = [];
    session.value = "";
    attached.value = [];
    openTerminal();
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
.vm-terminals {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}
.vm-sessions {
  display: flex;
  align-items: center;
  gap: 0.25rem;
  padding: 0.25rem 0.5rem;
  overflow-x: auto;
  border-bottom: 1px solid var(--p-content-border-color);
}
.vm-session {
  display: inline-flex;
  align-items: center;
  border: 1px solid transparent;
  border-radius: 0.375rem;
  color: var(--p-text-muted-color);
  white-space: nowrap;
}
.vm-session[data-selected="true"] {
  border-color: var(--p-content-border-color);
  color: var(--p-text-color);
}
.vm-session button {
  border: none;
  background: none;
  color: inherit;
  cursor: pointer;
}
.vm-session-name {
  padding: 0.125rem 0.25rem 0.125rem 0.5rem;
}
.vm-session-close {
  padding: 0.125rem 0.375rem 0.125rem 0.125rem;
  font-size: 0.7rem;
  opacity: 0.6;
}
.vm-session-close:hover {
  opacity: 1;
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
