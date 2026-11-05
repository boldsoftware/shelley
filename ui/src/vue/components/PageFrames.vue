<!-- The shown page tab's page, over a task's transcript or in a project's
     view: a positioned parent's whole area. Every page shown once
     stays mounted, hidden, so flipping keeps its state. A page is never
     Shelley's origin (pageFrame refuses it), so allow-same-origin keeps the
     page's own origin, and its cookies and storage, without reaching
     Shelley's DOM or storage; it can never navigate Shelley's window.
     Cookies ignore ports, though: a page on another port of Shelley's host
     is sent Shelley's cookies and can set them, framed here or not. -->
<template>
  <div v-show="active" class="page-frames" data-testid="page-frames">
    <template v-for="tab in tabs" :key="tab.key">
      <div v-if="mounted.has(tab.key)" v-show="active === tab.key" class="page-frame-pane">
        <div v-if="tab.error || tab.blocked" class="page-frame-message" role="alert">
          <p>{{ tab.error || tab.blocked }}</p>
          <a :href="tab.url" target="_blank" rel="noopener noreferrer" class="p-button">
            {{ t("pageTabOpenTitleInNewTab").replace("{title}", tab.title) }}
          </a>
        </div>
        <p v-else-if="!tab.frame" class="page-frame-message text-secondary">
          {{ t("pageTabChecking").replace("{url}", tab.url) }}
        </p>
        <template v-else>
          <p v-if="tab.frame.login && !hintDismissed.has(tab.key)" class="page-frame-hint">
            <span>
              {{ t("pageTabLoginHint") }}
              <a :href="tab.url" target="_blank" rel="noopener noreferrer">
                {{ t("pageTabOpenInNewTab") }}
              </a>
            </span>
            <button type="button" :aria-label="t('dismiss')" @click="dismissHint(tab.key)">
              <i class="pi pi-times" aria-hidden="true" />
            </button>
          </p>
          <iframe
            :key="tab.reloadedAt"
            :src="frameURL(tab.url, tab.reloadedAt)"
            :data-page-tab="tab.key"
            :title="tab.title"
            class="page-frame"
            sandbox="allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-same-origin allow-downloads allow-storage-access-by-user-activation"
            allow="clipboard-write; fullscreen"
            referrerpolicy="no-referrer"
            data-testid="page-frame"
          />
        </template>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref } from "vue";
import type { PageTabView } from "../composables/pageTabs";
import { frameURL } from "../../utils/pageTabs";
import { useI18n } from "../composables/i18n";

/** active: the shown tab's key, or null for none; mounted: the keys of the
 *  tabs whose frames stay mounted. */
defineProps<{ tabs: PageTabView[]; active: string | null; mounted: ReadonlySet<string> }>();
const { t } = useI18n();

const hintDismissed = ref(new Set<string>());
function dismissHint(key: string) {
  hintDismissed.value = new Set(hintDismissed.value).add(key);
}
</script>

<style scoped>
.page-frames {
  position: absolute;
  inset: 0;
  z-index: 5;
  background: var(--bg-base);
}
.page-frame-pane {
  display: flex;
  flex-direction: column;
  height: 100%;
}
.page-frame {
  flex: 1;
  border: none;
  background: white;
}
.page-frame-hint {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.5rem;
  margin: 0;
  padding: 0.25rem 1rem;
  border-bottom: 1px solid var(--border);
  color: var(--text-secondary);
  font-size: var(--font-size-12);
}
.page-frame-hint button {
  border: none;
  background: none;
  color: inherit;
  cursor: pointer;
}
.page-frame-message {
  margin: auto;
  max-width: 32rem;
  padding: 1rem;
  text-align: center;
  display: flex;
  flex-direction: column;
  gap: 1rem;
  align-items: center;
  overflow-wrap: anywhere;
}
</style>
