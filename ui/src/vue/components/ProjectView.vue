<!-- A project picked in the drawer, in the main pane: one bar, like a
     conversation's, with its name (a click renames it) and its tabs, its web page (if it has one) and its
     settings, then the shown tab. The page is framed as a task's page tabs
     frame theirs, or says why it can't be and links to it. -->
<template>
  <section class="project-view" data-testid="project-view">
    <div class="header">
      <div class="header-left">
        <Button
          class="btn-icon hide-on-desktop"
          text
          severity="secondary"
          icon="pi pi-bars"
          :aria-label="t('openConversations')"
          @click="emit('open-drawer')"
        />
        <Button
          v-if="isDrawerCollapsed"
          class="btn-icon show-on-desktop-only"
          text
          severity="secondary"
          icon="pi pi-angle-double-right"
          :aria-label="t('expandSidebar')"
          v-tooltip.top="t('expandSidebar')"
          @click="emit('toggle-drawer-collapse')"
        />
        <ExeBrand v-if="workspaceMode && isDrawerCollapsed" class="show-on-desktop-only" />
        <h1 v-if="project" class="app-bar-title header-title header-title-yields">
          <EditableTitle
            :text="project.name"
            :label="t('rename')"
            editable
            :placeholder="t('projectNamePlaceholder')"
            :check="workspace.projectNameCheck(project.name)"
            :submit="renameProject"
          />
        </h1>
        <ViewTabs
          v-if="project"
          :tabs="tabs"
          :active="tab"
          :label="t('projectTabs')"
          @show="show"
        />
      </div>
      <div v-if="pageTab && tab === 'page'" class="header-actions">
        <Button
          class="btn-icon"
          text
          severity="secondary"
          icon="pi pi-refresh"
          :aria-label="t('reloadPage')"
          v-tooltip.bottom="t('reloadPage')"
          @click="reload"
        />
        <a
          :href="pageTab.url"
          target="_blank"
          rel="noopener noreferrer"
          class="p-button p-button-text p-button-secondary btn-icon"
          :aria-label="t('pageTabOpenTitleInNewTab').replace('{title}', pageTab.title)"
          v-tooltip.bottom="t('pageTabOpenInNewTab')"
        >
          <i class="pi pi-external-link" aria-hidden="true" />
        </a>
      </div>
    </div>
    <p v-if="!project" class="text-secondary project-view-loading">{{ t("loading") }}</p>
    <template v-else>
      <div v-if="pageTab" v-show="tab === 'page'" class="project-view-page">
        <PageFrames :tabs="[pageTab]" :active="tab === 'page' ? PAGE : null" :mounted="mounted" />
      </div>
      <ProjectSettings v-show="tab === 'settings'" :project="project" :renamed="() => renamed" />
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import Button from "primevue/button";
import ExeBrand from "./ExeBrand.vue";
import PageFrames from "./PageFrames.vue";
import EditableTitle from "./EditableTitle.vue";
import ViewTabs, { type ViewTab } from "./ViewTabs.vue";
import ProjectSettings from "./ProjectSettings.vue";
import { workspaceApi } from "../../services/workspace";
import { useFrameChecks } from "../composables/pageTabs";
import { projectPage, useWorkspace, type ProjectTab } from "../composables/workspace";
import { useI18n } from "../composables/i18n";
import { workspaceMode } from "../../utils/workspaceMode";

defineProps<{ isDrawerCollapsed: boolean }>();
const emit = defineEmits<{ "open-drawer": []; "toggle-drawer-collapse": [] }>();
const { t } = useI18n();
const workspace = useWorkspace();
// App keys this view by project.
const id = workspace.projectView.value!.id;
const project = computed(() => workspace.projectById(id));
// A project without a page shows its settings (App moves the view there).
const tab = computed<ProjectTab>(() =>
  project.value && projectPage(project.value)
    ? (workspace.projectView.value?.tab ?? "page")
    : "settings",
);
function show(next: ProjectTab) {
  workspace.projectView.value = { id, tab: next };
}

const PAGE = "page";
const { probe, view } = useFrameChecks();
const reloadedAt = ref(0);
const pageTab = computed(() => {
  const url = project.value ? projectPage(project.value) : "";
  if (!url) return null;
  let title = url;
  try {
    title = new URL(url).host;
  } catch {
    // The server only saves http(s) URLs.
  }
  return view({ key: PAGE, url, title, linked: 1 }, reloadedAt.value);
});
const tabs = computed(() => {
  const settings: ViewTab<ProjectTab> = {
    key: "settings",
    icon: "pi-cog",
    title: t("projectSettingsTab"),
  };
  const page = pageTab.value;
  return page
    ? [{ key: "page" as const, icon: "pi-globe", title: page.title, tooltip: page.url }, settings]
    : [settings];
});
// The frame mounts once the page is first shown, and stays mounted.
const mounted = ref(new Set<string>());
watch(
  [tab, () => pageTab.value?.url],
  ([shown, url]) => {
    if (shown !== "page" || !url || mounted.value.has(PAGE)) return;
    mounted.value = new Set([PAGE]);
    void probe(url, false);
  },
  { immediate: true },
);
// A changed page is checked afresh.
watch(
  () => pageTab.value?.url,
  (url, before) => {
    if (url && before && mounted.value.has(PAGE)) void probe(url, true);
  },
);
function reload() {
  const url = pageTab.value?.url;
  if (url) void probe(url, true).then(() => (reloadedAt.value = Date.now()));
}

// The latest rename, which saving the settings waits for.
let renamed = Promise.resolve();
function renameProject(name: string) {
  return (renamed = rename(name));
}
async function rename(name: string) {
  const saved = project.value;
  if (!saved) throw new Error("The project no longer exists.");
  // The saved project, renamed: the settings' unsaved edits stay unsaved.
  await workspaceApi.updateProject(saved.name, { ...saved, name });
  await workspace.refresh();
}
</script>

<style scoped>
.project-view {
  display: flex;
  flex-direction: column;
  flex: 1 1 auto;
  min-height: 0;
  height: 100%;
  background: var(--bg-base);
}
.project-view > .project-page {
  flex: 1 1 auto;
}
.project-view-page {
  flex: 1 1 auto;
  min-height: 0;
  position: relative;
}
.project-view-loading {
  margin: auto;
}
</style>
