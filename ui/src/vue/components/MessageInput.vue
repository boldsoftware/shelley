<!-- Vue port of components/MessageInput.tsx. The composer: textarea,
     send/queue split button, attach, drag/paste upload, voice (SpeechRecognition).
     PRESERVES EXACTLY the e2e contract (file-upload.spec, queue-messages.spec,
     smoke, conversation): data-testid message-input, send-button,
     send-options-button, queue-option, queued-badge, cancel-queued,
     attach-button, voice-button, message-attachments; aria-label "Message
     input", "Send message", "Send options"; classes .message-input-container,
     .message-attachment, .drag-overlay, input.message-input-hidden. The
     message-input testid/aria are also queried by utils/focusMessageInput.ts.

     Public API (consumed by ChatInterface):
       Props:
         onSend?            — not a prop; emitted as `send` (see emits)
       Emits:
         (e: "send", message: string): Promise<void> | void
         (e: "queue", message: string): Promise<void> | void
         (e: "focus"): void
         (e: "clear-injected-text"): void
         (e: "draft-change", value: string): void
         (e: "draft-send-started"): void
         (e: "draft-cleared"): void
       Because send/queue are awaited in React (onSend/onQueue return
       Promises), the parent passes async handlers via the `onSend`/`onQueue`
       *function props* below instead of pure emits — Vue emits can't be
       awaited. We therefore accept them as props (mirroring React) AND expose
       the matching emit names for non-awaiting listeners. ChatInterface should
       use the `:on-send` / `:on-queue` function props. -->
<template>
  <div
    :class="`message-input-container ${isDraggingOver ? 'drag-over' : ''} ${isShellMode ? 'shell-mode' : ''} ${showSlashMenu ? 'slash-menu-open' : ''}`"
    @dragover="handleDragOver"
    @dragenter="handleDragEnter"
    @dragleave="handleDragLeave"
    @drop="handleDrop"
  >
    <div v-if="isDraggingOver" class="drag-overlay">
      <div class="drag-overlay-content">{{ t("dropFilesHere") }}</div>
    </div>
    <form class="message-input-form" @submit="handleSubmit">
      <input
        ref="fileInputRef"
        type="file"
        class="message-input-hidden"
        multiple
        aria-hidden="true"
        @change="handleFileSelect"
      />
      <div
        v-if="attachments.length > 0"
        class="message-attachments"
        data-testid="message-attachments"
      >
        <div
          v-for="a in attachments"
          :key="a.id"
          :class="`message-attachment message-attachment-${a.status}`"
          :title="a.status === 'error' ? `${a.name}: ${a.error}` : a.name"
        >
          <img
            v-if="a.isImage && a.previewUrl"
            :src="a.previewUrl"
            :alt="a.name"
            class="message-attachment-thumb"
          />
          <div v-else class="message-attachment-file">
            <svg
              fill="none"
              stroke="currentColor"
              stroke-width="2"
              viewBox="0 0 24 24"
              width="20"
              height="20"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"
              />
              <polyline points="14 2 14 8 20 8" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
            <span class="message-attachment-name">{{ a.name }}</span>
          </div>
          <div v-if="a.status === 'uploading'" class="message-attachment-overlay">
            <div class="spinner spinner-small"></div>
          </div>
          <div v-if="a.status === 'error'" class="message-attachment-error-badge">!</div>
          <button
            type="button"
            class="message-attachment-remove"
            :aria-label="`Remove ${a.name}`"
            @click="removeAttachment(a.id)"
          >
            <svg fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24">
              <path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12" />
            </svg>
          </button>
        </div>
      </div>
      <div class="textarea-wrapper">
        <div
          v-if="showSlashMenu"
          ref="slashMenuRef"
          class="slash-command-menu"
          role="listbox"
          aria-label="Slash commands"
          data-testid="slash-command-menu"
        >
          <button
            v-for="(item, index) in slashSuggestions"
            :key="item.command"
            type="button"
            :class="`slash-command-item${index === slashMenuSelectedIndex ? ' selected' : ''}`"
            role="option"
            :aria-selected="index === slashMenuSelectedIndex"
            @mousedown.prevent
            @mouseenter="slashMenuSelectedIndex = index"
            @click="chooseSlashCommand(index)"
          >
            <span class="slash-command-name">{{ item.command }}</span>
            <span class="slash-command-description">{{ item.description }}</span>
          </button>
        </div>
        <div
          v-if="showModelArgMenu"
          ref="slashMenuRef"
          class="slash-command-menu"
          role="listbox"
          aria-label="Model options"
          data-testid="model-arg-menu"
        >
          <button
            v-for="(item, index) in modelArgSuggestions"
            :key="`${item.kind}:${item.value}`"
            type="button"
            :class="`slash-command-item${index === slashMenuSelectedIndex ? ' selected' : ''}`"
            role="option"
            :aria-selected="index === slashMenuSelectedIndex"
            @mousedown.prevent
            @mouseenter="slashMenuSelectedIndex = index"
            @click="chooseModelArg(index)"
          >
            <span class="slash-command-name">{{ item.value }}</span>
            <span class="slash-command-description">{{
              item.kind === "model" ? "model" : "reasoning level"
            }}</span>
          </button>
        </div>
        <div
          v-if="isShellMode"
          class="shell-mode-indicator"
          title="This will run as a shell command"
        >
          <svg
            width="16"
            height="16"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
          >
            <polyline points="4 17 10 11 4 5" />
            <line x1="12" y1="19" x2="20" y2="19" />
          </svg>
        </div>
        <textarea
          ref="textareaRef"
          :value="message"
          :placeholder="placeholderText"
          class="message-textarea"
          :disabled="isDisabled"
          :rows="initialRows ?? 1"
          id="shelley-message-input"
          aria-label="Message input"
          data-testid="message-input"
          @input="onTextareaInput"
          @keydown="handleKeyDown"
          @paste="handlePaste"
          @focus="onTextareaFocus"
        />
      </div>
      <div class="message-controls-row">
        <div v-if="$slots.status" class="message-controls-status-slot"><slot name="status" /></div>
        <button
          type="button"
          :disabled="isDisabled"
          class="message-attach-btn"
          :aria-label="t('attachFile')"
          data-testid="attach-button"
          @click="handleAttachClick"
        >
          <svg
            fill="none"
            stroke="currentColor"
            stroke-width="2"
            viewBox="0 0 24 24"
            width="20"
            height="20"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              d="M15.172 7l-6.586 6.586a2 2 0 102.828 2.828l6.414-6.586a4 4 0 00-5.656-5.656l-6.415 6.585a6 6 0 108.486 8.486L20.5 13"
            />
          </svg>
        </button>
        <button
          v-if="speechRecognitionAvailable"
          type="button"
          :disabled="isDisabled"
          :class="`message-voice-btn ${isListening ? 'listening' : ''}`"
          :aria-label="isListening ? t('stopVoiceInput') : t('startVoiceInput')"
          data-testid="voice-button"
          @click="toggleListening"
        >
          <svg v-if="isListening" fill="currentColor" viewBox="0 0 24 24" width="20" height="20">
            <circle cx="12" cy="12" r="6" />
          </svg>
          <svg v-else fill="currentColor" viewBox="0 0 24 24" width="20" height="20">
            <path
              d="M12 14c1.66 0 3-1.34 3-3V5c0-1.66-1.34-3-3-3S9 3.34 9 5v6c0 1.66 1.34 3 3 3zm-1-9c0-.55.45-1 1-1s1 .45 1 1v6c0 .55-.45 1-1 1s-1-.45-1-1V5zm6 6c0 2.76-2.24 5-5 5s-5-2.24-5-5H5c0 3.53 2.61 6.43 6 6.92V21h2v-3.08c3.39-.49 6-3.39 6-6.92h-2z"
            />
          </svg>
        </button>
        <div ref="queueMenuRef" class="message-send-wrapper">
          <!-- Slack-style split button: [Send | ▾] — always same width -->
          <div
            v-if="showQueueOption && hasQueueHandler"
            :class="`send-split-btn${autoQueue ? ' send-split-btn-queue' : ''}`"
          >
            <button
              type="submit"
              :disabled="!canSubmit"
              class="send-split-main"
              :aria-label="autoQueue ? 'Queue message' : t('sendMessage')"
              data-testid="send-button"
            >
              <div v-if="isDisabled || submitting" class="flex items-center justify-center">
                <div class="spinner spinner-small message-send-spinner-white"></div>
              </div>
              <svg v-else fill="currentColor" viewBox="0 0 24 24" width="18" height="18">
                <path d="M12 4l-1.41 1.41L16.17 11H4v2h12.17l-5.58 5.59L12 20l8-8z" />
              </svg>
            </button>
            <div class="send-split-divider" />
            <button
              type="button"
              :disabled="!canSubmit || (!canQueue && !autoQueue && !canCompact)"
              :class="`send-split-chevron${canQueue || autoQueue || canCompact ? '' : ' send-split-chevron-inactive'}`"
              aria-label="Send options"
              data-testid="send-options-button"
              @click="showQueueMenu = !showQueueMenu"
            >
              <svg fill="currentColor" viewBox="0 0 24 24" width="14" height="14">
                <path d="M7 10l5 5 5-5z" />
              </svg>
            </button>
            <div v-if="showQueueMenu && (canQueue || autoQueue || canCompact)" class="queue-menu">
              <button
                v-if="canQueue || autoQueue"
                type="button"
                class="queue-menu-item"
                data-testid="queue-option"
                @click="autoQueue ? handleSendNow() : handleQueueMessage()"
              >
                <!-- During distill (autoQueue=true), main button queues, dropdown offers "send now" -->
                <template v-if="autoQueue">
                  <svg fill="currentColor" viewBox="0 0 24 24" width="16" height="16">
                    <path d="M12 4l-1.41 1.41L16.17 11H4v2h12.17l-5.58 5.59L12 20l8-8z" />
                  </svg>
                  Send now
                </template>
                <!-- Clock icon — "queue for later" -->
                <template v-else>
                  <svg
                    fill="none"
                    stroke="currentColor"
                    stroke-width="2"
                    viewBox="0 0 24 24"
                    width="16"
                    height="16"
                  >
                    <circle cx="12" cy="12" r="10" />
                    <polyline points="12 6 12 12 16 14" />
                  </svg>
                  Queue after agent finishes
                </template>
              </button>
              <!-- Compact the conversation, then queue this message to run once
                   compaction finishes. -->
              <button
                v-if="canCompact"
                type="button"
                class="queue-menu-item"
                data-testid="compact-and-send-option"
                @click="handleCompactAndSend"
              >
                <svg
                  fill="none"
                  stroke="currentColor"
                  stroke-width="2"
                  viewBox="0 0 24 24"
                  width="16"
                  height="16"
                >
                  <polyline points="4 14 10 14 10 20" />
                  <polyline points="20 10 14 10 14 4" />
                  <line x1="14" y1="10" x2="21" y2="3" />
                  <line x1="3" y1="21" x2="10" y2="14" />
                </svg>
                Compact and send
              </button>
            </div>
          </div>
          <!-- Regular round send button (new conversation, no queue possible) -->
          <button
            v-else
            type="submit"
            :disabled="!canSubmit"
            class="message-send-btn"
            :aria-label="t('sendMessage')"
            data-testid="send-button"
          >
            <div v-if="isDisabled || submitting" class="flex items-center justify-center">
              <div class="spinner spinner-small message-send-spinner-white"></div>
            </div>
            <svg v-else fill="currentColor" viewBox="0 0 24 24" width="20" height="20">
              <path d="M12 4l-1.41 1.41L16.17 11H4v2h12.17l-5.58 5.59L12 20l8-8z" />
            </svg>
          </button>
        </div>
      </div>
    </form>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { useI18n } from "../composables/i18n";
import { pickPlaceholderHint } from "../../utils/placeholderHints";
import { SLASH_COMMANDS } from "../../utils/slashCommands";
import { isImeComposing } from "../../utils/imeComposing";
import { THINKING_LEVELS } from "./thinkingLevel";

// Web Speech API types
interface SpeechRecognitionEvent extends Event {
  results: SpeechRecognitionResultList;
  resultIndex: number;
}
interface SpeechRecognitionResultList {
  length: number;
  item(index: number): SpeechRecognitionResult;
  [index: number]: SpeechRecognitionResult;
}
interface SpeechRecognitionResult {
  isFinal: boolean;
  length: number;
  item(index: number): SpeechRecognitionAlternative;
  [index: number]: SpeechRecognitionAlternative;
}
interface SpeechRecognitionAlternative {
  transcript: string;
  confidence: number;
}
interface SpeechRecognition extends EventTarget {
  continuous: boolean;
  interimResults: boolean;
  lang: string;
  onresult: ((event: SpeechRecognitionEvent) => void) | null;
  onerror: ((event: Event & { error: string }) => void) | null;
  onend: (() => void) | null;
  start(): void;
  stop(): void;
  abort(): void;
}
declare global {
  interface Window {
    SpeechRecognition: new () => SpeechRecognition;
    webkitSpeechRecognition: new () => SpeechRecognition;
  }
}

interface Attachment {
  id: string;
  name: string;
  isImage: boolean;
  /** Object URL for image preview thumbnail; revoked on remove/unmount. */
  previewUrl?: string;
  status: "uploading" | "ready" | "error";
  /** Server-returned path; only present once status === "ready". */
  path?: string;
  error?: string;
}

const props = withDefaults(
  defineProps<{
    /** Async send handler (awaited). Mirrors React's onSend prop. */
    onSend: (message: string) => Promise<void> | void;
    /** Async queue handler (awaited). Mirrors React's onQueue prop. */
    onQueue?: (message: string) => Promise<void> | void;
    /** Async compaction handler (awaited). When provided, the send-options
     * menu offers "Compact and send": it compacts the conversation and then
     * queues the composed message so it runs once compaction finishes. */
    onCompact?: () => Promise<void> | void;
    /** Show the split send button with queue chevron (e.g. when in a conversation) */
    showQueueOption?: boolean;
    /** Whether queuing is available right now (agent is working) */
    canQueue?: boolean;
    /** Auto-queue instead of sending (e.g. when distilling) */
    autoQueue?: boolean;
    disabled?: boolean;
    autoFocus?: boolean;
    injectedText?: string;
    /** Programmatic composer seed. When the parent's draft reconciliation
     * decides the composer text must change (conversation switch, server echo
     * that wins), it pushes a new seed object here. Wrapped in an object so
     * re-seeding the same string still fires the watch. Deliberately NOT the
     * live keystroke value: the parent must not re-render per keystroke (that
     * made typing crawl in huge conversations); it tracks live text via the
     * draft-change emit instead. */
    draftSeed?: { value: string } | null;
    initialRows?: number;
    /** Id of the focused conversation. MessageInput is intentionally NOT keyed
     * by this in the parent (remounting would break the first-message
     * conversationId flip), so we watch it here to reset per-conversation
     * transient state — chiefly pending attachments — that React got for free
     * via its keyed remount. Without this, a file attached but not sent in one
     * conversation would be carried into (and sent to) the next. */
    conversationId?: string | null;
    /** Id of a lazily-created draft for the *current* input session. When a
     * new conversation auto-saves a draft, conversationId flips null→draftId
     * mid-typing; that is the same session, not a switch, so we must NOT clear
     * attachments. React encodes this exact carve-out in its key:
     * `(conversationId === lazyDraftId ? null : conversationId) || "new"`. */
    lazyDraftId?: string | null;
    /** Ready model ids, used to autocomplete the /model command arguments. */
    modelOptions?: string[];
  }>(),
  {
    showQueueOption: false,
    canQueue: false,
    autoQueue: false,
    disabled: false,
    autoFocus: false,
    initialRows: 1,
  },
);

const emit = defineEmits<{
  (e: "focus"): void;
  (e: "clear-injected-text"): void;
  (e: "draft-change", value: string): void;
  (e: "draft-send-started"): void;
  (e: "draft-cleared"): void;
}>();

const { t } = useI18n();

const hasQueueHandler = computed(() => props.onQueue !== undefined);
// The "Compact and send" option is available whenever a compaction handler is
// wired and we're not already mid-compaction (autoQueue signals distilling).
const canCompact = computed(() => props.onCompact !== undefined && !props.autoQueue);

const message = ref(props.draftSeed?.value ?? "");
// setMessage mirrors the React controlled-value path: surfaces every change via
// draft-change so the parent can persist it.
function setMessage(next: string | ((prev: string) => string)) {
  const prev = message.value;
  const value = typeof next === "function" ? next(prev) : next;
  if (value !== prev) emit("draft-change", value);
  message.value = value;
}

// Sync programmatic seeds (e.g. switching between draft conversations).
watch(
  () => props.draftSeed,
  (seed) => {
    if (seed != null) message.value = seed.value;
  },
);

const submitting = ref(false);
const attachments = ref<Attachment[]>([]);
const uploadsInProgress = computed(
  () => attachments.value.filter((a) => a.status === "uploading").length,
);
const readyAttachments = computed(() =>
  attachments.value.filter((a) => a.status === "ready" && a.path),
);
const dragCounter = ref(0);
const isListening = ref(false);
const isSmallScreen = ref(typeof window !== "undefined" ? window.innerWidth < 480 : false);
const showQueueMenu = ref(false);
const slashMenuSelectedIndex = ref(0);
const slashMenuDismissed = ref(false);
// Set once the user has moved the highlight in the /model argument menu (via
// Arrow keys). When they then press Enter we complete the highlighted option
// rather than sending the (possibly bare) command — mirroring a mouse click.
const modelArgMenuNavigated = ref(false);

const queueMenuRef = ref<HTMLDivElement | null>(null);
const slashMenuRef = ref<HTMLDivElement | null>(null);
const textareaRef = ref<HTMLTextAreaElement | null>(null);
const fileInputRef = ref<HTMLInputElement | null>(null);
let recognition: SpeechRecognition | null = null;
// Track the base text (before speech recognition started) and finalized speech text
let baseText = "";
let finalizedText = "";

const speechRecognitionAvailable =
  typeof window !== "undefined" && !!(window.SpeechRecognition || window.webkitSpeechRecognition);

// Pick a placeholder hint per mount; re-pick when the platform flips.
const hint = ref(pickPlaceholderHint(isSmallScreen.value));
let initialPlatform = isSmallScreen.value;
watch(isSmallScreen, (small) => {
  if (small === initialPlatform) return;
  initialPlatform = small;
  hint.value = pickPlaceholderHint(small);
});

const placeholderText = computed(() => {
  if (hint.value.id !== "default" && hint.value.text) return hint.value.text;
  return isSmallScreen.value ? t("messagePlaceholderShort") : t("messagePlaceholder");
});

function handleResize() {
  isSmallScreen.value = window.innerWidth < 480;
}

function stopListening() {
  if (recognition) {
    recognition.stop();
    recognition = null;
  }
  isListening.value = false;
}

function startListening() {
  if (!speechRecognitionAvailable) return;
  const SpeechRecognitionClass = window.SpeechRecognition || window.webkitSpeechRecognition;
  const rec = new SpeechRecognitionClass();
  rec.continuous = true;
  rec.interimResults = true;
  rec.lang = navigator.language || "en-US";

  // Capture current message as base text
  baseText = message.value;
  finalizedText = "";

  rec.onresult = (event: SpeechRecognitionEvent) => {
    let finalTranscript = "";
    let interimTranscript = "";
    for (let i = event.resultIndex; i < event.results.length; i++) {
      const transcript = event.results[i][0].transcript;
      if (event.results[i].isFinal) {
        finalTranscript += transcript;
      } else {
        interimTranscript += transcript;
      }
    }
    if (finalTranscript) finalizedText += finalTranscript;
    const base = baseText;
    const needsSpace = base.length > 0 && !/\s$/.test(base);
    const spacer = needsSpace ? " " : "";
    setMessage(base + spacer + finalizedText + interimTranscript);
  };
  rec.onerror = (event) => {
    console.error("Speech recognition error:", event.error);
    stopListening();
  };
  rec.onend = () => {
    isListening.value = false;
    recognition = null;
  };
  recognition = rec;
  rec.start();
  isListening.value = true;
}

function toggleListening() {
  if (isListening.value) stopListening();
  else startListening();
}

// Close queue menu on click outside
function onQueueMenuOutside(e: MouseEvent) {
  if (queueMenuRef.value && !queueMenuRef.value.contains(e.target as Node)) {
    showQueueMenu.value = false;
  }
}
watch(showQueueMenu, (open) => {
  if (open) document.addEventListener("mousedown", onQueueMenuOutside);
  else document.removeEventListener("mousedown", onQueueMenuOutside);
});

// Close queue menu when queueing becomes unavailable
watch(
  () => [props.canQueue, props.autoQueue] as const,
  ([cq, aq]) => {
    if (!cq && !aq) showQueueMenu.value = false;
  },
);

async function uploadFile(file: File) {
  const id = `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  const isImage = file.type.startsWith("image/");
  const previewUrl = isImage ? URL.createObjectURL(file) : undefined;
  attachments.value = [
    ...attachments.value,
    { id, name: file.name, isImage, previewUrl, status: "uploading" },
  ];

  try {
    const formData = new FormData();
    formData.append("file", file);
    const response = await fetch("/api/upload", { method: "POST", body: formData });
    if (!response.ok) {
      const errorText = await response.text();
      let msg = response.statusText;
      if (errorText.trim()) {
        try {
          const payload = JSON.parse(errorText) as { message?: unknown };
          if (typeof payload.message === "string" && payload.message.trim()) {
            msg = payload.message.trim();
          }
        } catch {
          msg = errorText.trim();
        }
      }
      throw new Error(`Upload failed: ${msg}`);
    }
    const data = await response.json();
    attachments.value = attachments.value.map((a) =>
      a.id === id ? { ...a, status: "ready", path: data.path } : a,
    );
  } catch (error) {
    console.error("Failed to upload file:", error);
    const msg = error instanceof Error ? error.message : "unknown error";
    attachments.value = attachments.value.map((a) =>
      a.id === id ? { ...a, status: "error", error: msg } : a,
    );
  }
}

function removeAttachment(id: string) {
  const found = attachments.value.find((a) => a.id === id);
  if (found?.previewUrl) URL.revokeObjectURL(found.previewUrl);
  attachments.value = attachments.value.filter((a) => a.id !== id);
}

/** Compose final message text by appending `[path]` tokens for ready attachments. */
function composeMessageWithAttachments(text: string): string {
  if (readyAttachments.value.length === 0) return text;
  const tokens = readyAttachments.value.map((a) => `[${a.path}]`).join(" ");
  const trimmed = text.trimEnd();
  return trimmed.length > 0 ? `${trimmed} ${tokens}` : tokens;
}

function clearAttachments() {
  attachments.value.forEach((a) => {
    if (a.previewUrl) URL.revokeObjectURL(a.previewUrl);
  });
  attachments.value = [];
}

// Reset pending attachments when the focused conversation changes. React gets
// this for free by keying MessageInput on conversationId (the keyed remount
// throws away component state); Vue keeps a single instance alive across
// switches, so without this an unsent attachment would leak into — and be sent
// to — the next conversation.
//
// Mirror React's key carve-out for lazy drafts: when a brand-new conversation
// auto-saves a draft, conversationId flips null→draftId mid-typing. That is the
// same input session (React keeps the key "new", so no remount), and the user
// may have already attached a file they're about to send — don't clear it.
watch(
  () => props.conversationId,
  (newId) => {
    if (newId != null && newId === props.lazyDraftId) return;
    if (attachments.value.length > 0) clearAttachments();
  },
);

function handlePaste(event: ClipboardEvent) {
  const items = event.clipboardData?.items;
  if (items) {
    for (let i = 0; i < items.length; i++) {
      const item = items[i];
      if (item.kind === "file") {
        const file = item.getAsFile();
        if (file) {
          event.preventDefault();
          void uploadFile(file);
          return;
        }
      }
    }
  }
}

function handleDragOver(event: DragEvent) {
  event.preventDefault();
  event.stopPropagation();
}
function handleDragEnter(event: DragEvent) {
  event.preventDefault();
  event.stopPropagation();
  dragCounter.value += 1;
}
function handleDragLeave(event: DragEvent) {
  event.preventDefault();
  event.stopPropagation();
  dragCounter.value -= 1;
}
async function handleDrop(event: DragEvent) {
  event.preventDefault();
  event.stopPropagation();
  dragCounter.value = 0;
  // Snapshot the file list synchronously (DataTransfer enters protected mode
  // after the first await).
  if (event.dataTransfer && event.dataTransfer.files.length > 0) {
    const files = Array.from(event.dataTransfer.files);
    for (const file of files) {
      await uploadFile(file);
    }
  }
}

function handleAttachClick() {
  fileInputRef.value?.click();
}

async function handleFileSelect(event: Event) {
  const target = event.target as HTMLInputElement;
  const files = target.files;
  if (!files || files.length === 0) return;
  for (let i = 0; i < files.length; i++) {
    await uploadFile(files[i]);
  }
  // Reset input so same file can be selected again
  target.value = "";
}

// Auto-insert injected text (diff comments) directly into the textarea.
watch(
  () => props.injectedText,
  (injected) => {
    if (injected) {
      setMessage((prev) => {
        const needsNewline = prev.length > 0 && !prev.endsWith("\n");
        return prev + (needsNewline ? "\n\n" : "") + injected;
      });
      emit("clear-injected-text");
      setTimeout(() => textareaRef.value?.focus(), 0);
    }
  },
);

const hasContent = computed(
  () => message.value.trim().length > 0 || readyAttachments.value.length > 0,
);

const isDisabled = computed(() => props.disabled);
const canSubmit = computed(
  () => hasContent.value && !isDisabled.value && !submitting.value && uploadsInProgress.value === 0,
);
const isDraggingOver = computed(() => dragCounter.value > 0);
const isShellMode = computed(() => message.value.trimStart().startsWith("!"));
const slashQuery = computed(() => {
  const match = message.value.match(/^\/[a-zA-Z0-9_-]*$/);
  return match ? match[0].slice(1).toLowerCase() : null;
});
const slashSuggestions = computed(() => {
  if (slashQuery.value === null) return [];
  return Object.values(SLASH_COMMANDS).filter((item) =>
    item.command.slice(1).startsWith(slashQuery.value!),
  );
});
const exactSlashCommand = computed(() =>
  slashSuggestions.value.some((item) => item.command.slice(1) === slashQuery.value),
);
const showSlashMenu = computed(
  () =>
    slashQuery.value !== null &&
    !slashMenuDismissed.value &&
    !exactSlashCommand.value &&
    slashSuggestions.value.length > 0 &&
    !isDisabled.value &&
    !isShellMode.value,
);

// --- /model argument autocomplete ---------------------------------------
// As soon as "/model" has been typed (with or without a trailing space), offer
// the ready model ids plus the reasoning levels as completions for the token
// currently under the cursor. This mirrors the command grammar: /model <id>
// and/or <level>, order independent.
interface ModelArgOption {
  value: string;
  kind: "model" | "level";
}
const modelArgOptions = computed<ModelArgOption[]>(() => [
  ...(props.modelOptions ?? []).map((id) => ({ value: id, kind: "model" as const })),
  ...THINKING_LEVELS.map((l) => ({ value: l.value, kind: "level" as const })),
]);
// Matches "/model" and everything after it. The trailing arguments are
// optional, so a bare "/model" (no space yet) also opens the menu — arrowing
// then Enter, or just Enter, picks the top model. Captures the argument tail.
const modelArgContext = computed(() => {
  const m = message.value.match(/^\/model(?:\s+(.*))?$/s);
  if (m === null) return null;
  const rest = m[1] ?? "";
  // The token under the cursor is the final whitespace-delimited chunk; the
  // preceding tokens are already-entered arguments we must not re-suggest.
  const priorEnd = rest.replace(/\S*$/, "");
  const partial = rest.slice(priorEnd.length);
  const prior = priorEnd.trim().split(/\s+/).filter(Boolean);
  return { partial, prior };
});
const modelArgSuggestions = computed<ModelArgOption[]>(() => {
  const ctx = modelArgContext.value;
  if (ctx === null) return [];
  // Normalize dots to dashes so "opus-4.8" and "opus-4-8" both match, mirroring
  // the server's lenient resolver.
  const norm = (s: string) => s.toLowerCase().replace(/\./g, "-");
  const q = norm(ctx.partial);
  const priorLower = new Set(ctx.prior.map((t) => t.toLowerCase()));
  const usedModel = ctx.prior.some((t) => (props.modelOptions ?? []).includes(t));
  const usedLevel = ctx.prior.some((t) => THINKING_LEVELS.some((l) => l.value === t));
  const matched = modelArgOptions.value.filter((o) => {
    if (priorLower.has(o.value.toLowerCase())) return false;
    // Only one model and one level may be chosen.
    if (o.kind === "model" && usedModel) return false;
    if (o.kind === "level" && usedLevel) return false;
    // Models match on any substring (so "opus" surfaces "claude-opus-4.8");
    // levels match on prefix (they're short and prefix is unambiguous enough).
    return o.kind === "model" ? norm(o.value).includes(q) : o.value.toLowerCase().startsWith(q);
  });
  // Rank prefix matches ahead of mid-string substring matches so the closest
  // completions come first.
  return matched.sort((a, b) => {
    const ap = norm(a.value).startsWith(q) ? 0 : 1;
    const bp = norm(b.value).startsWith(q) ? 0 : 1;
    return ap - bp;
  });
});
const showModelArgMenu = computed(
  () =>
    modelArgContext.value !== null &&
    !slashMenuDismissed.value &&
    modelArgSuggestions.value.length > 0 &&
    !isDisabled.value,
);
// Whether the token under the cursor is already a complete, valid argument
// (an exact model id or reasoning level, dot/dash- and case-insensitive). When
// it is, Enter should send the command rather than "completing" the token (which
// would only append a space and force a second Enter).
const partialIsCompleteOption = computed(() => {
  const ctx = modelArgContext.value;
  if (ctx === null || ctx.partial === "") return false;
  const norm = (s: string) => s.toLowerCase().replace(/\./g, "-");
  const p = norm(ctx.partial);
  return modelArgOptions.value.some((o) => norm(o.value) === p);
});
function chooseModelArg(index: number) {
  const opt = modelArgSuggestions.value[index];
  const ctx = modelArgContext.value;
  if (!opt || ctx === null) return;
  // Replace the partial token under the cursor with the full option + a space,
  // so the next argument can be typed/autocompleted immediately. When the
  // message has no partial yet (e.g. bare "/model" with no trailing space),
  // ensure a separating space so we don't produce "/modelclaude-...".
  const withoutPartial =
    ctx.partial === "" ? message.value : message.value.slice(0, -ctx.partial.length);
  const sep = /\s$/.test(withoutPartial) ? "" : " ";
  setMessage(`${withoutPartial}${sep}${opt.value} `);
  requestAnimationFrame(() => textareaRef.value?.focus());
}

watch(modelArgContext, () => {
  slashMenuSelectedIndex.value = 0;
  modelArgMenuNavigated.value = false;
});

watch(slashQuery, () => {
  slashMenuSelectedIndex.value = 0;
});

watch(message, (value) => {
  if (value.length === 0) slashMenuDismissed.value = false;
  if (value === SLASH_COMMANDS.SHELL.command) {
    setMessage("!");
    requestAnimationFrame(() => textareaRef.value?.focus());
  }
});

async function chooseSlashCommand(index: number) {
  const item = slashSuggestions.value[index];
  if (!item) return;
  if (!item.takesArgs) {
    setMessage("");
    slashMenuDismissed.value = true;
    emit("draft-send-started");
    try {
      await props.onSend(item.command);
      emit("draft-cleared");
    } catch {
      setMessage(item.command);
    }
    return;
  }
  if (item.command === SLASH_COMMANDS.SHELL.command) {
    setMessage("!");
    requestAnimationFrame(() => textareaRef.value?.focus());
    return;
  }
  setMessage(`${item.command} `);
  requestAnimationFrame(() => textareaRef.value?.focus());
}

function onSlashMenuOutside(e: MouseEvent) {
  if (slashMenuRef.value?.contains(e.target as Node)) return;
  slashMenuDismissed.value = true;
}

watch([showSlashMenu, showModelArgMenu], ([a, b]) => {
  if (a || b) document.addEventListener("mousedown", onSlashMenuOutside);
  else document.removeEventListener("mousedown", onSlashMenuOutside);
});

async function handleSubmit(e: Event) {
  e.preventDefault();
  if (hasContent.value && !props.disabled && !submitting.value && uploadsInProgress.value === 0) {
    if (isListening.value) stopListening();

    // Auto-queue when distilling or when explicitly requested
    if (props.autoQueue && props.onQueue) {
      const messageToQueue = composeMessageWithAttachments(message.value).trim();
      setMessage("");
      clearAttachments();
      emit("draft-cleared");
      try {
        await props.onQueue(messageToQueue);
      } catch {
        setMessage(messageToQueue);
      }
      return;
    }

    const messageToSend = composeMessageWithAttachments(message.value);
    // Pause autosave before awaiting onSend so a trailing PUT can't race the
    // chat POST. Don't clear the draft yet — if send fails the textarea stays.
    emit("draft-send-started");
    submitting.value = true;
    try {
      await props.onSend(messageToSend);
      setMessage("");
      clearAttachments();
      emit("draft-cleared");
    } catch {
      // Keep the message on error so user can retry.
    } finally {
      submitting.value = false;
    }
  }
}

async function handleQueueMessage() {
  if (hasContent.value && props.onQueue) {
    if (isListening.value) stopListening();
    const messageToQueue = composeMessageWithAttachments(message.value).trim();
    setMessage("");
    clearAttachments();
    emit("draft-cleared");
    showQueueMenu.value = false;
    try {
      await props.onQueue(messageToQueue);
    } catch {
      setMessage(messageToQueue);
    }
  }
}

/** Compact the conversation, then queue the composed message so it runs once
 * compaction completes. Kicks off compaction and queues in one gesture. */
async function handleCompactAndSend() {
  if (hasContent.value && props.onCompact && props.onQueue) {
    if (isListening.value) stopListening();
    const messageToQueue = composeMessageWithAttachments(message.value).trim();
    setMessage("");
    clearAttachments();
    emit("draft-cleared");
    showQueueMenu.value = false;
    try {
      // Start compaction first so the conversation enters the distilling
      // state, then queue — enqueued messages drain after distillation ends.
      await props.onCompact();
      await props.onQueue(messageToQueue);
    } catch {
      setMessage(messageToQueue);
    }
  }
}

/** Send now (bypass auto-queue) — used from the dropdown during distill mode */
async function handleSendNow() {
  if (hasContent.value && !props.disabled && !submitting.value && uploadsInProgress.value === 0) {
    if (isListening.value) stopListening();
    const messageToSend = composeMessageWithAttachments(message.value).trim();
    setMessage("");
    clearAttachments();
    emit("draft-cleared");
    showQueueMenu.value = false;
    submitting.value = true;
    try {
      await props.onSend(messageToSend);
    } catch {
      setMessage(messageToSend);
    } finally {
      submitting.value = false;
    }
  }
}

function onTextareaInput(e: Event) {
  setMessage((e.target as HTMLTextAreaElement).value);
}

function onTextareaFocus() {
  // Scroll to bottom after keyboard animation settles
  requestAnimationFrame(() => requestAnimationFrame(() => emit("focus")));
}

function handleKeyDown(e: KeyboardEvent) {
  // Don't submit while IME is composing.
  if (isImeComposing(e)) return;
  if (showSlashMenu.value) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      slashMenuSelectedIndex.value =
        (slashMenuSelectedIndex.value + 1) % slashSuggestions.value.length;
      return;
    }
    if (e.key === "ArrowUp") {
      e.preventDefault();
      slashMenuSelectedIndex.value =
        (slashMenuSelectedIndex.value - 1 + slashSuggestions.value.length) %
        slashSuggestions.value.length;
      return;
    }
    if (e.key === "Enter" || e.key === "Tab") {
      e.preventDefault();
      void chooseSlashCommand(slashMenuSelectedIndex.value);
      return;
    }
  }
  if (showModelArgMenu.value) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      modelArgMenuNavigated.value = true;
      slashMenuSelectedIndex.value =
        (slashMenuSelectedIndex.value + 1) % modelArgSuggestions.value.length;
      return;
    }
    if (e.key === "ArrowUp") {
      e.preventDefault();
      modelArgMenuNavigated.value = true;
      slashMenuSelectedIndex.value =
        (slashMenuSelectedIndex.value - 1 + modelArgSuggestions.value.length) %
        modelArgSuggestions.value.length;
      return;
    }
    // Tab always completes the highlighted option. Enter completes the
    // highlighted option when:
    //   - the user moved the highlight with the arrow keys (Enter == click), or
    //   - the token under the cursor is a genuine partial (not yet a full
    //     option), so users can chain arguments, or
    //   - nothing has been chosen yet (bare "/model" with no argument): Enter
    //     picks the top suggestion instead of sending a useless bare command.
    // Once an argument is present and the token is complete (e.g. "/model low"
    // or "/model <id> "), Enter falls through and sends the command.
    if (e.key === "Tab") {
      e.preventDefault();
      chooseModelArg(slashMenuSelectedIndex.value);
      return;
    }
    const ctx = modelArgContext.value;
    const nothingChosenYet = ctx !== null && ctx.partial === "" && ctx.prior.length === 0;
    if (
      e.key === "Enter" &&
      (modelArgMenuNavigated.value ||
        nothingChosenYet ||
        (ctx?.partial !== "" && !partialIsCompleteOption.value))
    ) {
      e.preventDefault();
      chooseModelArg(slashMenuSelectedIndex.value);
      return;
    }
  }
  // Escape blurs the textarea so follow-up shortcuts work.
  if (e.key === "Escape") {
    textareaRef.value?.blur();
    return;
  }
  if (e.key === "Enter" && !e.shiftKey) {
    // On mobile, let Enter create newlines since there's a send button.
    const isMobile = "ontouchstart" in window;
    if (isMobile && !(e.ctrlKey || e.metaKey)) return;
    e.preventDefault();
    void handleSubmit(e);
  }
}

// Re-focus textarea after submission completes and it's re-enabled.
watch(submitting, (now) => {
  const isMobile = "ontouchstart" in window;
  if (!now && !isMobile && document.activeElement === document.body) {
    textareaRef.value?.focus();
  }
});

// autoFocus — re-attempt focus when the textarea becomes enabled.
watch(
  () => [props.autoFocus, props.disabled] as const,
  ([af, dis]) => {
    if (af && !dis && textareaRef.value) {
      setTimeout(() => textareaRef.value?.focus(), 0);
    }
  },
  { immediate: true },
);

// Handle virtual keyboard appearance on mobile via visualViewport.
function handleViewportResize() {
  if (document.activeElement === textareaRef.value) {
    requestAnimationFrame(() => {
      textareaRef.value?.scrollIntoView({ behavior: "smooth", block: "center" });
    });
  }
}

onMounted(() => {
  window.addEventListener("resize", handleResize);
  if (typeof window !== "undefined" && window.visualViewport) {
    window.visualViewport.addEventListener("resize", handleViewportResize);
  }
});

onUnmounted(() => {
  window.removeEventListener("resize", handleResize);
  if (typeof window !== "undefined" && window.visualViewport) {
    window.visualViewport.removeEventListener("resize", handleViewportResize);
  }
  document.removeEventListener("mousedown", onQueueMenuOutside);
  document.removeEventListener("mousedown", onSlashMenuOutside);
  if (recognition) recognition.abort();
  attachments.value.forEach((a) => {
    if (a.previewUrl) URL.revokeObjectURL(a.previewUrl);
  });
});
</script>
