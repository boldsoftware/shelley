<!-- A task's chat and page tabs (workspace mode), beside its title. A page
     the browser can't frame here is a link that opens it in a new tab. Tabs
     that don't fit scroll, and a menu at the strip's end lists them all. -->
<template>
  <div class="page-tabs">
    <nav
      ref="strip"
      class="page-tabs-scroll"
      :aria-label="t('pageTabsLabel')"
      data-testid="page-tabs"
    >
      <span class="page-tab page-tab-chat" :class="{ active: !pages.active.value }">
        <button
          type="button"
          :aria-label="t('pageTabChat')"
          :aria-current="pages.active.value ? undefined : 'page'"
          @click="pages.select(null)"
        >
          <i class="pi pi-comments" aria-hidden="true" />
          <span class="page-tab-title">{{ t("pageTabChat") }}</span>
        </button>
      </span>
      <span
        v-for="tab in pages.tabs.value"
        :key="tab.key"
        class="page-tab"
        :class="{ active: pages.active.value === tab.key }"
        data-testid="page-tab"
      >
        <a
          v-if="tab.blocked"
          :href="tab.url"
          target="_blank"
          rel="noopener noreferrer"
          :title="`${tab.url}\n${tab.blocked} ${t('pageTabOpensInNewTab')}`"
        >
          <span class="page-tab-title">{{ tab.title }}</span>
          <i class="pi pi-external-link" aria-hidden="true" />
        </a>
        <button
          v-else
          type="button"
          :title="
            pages.active.value === tab.key ? `${tab.url}\n${t('pageTabClickToReload')}` : tab.url
          "
          :aria-current="pages.active.value === tab.key ? 'page' : undefined"
          @click="pages.active.value === tab.key ? pages.reload(tab) : pages.select(tab.key)"
        >
          <i class="pi pi-globe" aria-hidden="true" />
          <span class="page-tab-title">{{ tab.title }}</span>
        </button>
        <a
          v-if="pages.active.value === tab.key"
          class="page-tab-icon"
          :href="tab.url"
          target="_blank"
          rel="noopener noreferrer"
          :aria-label="t('pageTabOpenTitleInNewTab').replace('{title}', tab.title)"
          :title="t('pageTabOpenInNewTab')"
        >
          <i class="pi pi-external-link" aria-hidden="true" />
        </a>
        <button
          type="button"
          class="page-tab-icon"
          :aria-label="t('pageTabCloseTitle').replace('{title}', tab.title)"
          :title="t('pageTabClose')"
          @click="pages.close(tab)"
        >
          <i class="pi pi-times" aria-hidden="true" />
        </button>
      </span>
    </nav>
    <template v-if="overflowing">
      <Button
        class="btn-icon-sm page-tabs-more"
        text
        severity="secondary"
        size="small"
        :aria-label="t('pageTabsAll')"
        :title="t('pageTabsAll')"
        aria-haspopup="menu"
        :aria-expanded="menuOpen"
        @click="menu?.toggle($event)"
      >
        <i class="pi pi-chevron-down" aria-hidden="true" />
      </Button>
      <TieredMenu
        ref="menu"
        :model="menuItems"
        popup
        :aria-label="t('pageTabsAll')"
        :pt="navMenuPt('page-tabs-menu')"
        @show="menuOpen = true"
        @hide="menuOpen = false"
      />
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import type { MenuItem } from "primevue/menuitem";
import type { PageTabs } from "../composables/pageTabs";
import Button from "primevue/button";
import TieredMenu from "primevue/tieredmenu";
import { navMenuPt } from "./navMenu";
import { useI18n } from "../composables/i18n";

const props = defineProps<{ pages: PageTabs }>();
const { t } = useI18n();
const strip = ref<HTMLElement | null>(null);

const overflowing = ref(false);
const menu = ref<InstanceType<typeof TieredMenu> | null>(null);
const menuOpen = ref(false);
const measure = () => {
  const el = strip.value;
  overflowing.value = !!el && el.scrollWidth > el.clientWidth + 1;
};
const observer = new ResizeObserver(measure);
onMounted(() => {
  if (!strip.value) return;
  observer.observe(strip.value);
  for (const tab of strip.value.children) observer.observe(tab);
});
onBeforeUnmount(() => observer.disconnect());
watch(
  () => props.pages.tabs.value.map((tab) => tab.key).join(" "),
  async () => {
    await nextTick();
    observer.disconnect();
    if (!strip.value) return;
    observer.observe(strip.value);
    for (const tab of strip.value.children) observer.observe(tab);
  },
);
const menuItems = computed<MenuItem[]>(() => [
  {
    label: t("pageTabChat"),
    icon: props.pages.active.value ? "pi pi-comments" : "pi pi-check",
    command: () => props.pages.select(null),
  },
  ...props.pages.tabs.value.map((tab) =>
    tab.blocked
      ? { label: tab.title, icon: "pi pi-external-link", url: tab.url, target: "_blank" }
      : {
          label: tab.title,
          icon: props.pages.active.value === tab.key ? "pi pi-check" : "pi pi-globe",
          command: () => props.pages.select(tab.key),
        },
  ),
]);

// A narrow strip scrolls; keep the shown tab in it.
watch(props.pages.active, async () => {
  await nextTick();
  const tab = strip.value?.querySelector<HTMLElement>(".page-tab.active");
  if (!strip.value || !tab) return;
  const end = tab.offsetLeft + tab.offsetWidth - strip.value.clientWidth;
  strip.value.scrollLeft = Math.min(Math.max(strip.value.scrollLeft, end), tab.offsetLeft);
});
</script>

<style scoped>
.page-tabs {
  display: flex;
  align-items: center;
  min-width: 4rem;
  flex: 0 1 auto;
}
/* The menu of all tabs, when they don't fit. */
.page-tabs > :not(nav) {
  flex: none;
}
.page-tabs-scroll {
  display: flex;
  align-items: stretch;
  gap: 0.25rem;
  min-width: 0;
  flex: 0 1 auto;
  /* The tabs' offsetParent, for scrolling the shown one into view. */
  position: relative;
  overflow-x: auto;
  scrollbar-width: none;
}
.page-tab {
  display: flex;
  align-items: center;
  flex: none;
  max-width: 11rem;
  position: relative;
  /* No taller than the title row's buttons. */
  height: 1.875rem;
  border-bottom: 2px solid transparent;
  color: var(--text-secondary);
  font-size: var(--font-size-13);
  white-space: nowrap;
}
.page-tab.active {
  border-bottom-color: var(--primary, currentColor);
  color: var(--text-primary);
}
.page-tab > button,
.page-tab > a {
  display: flex;
  align-items: center;
  gap: 0.25rem;
  min-width: 0;
  height: 100%;
  padding: 0 0.25rem;
  border: none;
  background: none;
  color: inherit;
  font: inherit;
  text-decoration: none;
  cursor: pointer;
}
.page-tab-title {
  overflow: hidden;
  text-overflow: ellipsis;
}
@media (max-width: 640px) {
  /* The chat tab is its icon. */
  .page-tab-chat .page-tab-title {
    display: none;
  }
}
.page-tab i {
  font-size: 0.7rem;
}
.page-tab > .page-tab-icon {
  flex: 0 0 auto;
  padding: 0 0.2rem;
  opacity: 0.7;
}
.page-tab > .page-tab-icon:hover {
  opacity: 1;
}
/* An inactive tab's close button shows over its end on hover; on touch,
   select the tab first. */
.page-tab:not(.active) > button.page-tab-icon {
  position: absolute;
  right: 0;
  background: var(--bg-base);
  opacity: 1;
}
.page-tab:not(.active):not(:hover) > button.page-tab-icon {
  display: none;
}
</style>
