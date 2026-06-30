<!-- Vue port of components/Message.tsx — the central message renderer and tool
     dispatcher. Preserves every class/testid/data-tool-name/aria/text the e2e
     suite relies on: .message-agent/.message-user/.message-tool/.message-error,
     [data-testid=message], [data-testid=message-content], role article/alert,
     data-message-id, data-commentable, the action bar, usage/info modals, the
     distillation box + editor, queued badge, and the renderContent dispatch
     (delegated to MessageContentBlock). Sub-components: GitInfoMessage,
     WarningMessage, DistillStatusMessage, ErrorRetryButton. -->
<template>
  <!-- Distillation status: compact agent-side indicator. -->
  <DistillStatusMessage v-if="isDistill" :message="message" />

  <!-- system: render nothing. -->
  <template v-else-if="message.type === 'system'" />

  <!-- warning -->
  <WarningMessage v-else-if="message.type === 'warning'" :message="message" />

  <!-- gitinfo -->
  <GitInfoMessage
    v-else-if="message.type === 'gitinfo'"
    :message="message"
    :on-open-diff-viewer="onOpenDiffViewer"
  />

  <!-- error message -->
  <template v-else-if="isError">
    <div
      ref="messageRef"
      :class="`${messageClasses} msg-container-relative`"
      data-testid="message"
      role="alert"
      aria-label="Error message"
      @click="handleMessageClick"
      @mouseenter="isHovered = true"
      @mouseleave="isHovered = false"
    >
      <MessageActionBar
        v-if="actionBarVisible && (hasCopyAction || hasUsageAction || hasForkAction)"
        :on-copy="hasCopyAction ? handleCopy : undefined"
        :on-show-usage="hasUsageAction ? handleShowUsage : undefined"
        :on-fork="hasForkAction ? handleFork : undefined"
      />
      <div class="message-content" data-testid="message-content">
        <div class="whitespace-pre-wrap break-words">{{ errorText }}</div>
        <ErrorRetryButton
          v-if="errorRetryable && isLastMessage"
          :conversation-id="message.conversation_id"
        />
      </div>
    </div>
    <UsageDetailModal
      v-if="showUsageModal && usage"
      :usage="usage"
      :duration-ms="durationMs"
      @close="showUsageModal = false"
    />
    <MessageInfoModal v-if="showInfoModal" :message="message" @close="showInfoModal = false" />
  </template>

  <!-- display_data driven (compact, tool-specific) -->
  <template v-else-if="displayData && displayData.length > 0">
    <div
      ref="messageRef"
      :class="`${messageClasses} msg-container-relative`"
      data-testid="message"
      role="article"
      @click="handleMessageClick"
      @mouseenter="isHovered = true"
      @mouseleave="isHovered = false"
    >
      <MessageActionBar
        v-if="actionBarVisible && (hasCopyAction || hasUsageAction || hasForkAction)"
        :on-copy="hasCopyAction ? handleCopy : undefined"
        :on-show-usage="hasUsageAction ? handleShowUsage : undefined"
        :on-fork="hasForkAction ? handleFork : undefined"
      />
      <div class="message-content" data-testid="message-content">
        <div v-for="(td, index) in displayData" :key="index">
          <MessageDisplayData
            :tool-display="td"
            :tool-name="td.tool_name"
            :on-comment-text-change="onCommentTextChange"
          />
        </div>
      </div>
    </div>
    <UsageDetailModal
      v-if="showUsageModal && usage"
      :usage="usage"
      :duration-ms="durationMs"
      @close="showUsageModal = false"
    />
    <MessageInfoModal v-if="showInfoModal" :message="message" @close="showInfoModal = false" />
  </template>

  <!-- no meaningful content -> render nothing -->
  <template v-else-if="!hasRenderableContent" />

  <!-- main content path -->
  <template v-else>
    <div
      ref="messageRef"
      :class="`${messageClasses} msg-container-relative`"
      data-testid="message"
      :data-message-id="message.message_id"
      :data-commentable="isCommentable ? 'true' : undefined"
      role="article"
      @click="handleMessageClick"
      @mouseenter="isHovered = true"
      @mouseleave="isHovered = false"
    >
      <MessageActionBar
        v-if="actionBarVisible && (hasCopyAction || hasUsageAction || hasForkAction)"
        :on-copy="hasCopyAction ? handleCopy : undefined"
        :on-show-usage="hasUsageAction ? handleShowUsage : undefined"
        :on-fork="hasForkAction ? handleFork : undefined"
      />
      <div class="message-content" data-testid="message-content">
        <!-- Distillation box takes precedence over content blocks. -->
        <div
          v-if="isDistilledUser"
          class="distillation-file-box"
          data-testid="distillation-file-box"
        >
          <div class="distillation-file-box-header">
            <div class="distillation-file-box-title">
              {{ distillationEditable ? "Editable distillation" : "Compacted summary" }}
            </div>
            <button
              v-if="distillationEditable"
              type="button"
              class="distillation-edit-button"
              title="Edit distillation in modal"
              @click="openDistillationEditor"
            >
              Edit
            </button>
          </div>
          <div v-if="distillationEditable" class="distillation-file-box-meta">
            Shown from editable file <code>{{ distillationFile }}</code
            >.
          </div>
          <div class="distillation-file-box-content">
            <MarkdownContent
              v-if="displayedDistillationContent"
              :text="displayedDistillationContent"
            />
            <span v-else class="distillation-empty">Empty distillation</span>
          </div>
        </div>

        <template v-else>
          <div v-for="(item, index) in coalescedContent" :key="index">
            <CitedText
              v-if="item.kind === 'text'"
              :text="item.text"
              :markdown-text="item.markdownText"
              :citations="item.citations"
              :render-markdown="shouldRenderMarkdown(markdownMode, isUser, isDistilledUser)"
              :message-id="message.message_id"
            />
            <MessageContentBlock
              v-else
              :content="item.content!"
              :render-md="shouldRenderMarkdown(markdownMode, isUser, isDistilledUser)"
              :message-id="message.message_id"
              :tool-progress="toolProgress"
              :tool-use-map="toolUseMap"
              :server-tool-result-map="serverToolResultMap"
              :on-comment-text-change="onCommentTextChange"
            />
          </div>
        </template>

        <div v-if="isQueued" class="queued-message-badge" data-testid="queued-badge">
          <span class="queued-message-badge-label">
            <svg
              fill="currentColor"
              viewBox="0 0 24 24"
              width="14"
              height="14"
              style="margin-right: 4px; vertical-align: middle"
            >
              <path
                d="M3 13h2v-2H3v2zm0 4h2v-2H3v2zm0-8h2V7H3v2zm4 4h14v-2H7v2zm0 4h14v-2H7v2zM7 7v2h14V7H7z"
              />
            </svg>
            Queued
          </span>
          <button
            v-if="onCancelQueued"
            class="queued-message-badge-cancel"
            data-testid="cancel-queued"
            title="Cancel queued message"
            @click="onQueuedCancel"
          >
            Cancel
          </button>
        </div>
      </div>
    </div>
    <UsageDetailModal
      v-if="showUsageModal && usage"
      :usage="usage"
      :duration-ms="durationMs"
      @close="showUsageModal = false"
    />
    <MessageInfoModal v-if="showInfoModal" :message="message" @close="showInfoModal = false" />
    <EditableFileModal
      v-if="distillationFile"
      :is-open="showDistillationEditor"
      :path="distillationFile"
      title="Edit distillation"
      @close="showDistillationEditor = false"
      @saved="(c: string) => (distillationContentOverride = c)"
    />
  </template>
</template>

<script setup lang="ts">
import { computed, inject, onUnmounted, ref, watch, type ComputedRef } from "vue";
import {
  type Message as MessageType,
  type LLMMessage,
  type LLMContent,
  type Usage,
  type ToolProgress,
  isDistillStatusMessage,
  isQueuedMessage,
} from "../../types";
import { type MarkdownMode } from "../../services/settings";
import { useMarkdownMode } from "../composables/markdownMode";
import { getContentType } from "../utils/messageContent";
import MarkdownContent from "./MarkdownContent.vue";
import MessageActionBar from "./MessageActionBar.vue";
import UsageDetailModal from "./UsageDetailModal.vue";
import MessageInfoModal from "./MessageInfoModal.vue";
import EditableFileModal from "./EditableFileModal.vue";
import GitInfoMessage from "./GitInfoMessage.vue";
import WarningMessage from "./WarningMessage.vue";
import DistillStatusMessage from "./DistillStatusMessage.vue";
import ErrorRetryButton from "./ErrorRetryButton.vue";
import MessageContentBlock from "./MessageContentBlock.vue";
import CitedText from "./CitedText.vue";
import { coalesceContent } from "../../utils/coalesceContent";
import MessageDisplayData from "./MessageDisplayData.vue";

interface ToolDisplay {
  tool_use_id: string;
  tool_name?: string;
  display: unknown;
}

const props = defineProps<{
  message: MessageType;
  onOpenDiffViewer?: (commit: string, cwd?: string) => void;
  onCommentTextChange?: (text: string) => void;
  onCancelQueued?: () => void;
  toolProgress?: Record<string, ToolProgress>;
  // onFork forks the conversation, copying messages up to and including this
  // one into a new conversation and navigating to it.
  onFork?: (messageId: string) => void;
}>();

const { markdownMode } = useMarkdownMode();

/** Should we render markdown for this content block? */
function shouldRenderMarkdown(
  mode: MarkdownMode,
  isUserMsg: boolean,
  isDistilledUserMsg: boolean,
): boolean {
  if (mode === "off") return false;
  // Agent messages (and distilled user messages) render in "agent" and "all".
  if (!isUserMsg || isDistilledUserMsg) return true;
  // Regular user messages only in "all" mode.
  return mode === "all";
}

const isDistill = computed(() => isDistillStatusMessage(props.message));

// ---- Action bar state (show on hover or tap) ----
const showActionBar = ref(false);
const isHovered = ref(false);
const showUsageModal = ref(false);
const showInfoModal = ref(false);
const messageRef = ref<HTMLDivElement | null>(null);

const actionBarVisible = computed(() => showActionBar.value || isHovered.value);

// ---- Parsed message payloads ----
function safeParse<T>(value: unknown, label: string): T | null {
  if (!value) return null;
  try {
    return typeof value === "string" ? (JSON.parse(value) as T) : (value as T);
  } catch (err) {
    console.error(`Failed to parse ${label}:`, err);
    return null;
  }
}

const llmMessage = computed<LLMMessage | null>(() =>
  safeParse<LLMMessage>(props.message.llm_data, "LLM data"),
);

const usage = computed<Usage | null>(() => {
  if (props.message.type === "agent" && props.message.usage_data) {
    return safeParse<Usage>(props.message.usage_data, "usage data");
  }
  return null;
});

const durationMs = computed<number | null>(() => {
  const u = usage.value;
  if (u?.start_time && u?.end_time) {
    return new Date(u.end_time).getTime() - new Date(u.start_time).getTime();
  }
  return null;
});

const displayData = computed<ToolDisplay[] | null>(() =>
  safeParse<ToolDisplay[]>(props.message.display_data, "display data"),
);

// ---- Classification ----
function hasToolResult(m: LLMMessage | null): boolean {
  if (!m) return false;
  return m.Content?.some((c) => c.Type === 6) ?? false; // 6 = tool_result
}
function hasToolContent(m: LLMMessage | null): boolean {
  if (!m) return false;
  return m.Content?.some((c) => c.Type === 5 || c.Type === 6) ?? false; // 5/6
}

const isUser = computed(() => props.message.type === "user" && !hasToolResult(llmMessage.value));
const isTool = computed(() => props.message.type === "tool" || hasToolContent(llmMessage.value));
const isError = computed(() => props.message.type === "error");

// ---- Distillation ----
const distillation = computed(() => {
  let distillationFile = "";
  let distillationContent = "";
  let distillationEditable = false;
  let isDistilledUser = false;
  if (isUser.value && props.message.user_data) {
    try {
      const ud =
        typeof props.message.user_data === "string"
          ? JSON.parse(props.message.user_data)
          : props.message.user_data;
      if (ud?.distilled === "true") {
        distillationFile = ud.distillation_file || "";
        distillationContent = ud.distillation_content || "";
        // "compact" summaries are generated checkpoints paired with a verbatim
        // recent tail; they are not editable. Only the default distillation
        // (which writes an editable temp file) is.
        distillationEditable = ud.distillation_editable === "true" && !!distillationFile;
        isDistilledUser = true;
      }
    } catch {
      // ignore
    }
  }
  return { distillationFile, distillationContent, distillationEditable, isDistilledUser };
});

const isDistilledUser = computed(() => distillation.value.isDistilledUser);
const distillationFile = computed(() => distillation.value.distillationFile);
const distillationEditable = computed(() => distillation.value.distillationEditable);

const showDistillationEditor = ref(false);
const distillationContentOverride = ref<string | null>(null);
const displayedDistillationContent = computed(
  () => distillationContentOverride.value ?? distillation.value.distillationContent,
);

const isQueued = computed(() => isUser.value && isQueuedMessage(props.message));

// ---- Text extraction for copy ----
function getMessageText(): string {
  const m = llmMessage.value;
  if (!m?.Content) return "";
  const textParts: string[] = [];
  m.Content.forEach((content) => {
    const contentType = getContentType(content.Type);
    if (contentType === "text" && content.Text) {
      textParts.push(content.Text);
    } else if (contentType === "thinking") {
      const thinkingText = content.Thinking || content.Text;
      if (thinkingText) textParts.push(`[Thinking]\n${thinkingText}`);
    } else if (contentType === "tool_result" && content.ToolResult) {
      content.ToolResult.forEach((result) => {
        if (result.Text) textParts.push(result.Text);
      });
    }
  });
  return textParts.join("\n");
}

const messageText = computed(() => getMessageText());
const hasCopyAction = computed(() => !!messageText.value);
// Info action on agent (usage) and user (lightweight metadata) for symmetry.
const hasUsageAction = computed(
  () => (props.message.type === "agent" && !!usage.value) || props.message.type === "user",
);
const hasForkAction = computed(
  () =>
    !!props.onFork &&
    !!props.message.message_id &&
    (props.message.type === "user" || props.message.type === "agent"),
);
const isCommentable = computed(() => !isUser.value && !isError.value && !isTool.value);

// ---- Tool maps (link tool_result back to tool_use) ----
const toolMaps = computed(() => {
  const toolUseMap: Record<string, { name: string; input: unknown }> = {};
  const serverToolResultMap: Record<string, LLMContent[]> = {};
  const m = llmMessage.value;
  if (m && m.Content) {
    m.Content.forEach((content) => {
      if (content.Type === 5 && content.ID && content.ToolName) {
        toolUseMap[content.ID] = { name: content.ToolName, input: content.ToolInput };
      }
      if (content.Type === 7 && content.ID && content.ToolName) {
        toolUseMap[content.ID] = { name: content.ToolName, input: content.ToolInput };
      }
      if (content.Type === 8 && content.ToolUseID && content.ToolResult) {
        serverToolResultMap[content.ToolUseID] = content.ToolResult;
      }
    });
  }
  return { toolUseMap, serverToolResultMap };
});
const toolUseMap = computed(() => toolMaps.value.toolUseMap);
const serverToolResultMap = computed(() => toolMaps.value.serverToolResultMap);

// ---- Error message details ----
const errorText = computed(() => {
  let text = "An error occurred";
  const m = llmMessage.value;
  if (m && m.Content && m.Content.length > 0) {
    const textContent = m.Content.find((c) => c.Type === 2);
    if (textContent && textContent.Text) text = textContent.Text;
  }
  return text;
});
const errorMeta = computed(() => {
  let retryable = false;
  if (props.message.user_data) {
    try {
      const ud =
        typeof props.message.user_data === "string"
          ? JSON.parse(props.message.user_data)
          : props.message.user_data;
      retryable = !!ud?.retryable;
    } catch {
      // ignore
    }
  }
  return { retryable };
});
const errorRetryable = computed(() => errorMeta.value.retryable);

// lastMessageId is provided by ChatInterface; an error message only offers its
// Retry button when it is the bottom-most message (see provide in
// ChatInterface.vue). Once a retry starts a new turn the error is no longer
// last and the button disappears.
const lastMessageId = inject<ComputedRef<string | null>>("lastMessageId");
const isLastMessage = computed(() => lastMessageId?.value === props.message.message_id);

// ---- Content filtering for the main path ----
const meaningfulContent = computed<LLMContent[]>(() => {
  return (
    llmMessage.value?.Content?.filter((c) => {
      const contentType = c.Type;
      if (contentType === 3) {
        return !!(c.Thinking || c.Text);
      }
      // 4 redacted_thinking, 5 tool_use, 6 tool_result, 7 server_tool_use,
      // 8 web_search_tool_result, 9 web_search_result.
      return (
        contentType !== 4 &&
        contentType !== 5 &&
        contentType !== 6 &&
        contentType !== 7 &&
        contentType !== 8 &&
        contentType !== 9 &&
        (c.Text?.trim() || contentType !== 2)
      );
    }) || []
  );
});

const hasOperationStatus = computed(() =>
  llmMessage.value?.Content?.some((c) => c.Type === 2 && c.Text?.includes("[Operation")),
);

const contentToRender = computed<LLMContent[]>(() =>
  meaningfulContent.value.length > 0
    ? meaningfulContent.value
    : llmMessage.value?.Content?.filter((c) => c.Type === 2 && c.Text?.includes("[Operation")) ||
      [],
);

// Merge adjacent text blocks (and inject inline citation markers) so a single
// sentence interrupted by web-search citation quotes renders as one flowing
// paragraph instead of several stray lines. See utils/coalesceContent.ts.
const coalescedContent = computed(() => coalesceContent(contentToRender.value));

// Whether the main path has anything to render (mirrors the React early-returns
// after the error/display_data branches).
const hasRenderableContent = computed(() => {
  const m = llmMessage.value;
  if (!m || !m.Content || m.Content.length === 0) return false;
  if (meaningfulContent.value.length === 0 && !hasOperationStatus.value) return false;
  return true;
});

// ---- Message container classes ----
const messageClasses = computed(() => {
  if (isUser.value && !isDistilledUser.value) {
    return `message message-user${isQueued.value ? " message-queued" : ""}`;
  }
  if (isError.value) return "message message-error";
  if (isTool.value) return "message message-tool";
  return "message message-agent";
});

// ---- Handlers ----
function handleMessageClick(e: MouseEvent) {
  // Don't toggle if clicking on a link, button, or interactive element.
  const target = e.target as HTMLElement;
  if (
    target.closest("a") ||
    target.closest("button") ||
    target.closest("[data-action-bar]") ||
    target.closest(".bash-tool-header") ||
    target.closest(".patch-tool-header") ||
    target.closest(".generic-tool-header") ||
    target.closest(".think-tool-header") ||
    target.closest(".keyword-search-tool-header") ||
    target.closest(".change-dir-tool-header") ||
    target.closest(".browser-tool-header") ||
    target.closest(".screenshot-tool-header")
  ) {
    return;
  }
  showActionBar.value = !showActionBar.value;
}

function handleCopy() {
  const text = messageText.value;
  if (text) {
    navigator.clipboard.writeText(text).catch((err) => {
      console.error("Failed to copy text:", err);
    });
  }
  showActionBar.value = false;
}

// Agent messages with token usage open the detailed usage modal; other
// messages (e.g. user messages) open a lightweight info modal so the action is
// available symmetrically.
function handleShowUsage() {
  if (usage.value) {
    showUsageModal.value = true;
  } else {
    showInfoModal.value = true;
  }
  showActionBar.value = false;
}

function handleFork() {
  if (props.onFork) props.onFork(props.message.message_id);
  showActionBar.value = false;
}

function openDistillationEditor(e: MouseEvent) {
  e.stopPropagation();
  showDistillationEditor.value = true;
}

function onQueuedCancel(e: MouseEvent) {
  e.stopPropagation();
  props.onCancelQueued?.();
}

// Close action bar when clicking outside (mirrors the React useEffect).
function handleClickOutside(e: MouseEvent) {
  const target = e.target as HTMLElement;
  if (!messageRef.value?.contains(target)) {
    showActionBar.value = false;
  }
}
watch(showActionBar, (open) => {
  if (open) {
    document.addEventListener("mousedown", handleClickOutside);
  } else {
    document.removeEventListener("mousedown", handleClickOutside);
  }
});
onUnmounted(() => document.removeEventListener("mousedown", handleClickOutside));
</script>
