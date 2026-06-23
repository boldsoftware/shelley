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
    :class="`message-input-container ${isDraggingOver ? 'drag-over' : ''} ${isShellMode ? 'shell-mode' : ''}`"
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
              :disabled="!canSubmit || (!canQueue && !autoQueue)"
              :class="`send-split-chevron${canQueue || autoQueue ? '' : ' send-split-chevron-inactive'}`"
              aria-label="Send options"
              data-testid="send-options-button"
              @click="showQueueMenu = !showQueueMenu"
            >
              <svg fill="currentColor" viewBox="0 0 24 24" width="14" height="14">
                <path d="M7 10l5 5 5-5z" />
              </svg>
            </button>
            <div v-if="showQueueMenu && (canQueue || autoQueue)" class="queue-menu">
              <button
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
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import { useI18n } from "../composables/i18n";
import { pickPlaceholderHint } from "../../utils/placeholderHints";

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
    /** Show the split send button with queue chevron (e.g. when in a conversation) */
    showQueueOption?: boolean;
    /** Whether queuing is available right now (agent is working) */
    canQueue?: boolean;
    /** Auto-queue instead of sending (e.g. when distilling) */
    autoQueue?: boolean;
    disabled?: boolean;
    autoFocus?: boolean;
    injectedText?: string;
    /** Controlled draft text. When provided, MessageInput surfaces every
     * keystroke via the draft-change emit so the parent can persist it. */
    draftValue?: string;
    initialRows?: number;
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

const message = ref(props.draftValue ?? "");
// setMessage mirrors the React controlled-value path: surfaces every change via
// draft-change so the parent can persist it.
function setMessage(next: string | ((prev: string) => string)) {
  const prev = message.value;
  const value = typeof next === "function" ? next(prev) : next;
  if (value !== prev) emit("draft-change", value);
  message.value = value;
}

// Sync external draft updates (e.g. switching between draft conversations).
watch(
  () => props.draftValue,
  (dv) => {
    if (dv !== undefined) message.value = dv;
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

const queueMenuRef = ref<HTMLDivElement | null>(null);
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
  if (e.isComposing) return;
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

function adjustTextareaHeight() {
  const ta = textareaRef.value;
  if (ta) {
    ta.style.height = "auto";
    const scrollHeight = ta.scrollHeight;
    const maxHeight = 200;
    ta.style.height = `${Math.min(scrollHeight, maxHeight)}px`;
  }
}

watch(message, () => {
  void nextTick(adjustTextareaHeight);
});

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
  void nextTick(adjustTextareaHeight);
});

onUnmounted(() => {
  window.removeEventListener("resize", handleResize);
  if (typeof window !== "undefined" && window.visualViewport) {
    window.visualViewport.removeEventListener("resize", handleViewportResize);
  }
  document.removeEventListener("mousedown", onQueueMenuOutside);
  if (recognition) recognition.abort();
  attachments.value.forEach((a) => {
    if (a.previewUrl) URL.revokeObjectURL(a.previewUrl);
  });
});
</script>
