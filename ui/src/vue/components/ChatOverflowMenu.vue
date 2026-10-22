<!--
  Desktop keeps workspace actions and the VM link in the header;
  mobile moves them into this menu.
  Settings live in shared composables/services; conversation actions emit
  events for ChatInterface to wire to its existing handlers.
-->
<template>
  <div class="chat-workspace-actions">
    <button
      v-if="hasCwd"
      type="button"
      class="btn-icon chat-workspace-action"
      :aria-label="t('diffs')"
      :title="`${t('diffs')} (${menuShortcutLabel('diffs')})`"
      @click="onDiffs"
    >
      <WorkspaceActionIcon action="diffs" />
    </button>
    <button
      v-if="hasCwd"
      type="button"
      class="btn-icon chat-workspace-action"
      :aria-label="t('gitGraph')"
      :title="`${t('gitGraph')} (${menuShortcutLabel('gitGraph')})`"
      @click="onGitGraph"
    >
      <WorkspaceActionIcon action="gitGraph" />
    </button>
    <button
      type="button"
      class="btn-icon chat-workspace-action"
      :aria-label="t('terminal')"
      :title="`${t('terminal')} (${menuShortcutLabel('terminal')})`"
      @click="onTerminal"
    >
      <WorkspaceActionIcon action="terminal" />
    </button>
    <span v-if="vmLink" class="chat-workspace-divider" aria-hidden="true" />
    <a
      v-if="vmLink"
      class="btn-icon chat-workspace-action"
      :href="vmLink.url"
      :aria-label="vmLink.title"
      :title="vmLink.title"
      target="_blank"
      rel="noopener noreferrer"
    >
      <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="2"
          d="M10 6H6a2 2 0 00-2 2v10a2 2 0 002 2h10a2 2 0 002-2v-4M14 4h6m0 0v6m0-6L10 14"
        />
      </svg>
    </a>
  </div>
  <slot />
  <div class="chat-overflow-menu-wrapper">
    <Button
      ref="triggerRef"
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
      <button v-if="hasCwd" class="overflow-menu-item overflow-mobile-action" @click="onDiffs">
        <WorkspaceActionIcon action="diffs" class="chat-menu-icon" />
        {{ t("diffs") }}
      </button>
      <button v-if="hasCwd" class="overflow-menu-item overflow-mobile-action" @click="onGitGraph">
        <WorkspaceActionIcon action="gitGraph" class="chat-menu-icon" />
        {{ t("gitGraph") }}
      </button>
      <button class="overflow-menu-item overflow-mobile-action" @click="onTerminal">
        <WorkspaceActionIcon action="terminal" class="chat-menu-icon" />
        {{ t("terminal") }}
      </button>
      <button class="overflow-menu-item" @click="onCommandPalette">
        <WorkspaceActionIcon action="commandPalette" class="chat-menu-icon" />
        {{ t("commandMenu") }}
        <span class="overflow-menu-shortcut hide-on-mobile"
          ><kbd>{{ menuShortcutLabel("commandPalette") }}</kbd></span
        >
      </button>
      <button
        v-for="(link, index) in links"
        :key="index"
        class="overflow-menu-item"
        :class="{ 'overflow-mobile-action': link === vmLink }"
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
      <div class="overflow-menu-divider" />
      <button class="overflow-menu-item" @click="onEditFile">
        <WorkspaceActionIcon action="editFile" class="chat-menu-icon" />
        {{ t("editFile") }}
        <span class="overflow-menu-shortcut hide-on-mobile"
          ><kbd>{{ menuShortcutLabel("editFile") }}</kbd></span
        >
      </button>
      <slot name="models" :hide="hide" />
      <button v-if="showDirectory" class="overflow-menu-item" @click="onDirectory">
        <i class="pi pi-folder chat-menu-icon" aria-hidden="true" />
        {{ t("directory") }}
        <span class="overflow-menu-cwd" :title="cwd">{{ tildifyPath(cwd) }}</span>
      </button>
      <button v-if="canExport" class="overflow-menu-item" @click="onExport">
        <i class="pi pi-download chat-menu-icon" aria-hidden="true" />
        {{ t("exportConversation") }}
        <span class="overflow-menu-shortcut hide-on-mobile"
          ><kbd>{{ menuShortcutLabel("export") }}</kbd></span
        >
      </button>
      <button v-if="canArchive" class="overflow-menu-item" @click="onArchive">
        <i class="pi pi-inbox chat-menu-icon" aria-hidden="true" />
        {{ t("archiveConversation") }}
        <span class="overflow-menu-shortcut hide-on-mobile"
          ><kbd>{{ menuShortcutLabel("archive") }}</kbd></span
        >
      </button>
      <div class="overflow-menu-divider" />
      <button class="overflow-menu-item" @click="onEditAgentsMd">
        <i class="pi pi-pencil chat-menu-icon" aria-hidden="true" />
        {{ t("editUserAgentsMd") }}
        <span class="overflow-menu-shortcut hide-on-mobile"
          ><kbd>{{ menuShortcutLabel("editAgentsMd") }}</kbd></span
        >
      </button>
      <button class="overflow-menu-item" @click="onMcpServers">
        <i class="pi pi-server chat-menu-icon" aria-hidden="true" />
        {{ t("mcpServers") }}
      </button>
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
              v-if="messageUser"
              type="button"
              class="overflow-choice-option"
              :class="{ 'is-selected': conversationViewMode === 'brief' }"
              :aria-label="t('seeChatOnly')"
              :aria-pressed="conversationViewMode === 'brief'"
              :title="t('seeChatOnly')"
              data-testid="conversation-view-brief"
              @click="setConversationViewMode('brief')"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" aria-hidden="true">
                <path
                  d="M4 6.5A2.5 2.5 0 0 1 6.5 4h11A2.5 2.5 0 0 1 20 6.5v7a2.5 2.5 0 0 1-2.5 2.5H10l-4 4v-4h0.5A2.5 2.5 0 0 1 4 13.5z"
                  stroke-linejoin="round"
                />
              </svg>
            </button>
            <button
              v-else
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
      <button class="overflow-menu-item" aria-haspopup="dialog" @click="onLanguagePicker">
        <i class="pi pi-globe chat-menu-icon" aria-hidden="true" />
        {{ t("switchLanguage") }}
        <span class="overflow-menu-language">{{ currentLanguage.label }}</span>
      </button>
      <button class="overflow-menu-item" @click="onCheckVersion">
        <i class="pi pi-refresh chat-menu-icon" aria-hidden="true" />
        {{ t("checkForNewVersion") }}
        <span v-if="hasUpdate" class="version-menu-dot" />
        <span class="overflow-menu-shortcut hide-on-mobile"
          ><kbd>{{ menuShortcutLabel("checkVersion") }}</kbd></span
        >
      </button>
    </Popover>

    <Modal
      :is-open="languagePickerOpen"
      :title="t('switchLanguage')"
      class-name="language-picker-modal"
      @close="languagePickerOpen = false"
    >
      <div
        ref="languageOptionsRef"
        class="language-picker-options"
        role="group"
        :aria-label="t('language')"
      >
        <button
          v-for="option in languageOptions"
          :key="option.locale"
          type="button"
          class="language-picker-option"
          :aria-pressed="locale === option.locale"
          @click="onLangChange(option.locale)"
        >
          <span class="language-dropdown-flag" aria-hidden="true">{{ option.flag }}</span>
          <span>{{ option.label }}</span>
          <i
            v-if="locale === option.locale"
            class="pi pi-check language-picker-check"
            aria-hidden="true"
          />
        </button>
      </div>
    </Modal>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, toRef } from "vue";
import Popover from "primevue/popover";
import Button from "primevue/button";
import Modal from "./Modal.vue";
import OverflowDotsIcon from "./OverflowDotsIcon.vue";
import WorkspaceActionIcon from "./WorkspaceActionIcon.vue";
import type { Link } from "../../types";
import type { Locale } from "../../i18n/types";
import { useI18n } from "../composables/i18n";
import { useConversationView } from "../composables/conversationView";
import { menuShortcutLabel } from "../../utils/menuShortcuts";
import { tildifyPath } from "../../utils/tildify";
import { type ThemeMode, getStoredTheme, setStoredTheme, applyTheme } from "../../services/theme";
import {
  isChannelEnabled,
  setChannelEnabled,
  getBrowserNotificationState,
  requestBrowserNotificationPermission,
} from "../../services/notifications";

const props = defineProps<{
  hasCwd: boolean;
  showDirectory: boolean;
  cwd: string;
  links: Link[];
  canArchive: boolean;
  canExport: boolean;
  hasUpdate: boolean;
  // The conversation has the message_user tool: offer the brief (chat only)
  // view instead of end-of-turn.
  messageUser: boolean;
}>();

const emit = defineEmits<{
  (e: "open-command-palette"): void;
  (e: "open-directory-picker"): void;
  (e: "open-diffs"): void;
  (e: "open-git-graph"): void;
  (e: "open-terminal"): void;
  (e: "open-external-link", url: string): void;
  (e: "archive"): void;
  (e: "export"): void;
  (e: "edit-agents-md"): void;
  (e: "edit-file"): void;
  (e: "open-mcp-servers"): void;
  (e: "check-version"): void;
}>();

const { t, locale, setLocale } = useI18n();
const { conversationViewMode, setConversationViewMode } = useConversationView(
  toRef(props, "messageUser"),
);

const triggerRef = ref<{ $el: HTMLButtonElement } | null>(null);
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
const onDirectory = () => (emit("open-directory-picker"), hide());
const onDiffs = () => (emit("open-diffs"), hide());
const onGitGraph = () => (emit("open-git-graph"), hide());
const onTerminal = () => (emit("open-terminal"), hide());
const onArchive = () => (emit("archive"), hide());
const onExport = () => (emit("export"), hide());
const onEditAgentsMd = () => (emit("edit-agents-md"), hide());
const onEditFile = () => (emit("edit-file"), hide());
const onMcpServers = () => (emit("open-mcp-servers"), hide());
const onCheckVersion = () => (emit("check-version"), hide());
function onExternalLink(url: string) {
  emit("open-external-link", url);
  hide();
}

// The server's VM link is named after the hostname; don't promote other links.
const vmLink = computed(() =>
  props.links.find((link) => link.title === window.__SHELLEY_INIT__?.hostname),
);


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
const currentLanguage = computed(() => languageOptions.find((o) => o.locale === locale.value)!);
const languagePickerOpen = ref(false);
const languageOptionsRef = ref<HTMLDivElement | null>(null);

async function onLanguagePicker() {
  hide();
  // The menu item disappears, so let the dialog restore focus to the menu trigger.
  triggerRef.value?.$el.focus();
  languagePickerOpen.value = true;
  await nextTick();
  languageOptionsRef.value?.querySelector<HTMLButtonElement>('[aria-pressed="true"]')?.focus();
}
function onLangChange(l: Locale) {
  setLocale(l);
  languagePickerOpen.value = false;
}
</script>
