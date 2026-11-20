<!-- An MCP Apps view (services/mcpAppHost.ts) in a sandboxed iframe. The
     iframe sits in a <dialog> that shows inline, closed; fullscreen opens it
     modally, in the top layer, so the iframe never moves and never reloads.
     What the view asks to do as the user waits for the user's OK in a bar
     below it, out of the view's reach. The page gets a channel to the host
     ahead of its own scripts (channelScript). -->
<template>
  <dialog
    ref="dialogRef"
    class="mcp-app-frame"
    :class="{ 'mcp-app-frame--borderless': prefersBorder === false }"
    data-testid="mcp-app-frame"
    :data-display-mode="displayMode"
    @close="onClosed"
  >
    <div v-if="displayMode === 'fullscreen'" class="mcp-app-frame-bar">
      <span class="mcp-app-frame-title">{{ title }}</span>
      <button
        type="button"
        class="mcp-app-frame-close"
        :aria-label="t('closeAction')"
        :title="t('closeAction')"
        @click="setDisplayMode('inline')"
      >
        ✕
      </button>
    </div>
    <iframe
      ref="iframeRef"
      :srcdoc="srcdoc"
      :sandbox="sandbox"
      :allow="allow"
      :title="title"
      class="mcp-app-frame-iframe"
      :style="displayMode === 'inline' ? { height: `${height}px` } : undefined"
      @load="onLoad"
    />
    <div
      v-if="consent"
      class="mcp-app-frame-consent"
      role="alertdialog"
      :aria-label="consentPrompt"
      data-testid="mcp-app-consent"
    >
      <div class="mcp-app-frame-consent-prompt">{{ consentPrompt }}</div>
      <pre class="mcp-app-frame-consent-detail">{{ consent.detail }}</pre>
      <div class="mcp-app-frame-consent-actions">
        <button
          type="button"
          class="btn-secondary btn-sm"
          :disabled="!consentReady"
          @click="answer(false)"
        >
          {{ t("cancel") }}
        </button>
        <button
          type="button"
          class="btn-primary btn-sm"
          :disabled="!consentReady"
          @click="answer(true)"
        >
          {{ consentAction }}
        </button>
      </div>
    </div>
  </dialog>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  AppHost,
  channelScript,
  channelToken,
  prependToPage,
  type AppHostOptions,
  type Consent,
  type DisplayMode,
  type HostContext,
  styleVariables,
} from "../../services/mcpAppHost";
import { useDarkMode } from "../composables/darkMode";
import { useI18n } from "../composables/i18n";

const props = defineProps<{
  html: string;
  title: string;
  sandbox: string;
  allow?: string;
  prefersBorder?: boolean;
  options: Omit<AppHostOptions, "context" | "onSize" | "onDisplayMode" | "confirm">;
}>();
/** load gives a way to send the view messages outside the protocol. */
const emit = defineEmits<{ load: [send: (message: unknown) => void] }>();

const { t } = useI18n();
const dialogRef = ref<HTMLDialogElement | null>(null);
const iframeRef = ref<HTMLIFrameElement | null>(null);
const displayMode = ref<DisplayMode>("inline");
const height = ref(300);
const isDark = useDarkMode();
let host: AppHost | null = null;
let loaded = false;
let lastDimensions = "";
const token = ref(channelToken());
const srcdoc = computed(() => prependToPage(props.html, channelScript(token.value)));

// One request at a time: others are refused while it waits, and the view
// going declines it. The buttons wait a moment, and the view can't resize
// itself meanwhile, so a view can't put them under a click meant for it.
const consent = ref<(Consent & { resolve(ok: boolean): void }) | null>(null);
const consentReady = ref(false);
let consentTimer: ReturnType<typeof setTimeout> | undefined;
let deferredHeight: number | undefined;
const CONSENT_DELAY_MS = 500;

const PROMPTS = { send: "viewAsksToSend", open: "viewAsksToOpen", save: "viewAsksToSave" } as const;
const ACTIONS = { send: "viewSend", open: "viewOpen", save: "save" } as const;
const consentPrompt = computed(() =>
  consent.value ? t(PROMPTS[consent.value.kind]).replace("{source}", props.options.source) : "",
);
const consentAction = computed(() => (consent.value ? t(ACTIONS[consent.value.kind]) : ""));

function confirm(request: Consent): Promise<boolean> {
  if (consent.value) return Promise.reject(new Error("Another request is waiting for the user."));
  return new Promise((resolve) => {
    consent.value = { ...request, resolve };
    consentReady.value = false;
    consentTimer = setTimeout(() => (consentReady.value = true), CONSENT_DELAY_MS);
  });
}

function answer(ok: boolean) {
  const pending = consent.value;
  if (!pending) return;
  consent.value = null;
  clearTimeout(consentTimer);
  if (deferredHeight !== undefined) height.value = deferredHeight;
  deferredHeight = undefined;
  pending.resolve(ok);
}

const MIN_HEIGHT = 60;
const maxHeight = () => Math.round(Math.max(400, window.innerHeight * 0.8));

function context(): HostContext {
  const inline = displayMode.value === "inline";
  const width = dialogRef.value?.clientWidth ?? 0;
  return {
    theme: isDark.value ? "dark" : "light",
    displayMode: displayMode.value,
    containerDimensions: inline
      ? { width, maxHeight: maxHeight() }
      : { width, height: iframeRef.value?.clientHeight ?? window.innerHeight },
  };
}

function connect() {
  answer(false);
  host?.close();
  host = null;
  loaded = false;
  if (!iframeRef.value) return;
  lastDimensions = "";
  host = new AppHost(iframeRef.value, token.value, {
    ...props.options,
    context,
    onSize({ height: h }) {
      if (typeof h !== "number" || h <= 0) return;
      const clamped = Math.min(Math.max(Math.ceil(h), MIN_HEIGHT), maxHeight());
      if (consent.value) deferredHeight = clamped;
      else height.value = clamped;
    },
    onDisplayMode(mode) {
      setDisplayMode(mode);
      return displayMode.value;
    },
    confirm,
  });
}

// The view's page loaded, or the view went to another page, which gets no
// bridge: it isn't the server's view, and its CSP is gone.
function onLoad() {
  if (loaded) {
    answer(false);
    host?.close();
    host = null;
    return;
  }
  loaded = true;
  emit("load", (message) => host?.send(message));
}

function setDisplayMode(mode: DisplayMode) {
  const dialog = dialogRef.value;
  if (!dialog || mode === displayMode.value) return;
  if (mode === "fullscreen") dialog.showModal();
  else dialog.close();
  displayMode.value = mode;
  void nextTick(() => host?.changed({ displayMode: mode, ...dimensions() }));
}

// Escape, or the close button: back inline.
function onClosed() {
  if (displayMode.value !== "fullscreen") return;
  displayMode.value = "inline";
  void nextTick(() => host?.changed({ displayMode: "inline", ...dimensions() }));
}

function dimensions() {
  const { containerDimensions } = context();
  lastDimensions = JSON.stringify(containerDimensions);
  return { containerDimensions };
}

function receive(event: MessageEvent) {
  host?.receive(event);
}

const resizeObserver = new ResizeObserver(() => {
  const { containerDimensions } = context();
  if (JSON.stringify(containerDimensions) !== lastDimensions) host?.changed(dimensions());
});

onMounted(() => {
  window.addEventListener("message", receive);
  connect();
  if (dialogRef.value) resizeObserver.observe(dialogRef.value);
});

onBeforeUnmount(() => {
  answer(false);
  host?.teardown();
  host?.close();
  window.removeEventListener("message", receive);
  resizeObserver.disconnect();
  if (dialogRef.value?.open) dialogRef.value.close();
});

// A new page is a new view, with a new channel.
watch(
  () => props.html,
  () => {
    token.value = channelToken();
    connect();
  },
);

watch(isDark, () => {
  const { theme } = context();
  host?.changed({ theme, styles: { variables: styleVariables() } });
});
</script>

<style scoped>
/* Inline, the dialog stays closed but shows, in flow. */
.mcp-app-frame {
  display: flex;
  flex-direction: column;
  position: static;
  width: 100%;
  max-width: none;
  margin: 0;
  padding: 0;
  border: 1px solid var(--border);
  border-radius: 0.5rem;
  overflow: hidden;
  background: transparent;
  color: inherit;
}

.mcp-app-frame--borderless {
  border: none;
  border-radius: 0;
}

.mcp-app-frame:modal {
  position: fixed;
  inset: 0;
  width: 100%;
  height: 100%;
  max-height: none;
  border: none;
  border-radius: 0;
  background: var(--bg-base);
}

.mcp-app-frame-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.5rem;
  padding: 0.375rem 0.5rem 0.375rem 0.75rem;
  border-bottom: 1px solid var(--border);
  font-size: var(--font-size-13);
  color: var(--text-secondary);
}

.mcp-app-frame-title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mcp-app-frame-close {
  padding: 0.25rem 0.5rem;
  border: none;
  border-radius: 0.25rem;
  background: none;
  color: inherit;
  cursor: pointer;
}

.mcp-app-frame-close:hover {
  background: var(--bg-tertiary);
}

.mcp-app-frame-iframe {
  display: block;
  width: 100%;
  border: none;
  background: transparent;
}

.mcp-app-frame:modal .mcp-app-frame-iframe {
  flex: 1;
  min-height: 0;
}

/* Fullscreen, a long request takes the view's space and scrolls, its
   buttons after all of it. */
.mcp-app-frame:modal .mcp-app-frame-consent {
  flex: 0 1 auto;
  min-height: 0;
  overflow-y: auto;
}

.mcp-app-frame-consent {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  padding: 0.625rem 0.75rem;
  border-top: 1px solid var(--border);
  background: var(--bg-secondary);
  font-size: var(--font-size-13);
}

.mcp-app-frame-consent-prompt {
  color: var(--text-secondary);
}

/* All of it: what's sent is what's shown. */
.mcp-app-frame-consent-detail {
  margin: 0;
  color: var(--text-primary);
  font-family: var(--font-mono);
  font-size: var(--font-size-12);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.mcp-app-frame-consent-actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.5rem;
}
</style>
