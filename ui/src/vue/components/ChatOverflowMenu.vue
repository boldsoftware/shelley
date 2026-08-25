<!--
  ChatOverflowMenu.vue — the top-right "kebab" overflow menu.

  This is the first piece of the UI rebuilt on real PrimeVue *components*
  (the rest of the Vue world so far only consumes the PrimeVue *theme*). It
  replaces a hand-rolled dropdown — a `v-if` panel with a manual document
  `mousedown` outside-click listener and bespoke segmented-toggle rows — with:

    - <Popover> the dropdown surface (dismiss-on-outside-click + Esc + focus
                trap come for free, so we delete the manual handlers)
    - Native icon groups for compact view / theme / notification choices
    - <Select> the language picker

  The e2e DOM/ARIA contract is preserved so the shared Playwright specs keep
  passing in BOTH worlds:
    - root wrapper:  .chat-overflow-menu-wrapper
    - trigger:       button.btn-icon  (aria-label = t('moreOptions'))
    - action items:  button.overflow-menu-item  (matched by visible text)
  See e2e/agents-md-vim.spec.ts and e2e/diff-viewer-find.spec.ts.

  State the menu reads/writes lives in shared composables/services
  (markdownMode, theme, notifications), so this component owns it directly
  instead of taking a dozen props. Conversation-scoped actions (diffs, git
  graph, archive, export, …) are surfaced as events for ChatInterface to wire
  to its existing handlers.
-->
<template>
  <div class="chat-overflow-menu-wrapper">
    <Button
      class="btn-icon"
      text
      severity="secondary"
      :aria-label="t('moreOptions')"
      aria-haspopup="true"
      :aria-expanded="open"
      @click="toggle"
    >
      <OverflowDotsIcon />
      <span v-if="hasUpdate" class="version-update-dot" />
    </Button>

    <Popover
      ref="popoverRef"
      :pt="{ root: { class: 'chat-overflow-popover' }, content: { class: 'overflow-menu-panel' } }"
      @show="open = true"
      @hide="open = false"
    >
      <!-- Command palette (search everything / quick actions) -->
      <button class="overflow-menu-item" @click="onCommandPalette">
        <svg
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
          viewBox="0 0 24 24"
          class="chat-menu-icon"
          aria-hidden="true"
        >
          <path
            d="M7 9a2 2 0 1 1 2 -2v10a2 2 0 1 1 -2 -2h10a2 2 0 1 1 -2 2v-10a2 2 0 1 1 2 2h-10"
          />
        </svg>
        {{ t("commandMenu") }}
        <span class="overflow-menu-shortcut"
          ><kbd>{{ menuShortcutLabel("commandPalette") }}</kbd></span
        >
      </button>
      <div class="overflow-menu-divider" />

      <!-- Conversation / workspace actions -->
      <button v-if="hasCwd" class="overflow-menu-item" @click="onDiffs">
        <!-- Diffs: two rows of +/- changes -->
        <svg
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
          viewBox="0 0 24 24"
          class="chat-menu-icon"
          aria-hidden="true"
        >
          <path d="M4 7h4M6 5v4" />
          <path d="M14 7h6" />
          <path d="M4 17h6" />
          <path d="M14 17h6M17 15v4" />
        </svg>
        {{ t("diffs") }}
        <span class="overflow-menu-shortcut"
          ><kbd>{{ menuShortcutLabel("diffs") }}</kbd></span
        >
      </button>
      <button v-if="hasCwd" class="overflow-menu-item" @click="onGitGraph">
        <!-- Git graph: commits A (top) and B (top-right) branching from C (bottom) -->
        <svg
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
          viewBox="0 0 24 24"
          class="chat-menu-icon"
          aria-hidden="true"
        >
          <path d="M6 16.6V7.4" />
          <path d="M6 16.6C8 11 12 6 14.6 5" />
          <circle cx="6" cy="5" r="2.4" />
          <circle cx="17" cy="5" r="2.4" />
          <circle cx="6" cy="19" r="2.4" />
        </svg>
        {{ t("gitGraph") }}
        <span class="overflow-menu-shortcut"
          ><kbd>{{ menuShortcutLabel("gitGraph") }}</kbd></span
        >
      </button>
      <button class="overflow-menu-item" @click="onTerminal">
        <i class="pi pi-desktop chat-menu-icon" aria-hidden="true" />
        {{ t("terminal") }}
        <span class="overflow-menu-shortcut"
          ><kbd>{{ menuShortcutLabel("terminal") }}</kbd></span
        >
      </button>

      <!-- Custom server-provided links (icon is a raw SVG path) -->
      <button
        v-for="(link, index) in links"
        :key="index"
        class="overflow-menu-item"
        @click="onExternalLink(link.url)"
      >
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" class="chat-menu-icon">
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            :stroke-width="2"
            :d="
              link.icon_svg ||
              'M10 6H6a2 2 0 00-2 2v10a2 2 0 002 2h10a2 2 0 002-2v-4M14 4h6m0 0v6m0-6L10 14'
            "
          />
        </svg>
        {{ link.title }}
      </button>

      <template v-if="canArchive">
        <div class="overflow-menu-divider" />
        <button class="overflow-menu-item" @click="onArchive">
          <i class="pi pi-inbox chat-menu-icon" aria-hidden="true" />
          {{ t("archiveConversation") }}
          <span class="overflow-menu-shortcut"
            ><kbd>{{ menuShortcutLabel("archive") }}</kbd></span
          >
        </button>
      </template>

      <template v-if="canExport">
        <div class="overflow-menu-divider" />
        <button class="overflow-menu-item" @click="onExport">
          <i class="pi pi-download chat-menu-icon" aria-hidden="true" />
          {{ t("exportConversation") }}
          <span class="overflow-menu-shortcut"
            ><kbd>{{ menuShortcutLabel("export") }}</kbd></span
          >
        </button>
      </template>

      <div class="overflow-menu-divider" />
      <button class="overflow-menu-item" @click="onEditAgentsMd">
        <i class="pi pi-pencil chat-menu-icon" aria-hidden="true" />
        {{ t("editUserAgentsMd") }}
        <span class="overflow-menu-shortcut"
          ><kbd>{{ menuShortcutLabel("editAgentsMd") }}</kbd></span
        >
      </button>
      <button class="overflow-menu-item" @click="onEditFile">
        <i class="pi pi-file-edit chat-menu-icon" aria-hidden="true" />
        {{ t("editFile") }}
        <span
          v-tooltip.bottom="editFileShortcutTooltip"
          class="overflow-menu-shortcut"
          :class="{ 'overflow-menu-shortcut-inert': isFirefox }"
          ><kbd>{{ menuShortcutLabel("editFile") }}</kbd></span
        >
      </button>

      <div class="overflow-menu-divider" />
      <button class="overflow-menu-item" @click="onCheckVersion">
        <i class="pi pi-refresh chat-menu-icon" aria-hidden="true" />
        {{ t("checkForNewVersion") }}
        <span v-if="hasUpdate" class="version-menu-dot" />
        <span class="overflow-menu-shortcut"
          ><kbd>{{ menuShortcutLabel("checkVersion") }}</kbd></span
        >
      </button>

      <!-- Compact view/theme/notification controls -->
      <div class="overflow-menu-divider" />
      <div class="overflow-quick-controls">
        <div
          class="overflow-quick-control"
          data-testid="conversation-view-toggle"
          role="group"
          :aria-label="t('brevity')"
        >
          <span class="overflow-quick-label">{{ t("brevity") }}</span>
          <div class="overflow-choice-options">
            <button
              type="button"
              class="overflow-choice-option"
              :class="{ 'is-selected': conversationViewMode === 'all' }"
              :aria-label="t('seeAllMessages')"
              :aria-pressed="conversationViewMode === 'all'"
              :title="t('seeAllMessages')"
              @click="setConversationViewMode('all')"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" aria-hidden="true">
                <path d="M8 6h12M8 12h12M8 18h12" stroke-linecap="round" />
                <circle cx="4" cy="6" r="1.4" fill="currentColor" stroke="none" />
                <circle cx="4" cy="12" r="1.4" fill="currentColor" stroke="none" />
                <circle cx="4" cy="18" r="1.4" fill="currentColor" stroke="none" />
              </svg>
            </button>
            <button
              type="button"
              class="overflow-choice-option"
              :class="{ 'is-selected': conversationViewMode === 'end-of-turn' }"
              :aria-label="t('seeEndOfTurnMessagesOnly')"
              :aria-pressed="conversationViewMode === 'end-of-turn'"
              :title="t('seeEndOfTurnMessagesOnly')"
              @click="setConversationViewMode('end-of-turn')"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" aria-hidden="true">
                <path d="M8 7h12M8 17h12" stroke-linecap="round" />
                <circle cx="4" cy="7" r="1.4" fill="currentColor" stroke="none" />
                <path d="m2.7 17 1.1 1.1 2.3-2.5" stroke-linecap="round" stroke-linejoin="round" />
              </svg>
            </button>
          </div>
        </div>

        <div
          class="overflow-quick-control"
          data-testid="theme-cycle"
          role="group"
          :aria-label="t('look')"
        >
          <span class="overflow-quick-label">{{ t("look") }}</span>
          <div class="overflow-choice-options">
            <button
              v-for="choice in themeOrder"
              :key="choice"
              type="button"
              class="overflow-choice-option"
              :class="{ 'is-selected': theme === choice }"
              :aria-label="t(choice)"
              :aria-pressed="theme === choice"
              :title="t(choice)"
              @click="setTheme(choice)"
            >
              <i :class="['pi', themeIconFor(choice)]" aria-hidden="true" />
            </button>
          </div>
        </div>

        <div
          v-if="notificationSupported"
          class="overflow-quick-control"
          data-testid="notification-toggle"
          role="group"
          :aria-label="t('notifications')"
        >
          <span class="overflow-quick-label">{{ t("notifications") }}</span>
          <div class="overflow-choice-options">
            <button
              type="button"
              class="overflow-choice-option"
              :class="{ 'is-selected': !notifEnabled }"
              :aria-label="t('disableNotifications')"
              :aria-pressed="!notifEnabled"
              :title="t('disableNotifications')"
              @click="setNotifications(false)"
            >
              <i class="pi pi-bell-slash" aria-hidden="true" />
            </button>
            <button
              type="button"
              class="overflow-choice-option"
              :class="{ 'is-selected': notifEnabled }"
              :disabled="notifBlocked && !notifEnabled"
              :aria-label="
                notifBlocked && !notifEnabled ? t('blockedByBrowser') : t('enableNotifications')
              "
              :aria-pressed="notifEnabled"
              :title="
                notifBlocked && !notifEnabled ? t('blockedByBrowser') : t('enableNotifications')
              "
              @click="setNotifications(true)"
            >
              <i class="pi pi-bell" aria-hidden="true" />
            </button>
          </div>
        </div>
      </div>

      <!-- Language -->
      <div class="overflow-menu-divider" />
      <div class="overflow-menu-control">
        <div class="md-toggle-label">{{ t("language") }}</div>
        <Select
          v-model="lang"
          :options="languageOptions"
          option-label="label"
          option-value="locale"
          :aria-label="t('switchLanguage')"
          class="overflow-language-select"
          append-to="self"
          @update:model-value="onLangChange"
        >
          <template #value="{ value }">
            <span class="language-dropdown-flag">{{ languageFor(value).flag }}</span>
            <span>{{ languageFor(value).label }}</span>
          </template>
          <template #option="{ option }">
            <span class="language-dropdown-flag">{{ option.flag }}</span>
            <span>{{ option.label }}</span>
          </template>
        </Select>
      </div>
    </Popover>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import Popover from "primevue/popover";
import Button from "primevue/button";
import Select from "primevue/select";
import OverflowDotsIcon from "./OverflowDotsIcon.vue";
import type { Link } from "../../types";
import type { Locale } from "../../i18n/types";
import { useI18n } from "../composables/i18n";
import { useConversationView } from "../composables/conversationView";
import { menuShortcutLabel, isFirefox } from "../../utils/menuShortcuts";
import { type ThemeMode, getStoredTheme, setStoredTheme, applyTheme } from "../../services/theme";
import {
  isChannelEnabled,
  setChannelEnabled,
  getBrowserNotificationState,
  requestBrowserNotificationPermission,
} from "../../services/notifications";

defineProps<{
  hasCwd: boolean;
  links: Link[];
  canArchive: boolean;
  canExport: boolean;
  hasUpdate: boolean;
}>();

const emit = defineEmits<{
  (e: "open-command-palette"): void;
  (e: "open-diffs"): void;
  (e: "open-git-graph"): void;
  (e: "open-terminal"): void;
  (e: "open-external-link", url: string): void;
  (e: "archive"): void;
  (e: "export"): void;
  (e: "edit-agents-md"): void;
  (e: "edit-file"): void;
  (e: "check-version"): void;
}>();

const { t, locale, setLocale } = useI18n();
const { conversationViewMode, setConversationViewMode } = useConversationView();

// Edit File uses Cmd/Ctrl+Shift+P (VS Code parity). Firefox reserves that combo
// for "New Private Window" and never delivers it to the page, so the shortcut
// is inert there; explain that on hover rather than silently misleading users.
const editFileShortcutTooltip = computed(() =>
  isFirefox ? t("editFileShortcutFirefox") : t("editFileShortcut"),
);

const popoverRef = ref<InstanceType<typeof Popover> | null>(null);
const open = ref(false);

function toggle(event: MouseEvent) {
  popoverRef.value?.toggle(event);
}
function hide() {
  popoverRef.value?.hide();
}

// Each action emits its event, then closes the Popover. Kept as explicit
// one-liners (rather than a union-typed helper) so defineEmits' per-event
// overloads type-check cleanly.
const onCommandPalette = () => (emit("open-command-palette"), hide());
const onDiffs = () => (emit("open-diffs"), hide());
const onGitGraph = () => (emit("open-git-graph"), hide());
const onTerminal = () => (emit("open-terminal"), hide());
const onArchive = () => (emit("archive"), hide());
const onExport = () => (emit("export"), hide());
const onEditAgentsMd = () => (emit("edit-agents-md"), hide());
const onEditFile = () => (emit("edit-file"), hide());
const onCheckVersion = () => (emit("check-version"), hide());
function onExternalLink(url: string) {
  emit("open-external-link", url);
  hide();
}

const notificationSupported = typeof Notification !== "undefined";

// ---- Theme ----
const theme = ref<ThemeMode>(getStoredTheme());
const themeOrder: ThemeMode[] = ["system", "light", "dark"];
function themeIconFor(mode: ThemeMode): string {
  if (mode === "light") return "pi-sun";
  if (mode === "dark") return "pi-moon";
  return "pi-desktop";
}
function setTheme(mode: ThemeMode) {
  theme.value = mode;
  setStoredTheme(mode);
  applyTheme(mode);
}

// ---- Browser notifications (on / off) ----
const notifEnabled = ref<boolean>(isChannelEnabled("browser"));
const notifBlocked = ref(getBrowserNotificationState() === "denied");
async function setNotifications(enabled: boolean) {
  if (enabled === notifEnabled.value) return;
  if (!enabled) {
    setChannelEnabled("browser", false);
    notifEnabled.value = false;
    return;
  }
  notifEnabled.value = await requestBrowserNotificationPermission();
  notifBlocked.value = getBrowserNotificationState() === "denied";
}

// ---- Language picker ----
interface LanguageOption {
  locale: Locale;
  flag: string;
  label: string;
}
const languageOptions: LanguageOption[] = [
  { locale: "en", flag: "\uD83C\uDDFA\uD83C\uDDF8", label: "English" },
  { locale: "ja", flag: "\uD83C\uDDEF\uD83C\uDDF5", label: "\u65E5\u672C\u8A9E" },
  { locale: "fr", flag: "\uD83C\uDDEB\uD83C\uDDF7", label: "Fran\u00E7ais" },
  {
    locale: "ru",
    flag: "\uD83C\uDDF7\uD83C\uDDFA",
    label: "\u0420\u0443\u0441\u0441\u043A\u0438\u0439",
  },
  { locale: "es", flag: "\uD83C\uDDEA\uD83C\uDDF8", label: "Espa\u00F1ol" },
  { locale: "zh-CN", flag: "\uD83C\uDDE8\uD83C\uDDF3", label: "\u7B80\u4F53\u4E2D\u6587" },
  { locale: "zh-TW", flag: "\uD83C\uDDF9\uD83C\uDDFC", label: "\u7E41\u9AD4\u4E2D\u6587" },
  { locale: "vi", flag: "\uD83C\uDDFB\uD83C\uDDF3", label: "Ti\u1EBFng Vi\u1EC7t" },
  { locale: "upgoer5", flag: "\uD83D\uDE80", label: "Up-Goer Five" },
];
const lang = ref<Locale>(locale.value);
function languageFor(l: Locale): LanguageOption {
  return languageOptions.find((o) => o.locale === l) || languageOptions[0];
}
function onLangChange(l: Locale) {
  lang.value = l;
  setLocale(l);
}
</script>
