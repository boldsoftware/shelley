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

  <!-- The user moved the conversation's working directory. A user-role message
       (the agent must read it), but not something the user typed, so it renders
       as a status line rather than a chat bubble. -->
  <CwdChangeMessage v-else-if="isCwdChange" :message="message" />

  <!-- system: render nothing. -->
  <template v-else-if="message.type === 'system'" />

  <!-- slug: render nothing. A slug marker exists only to carry the usage of the
       LLM call that named the conversation, so its cost is accounted for
       without rewriting an already-published message row. -->
  <template v-else-if="message.type === 'slug'" />

  <!-- warning -->
  <WarningMessage v-else-if="message.type === 'warning'" :message="message" />

  <!-- modelchange -->
  <ModelChangeMessage v-else-if="message.type === 'modelchange'" :message="message" />

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
      :data-message-id="message.message_id"
      role="alert"
      aria-label="Error message"
      @click="handleMessageClick($event, 'main')"
      @mouseenter="hoveredActionKey = 'main'"
      @mouseleave="hoveredActionKey = null"
    >
      <MessageActionBar
        v-if="actionBarVisible('main') && (hasCopyAction || hasUsageAction || hasForkAction)"
        :on-copy="hasCopyAction ? () => handleCopy(messageText) : undefined"
        :on-show-usage="hasUsageAction ? handleShowUsage : undefined"
        :on-fork="hasForkAction ? handleFork : undefined"
      />
      <div class="message-content" data-testid="message-content">
        <div class="whitespace-pre-wrap break-words">{{ errorText }}</div>
        <RefusalContinueButton
          v-if="isRefusal && isLastMessage"
          :conversation-id="message.conversation_id"
        />
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
      :data-message-id="message.message_id"
      role="article"
      @click="handleMessageClick($event, 'main')"
      @mouseenter="hoveredActionKey = 'main'"
      @mouseleave="hoveredActionKey = null"
    >
      <MessageActionBar
        v-if="actionBarVisible('main') && (hasCopyAction || hasUsageAction || hasForkAction)"
        :on-copy="hasCopyAction ? () => handleCopy(messageText) : undefined"
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
      @mouseenter="hoveredRow = true"
      @mouseleave="hoveredRow = false"
    >
      <div class="message-content message-content-entities" data-testid="message-content">
        <div v-if="authorEmail" class="message-author-email" data-testid="message-author-email">
          {{ authorEmail }}
        </div>
        <!-- One action region per content entity (thinking blocks stand alone,
             adjacent answer content stays grouped; see splitContentEntities).
             Copy is scoped to the entity; info/fork stay message-level. -->
        <div
          v-for="entity in contentEntities"
          :key="entity.key"
          class="msg-container-relative"
          :data-content-entity="entity.kind"
          @click="handleMessageClick($event, entity.key)"
          @mouseenter="hoveredActionKey = entity.key"
          @mouseleave="hoveredActionKey = null"
        >
          <MessageActionBar
            v-if="
              actionBarVisible(entity.key) &&
              (entity.copyText || hasUsageAction || hasForkAction)
            "
            :on-copy="entity.copyText ? () => handleCopy(entity.copyText) : undefined"
            :on-show-usage="hasUsageAction ? handleShowUsage : undefined"
            :on-fork="hasForkAction ? handleFork : undefined"
          />

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
                v-tooltip.top="'Edit distillation in modal'"
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
            <div v-for="(item, index) in entity.items" :key="index">
              <CitedText
                v-if="item.kind === 'text'"
                :text="item.text"
                :markdown-text="item.markdownText"
                :citations="item.citations"
                :render-markdown="shouldRenderMarkdown(markdownMode, isUser, isDistilledUser)"
                :message-id="message.message_id"
                :cache-owner="message"
                :run-key="`${entity.key}-${index}`"
              />
              <MessageContentBlock
                v-else
                :content="item.content!"
                :render-md="shouldRenderMarkdown(markdownMode, isUser, isDistilledUser)"
                :message-id="message.message_id"
                :tool-use-map="toolUseMap"
                :server-tool-result-map="serverToolResultMap"
                :on-comment-text-change="onCommentTextChange"
              />
            </div>
          </template>
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
  cwdChange,
  isDistillStatusMessage,
} from "../../types";
import { type MarkdownMode } from "../../services/settings";
import { useMarkdownMode } from "../composables/markdownMode";
import { usePerfLifecycle } from "../composables/perfLifecycle";
import { getContentType } from "../utils/messageContent";
import MarkdownContent from "./MarkdownContent.vue";
import MessageActionBar from "./MessageActionBar.vue";
import UsageDetailModal from "./UsageDetailModal.vue";
import MessageInfoModal from "./MessageInfoModal.vue";
import EditableFileModal from "./EditableFileModal.vue";
import GitInfoMessage from "./GitInfoMessage.vue";
import WarningMessage from "./WarningMessage.vue";
import ModelChangeMessage from "./ModelChangeMessage.vue";
import DistillStatusMessage from "./DistillStatusMessage.vue";
import CwdChangeMessage from "./CwdChangeMessage.vue";
import ErrorRetryButton from "./ErrorRetryButton.vue";
import RefusalContinueButton from "./RefusalContinueButton.vue";
import MessageContentBlock from "./MessageContentBlock.vue";
import CitedText from "./CitedText.vue";
import { coalesceContent, splitContentEntities } from "../../utils/coalesceContent";
import { perfCount } from "../../utils/perf";
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
  // onFork forks the conversation, copying messages up to and including this
  // one into a new conversation and navigating to it.
  onFork?: (messageId: string) => void;
}>();

const { markdownMode } = useMarkdownMode();

// Recomputation counters (see utils/perf.ts): mounts tell us how many Message
// components exist / get recreated; updates reveal wide prop-invalidation
// churn (historically, a toolProgress object identity change re-rendering
// every row — fixed by injecting tool progress; see composables/toolProgress.ts).
usePerfLifecycle("message");

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
const isCwdChange = computed(() => cwdChange(props.message) !== null);

// ---- Action bar state (show on hover or tap) ----
// One action bar per content entity, keyed by entity key ("main" for the
// single-region error and display_data branches).
const pinnedActionKey = ref<string | null>(null);
const hoveredActionKey = ref<string | null>(null);
const hoveredRow = ref(false);
const showUsageModal = ref(false);
const showInfoModal = ref(false);
const messageRef = ref<HTMLDivElement | null>(null);

// Hover temporarily takes precedence over a pinned region, so two sibling
// action bars can never be visible at once. rowActionKey is the fallback for a
// single-entity message, whose entity can be far narrower than the row.
function actionBarVisible(key: string): boolean {
  return key === (hoveredActionKey.value ?? rowActionKey.value ?? pinnedActionKey.value);
}

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

const llmMessage = computed<LLMMessage | null>(() => {
  perfCount("message.parseLlmData");
  return safeParse<LLMMessage>(props.message.llm_data, "LLM data");
});

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

// When multiple distinct users have participated in the conversation,
// ChatInterface provides showUserEmails=true so each human user message is
// labeled with its author's exe.dev email. Elided otherwise, and for
// distilled/compacted user messages (which render agent-side and aren't a
// single person's turn).
const showUserEmails = inject<ComputedRef<boolean>>("showUserEmails");
const authorEmail = computed(() =>
  isUser.value && !isDistilledUser.value && showUserEmails?.value
    ? props.message.user_email || null
    : null,
);

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
  let errorType = "";
  if (props.message.user_data) {
    try {
      const ud =
        typeof props.message.user_data === "string"
          ? JSON.parse(props.message.user_data)
          : props.message.user_data;
      retryable = !!ud?.retryable;
      errorType = typeof ud?.error_type === "string" ? ud.error_type : "";
    } catch {
      // ignore
    }
  }
  return { retryable, errorType };
});
const errorRetryable = computed(() => errorMeta.value.retryable);
// A refusal (stop_reason=refusal) is non-retryable on the same model, but the
// user can switch to a more capable model (Opus) and continue. Only the
// bottom-most refusal error offers the affordance.
const isRefusal = computed(() => errorMeta.value.errorType === "refusal");

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

// Thinking is a separate kind of output from the answer: each thinking block is
// its own action region so Copy grabs just that block. A distilled user message
// renders as a single box, so it stays one entity carrying the distillation text.
const contentEntities = computed(() =>
  isDistilledUser.value
    ? [
        {
          key: "distillation",
          kind: "content" as const,
          items: [],
          copyText: displayedDistillationContent.value || messageText.value,
        },
      ]
    : splitContentEntities(coalescedContent.value),
);

// A message with one content region shows at most one action bar, so hovering
// anywhere on the row can reveal it. This matters because an entity is only as
// wide as its content: a user bubble is shrink-to-fit and right-aligned
// (margin-left: auto; max-width: 80%), so most of the row is outside it and
// pointing at the row's centre never enters the entity at all. With two or more
// regions there is no single bar to show, and per-entity hover decides.
const rowActionKey = computed(() =>
  hoveredRow.value && contentEntities.value.length === 1 ? contentEntities.value[0].key : null,
);

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
    return `message message-user`;
  }
  if (isError.value) return "message message-error";
  if (isTool.value) return "message message-tool";
  return "message message-agent";
});

// ---- Handlers ----
function handleMessageClick(e: MouseEvent, key: string) {
  // Don't toggle if clicking on a link, button, or interactive element.
  const target = e.target as HTMLElement;
  if (
    target.closest("a") ||
    target.closest("button") ||
    // A markdown image is a bare <img> that opens the annotation view on click
    // (MarkdownContent.vue); toggling the action bar underneath it is noise.
    target.matches('img[role="button"]') ||
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
  pinnedActionKey.value = pinnedActionKey.value === key ? null : key;
}

function handleCopy(text: string) {
  if (text) {
    navigator.clipboard.writeText(text).catch((err) => {
      console.error("Failed to copy text:", err);
    });
  }
  pinnedActionKey.value = null;
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
  pinnedActionKey.value = null;
}

function handleFork() {
  if (props.onFork) props.onFork(props.message.message_id);
  pinnedActionKey.value = null;
}

function openDistillationEditor(e: MouseEvent) {
  e.stopPropagation();
  showDistillationEditor.value = true;
}

// Close a pinned action bar when clicking outside the message.
function handleClickOutside(e: MouseEvent) {
  const target = e.target as HTMLElement;
  if (!messageRef.value?.contains(target)) {
    pinnedActionKey.value = null;
  }
}
watch(pinnedActionKey, (open) => {
  if (open) {
    document.addEventListener("mousedown", handleClickOutside);
  } else {
    document.removeEventListener("mousedown", handleClickOutside);
  }
});
onUnmounted(() => document.removeEventListener("mousedown", handleClickOutside));
</script>
