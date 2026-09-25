// Shared message/tool coalescing logic and types for the conversation render model.
import {
  type Message,
  type LLMContent,
  isDistillStatusMessage,
  distillStatus,
  isCompactionCarried,
} from "../../types";
import { isCancelledToolResult } from "../utils/toolStatus";

export interface CoalescedItem {
  type: "message" | "tool";
  generation: number;
  /** Sequence position represented by this visible transcript item. */
  sourceSequenceID: number;
  /** Stable identity for inserting transcript-adjacent UI. */
  anchorKey: string;
  // carried marks an item copied verbatim from the previous generation by a
  // compaction. The UI collapses these behind a single band.
  carried?: boolean;
  message?: Message;
  toolUseId?: string;
  toolName?: string;
  toolInput?: unknown;
  toolResult?: LLMContent[];
  toolError?: boolean;
  toolInvokedAt?: string | null;
  toolStartTime?: string | null;
  toolEndTime?: string | null;
  hasResult?: boolean;
  toolInterrupted?: boolean;
  display?: unknown;
}

export function coalesceMessages(
  messages: Message[],
  interruptedGeneration?: number,
): CoalescedItem[] {
  if (messages.length === 0) return [];

  const items: CoalescedItem[] = [];
  // Distillation status is split into two immutable messages: an in_progress one
  // at start and a terminal (complete/error) one when it finishes. Once a
  // terminal status arrives, suppress the earlier in_progress one so the spinner
  // doesn't linger beside the result. Grouping key: source_slug + new_generation
  // (distillations are sequential per conversation). A still-running
  // distillation has only the in_progress message and keeps showing the spinner.
  const supersededInProgress = new Set<string>();
  {
    const lastInProgress = new Map<string, Message>();
    messages.forEach((message) => {
      const status = distillStatus(message);
      if (!status) return;
      let key = "";
      try {
        const ud =
          typeof message.user_data === "string" ? JSON.parse(message.user_data) : message.user_data;
        key = `${ud?.source_slug ?? ""}\u0000${ud?.new_generation ?? ""}`;
      } catch {
        key = "";
      }
      if (status === "in_progress") {
        lastInProgress.set(key, message);
      } else {
        const prior = lastInProgress.get(key);
        if (prior) {
          supersededInProgress.add(prior.message_id);
          lastInProgress.delete(key);
        }
      }
    });
  }
  const toolResultMap: Record<
    string,
    {
      result: LLMContent[];
      error: boolean;
      startTime: string | null;
      endTime: string | null;
      interrupted: boolean;
    }
  > = {};
  const displayDataMap: Record<string, unknown> = {};

  // First pass: collect all tool results + display data.
  messages.forEach((message) => {
    if (message.llm_data) {
      try {
        const llmData =
          typeof message.llm_data === "string" ? JSON.parse(message.llm_data) : message.llm_data;
        if (llmData && llmData.Content && Array.isArray(llmData.Content)) {
          const userData =
            typeof message.user_data === "string"
              ? JSON.parse(message.user_data)
              : message.user_data;
          llmData.Content.forEach((content: LLMContent) => {
            if (content && content.Type === 6 && content.ToolUseID) {
              toolResultMap[content.ToolUseID] = {
                result: content.ToolResult || [],
                error: content.ToolError || false,
                startTime: content.ToolUseStartTime || null,
                endTime: content.ToolUseEndTime || null,
                interrupted:
                  userData?.interrupted_tool_result === true ||
                  (content.ToolError === true &&
                    (content.ToolResult || []).some(
                      (result) => result.Type === 2 && isCancelledToolResult(result.Text || ""),
                    )),
              };
              if (content.Display) {
                displayDataMap[content.ToolUseID] = content.Display;
              }
            }
          });
        }
      } catch (err) {
        console.error("Failed to parse message LLM data for tool results:", err);
      }
    }
  });

  // Second pass: process messages and extract tool uses.
  messages.forEach((message) => {
    const carried = isCompactionCarried(message);
    // Suppress an in_progress distill status message once its terminal
    // (complete/error) counterpart has arrived, so the spinner doesn't linger.
    if (supersededInProgress.has(message.message_id)) return;
    // Slug markers carry only the usage of the LLM call that named the
    // conversation; they have no content and render as nothing. Drop them here
    // rather than in the renderer: a coalesced item drives timestamp and
    // day-separator emission, so keeping one would print a stray "Thu, Jul 30"
    // header with nothing underneath it. Their cost is read straight off
    // messages in ChatInterface's usage walk, not from coalesced items.
    if (message.type === "slug") return;
    if (message.type === "system") {
      if (!isDistillStatusMessage(message)) return;
      items.push(messageItem(message, carried));
      return;
    }

    if (message.type === "error" || message.type === "warning" || message.type === "modelchange") {
      items.push(messageItem(message, carried));
      return;
    }

    let hasToolResult = false;
    if (message.llm_data) {
      try {
        const llmData =
          typeof message.llm_data === "string" ? JSON.parse(message.llm_data) : message.llm_data;
        if (llmData && llmData.Content && Array.isArray(llmData.Content)) {
          hasToolResult = llmData.Content.some((c: LLMContent) => c.Type === 6);
        }
      } catch (err) {
        console.error("Failed to parse message LLM data:", err);
      }
    }

    if (message.type === "user" && !hasToolResult) {
      items.push(messageItem(message, carried));
      return;
    }
    if (message.type === "user" && hasToolResult) {
      return;
    }

    if (message.llm_data) {
      try {
        const llmData =
          typeof message.llm_data === "string" ? JSON.parse(message.llm_data) : message.llm_data;
        if (llmData && llmData.Content && Array.isArray(llmData.Content)) {
          const textContents: LLMContent[] = [];
          const toolUses: LLMContent[] = [];
          const serverToolResults: Record<string, LLMContent[]> = {};
          let hasThinking = false;
          // Block positions, used to decide whether this message's text was
          // spoken before or after its tool calls. Providers that run
          // server-side tools (web_search) return a single assistant message
          // whose blocks interleave reasoning, tool calls, and the final
          // answer; that answer is written last and must not be rendered above
          // the searches that produced it.
          let lastToolIndex = -1;
          let firstTextIndex = -1;

          llmData.Content.forEach((content: LLMContent, index: number) => {
            if (content.Type === 2) {
              textContents.push(content);
              if (firstTextIndex < 0 && (content.Text || "").trim()) firstTextIndex = index;
            } else if (content.Type === 3 && (content.Thinking || content.Text)) {
              // Non-empty thinking block. Adaptive-thinking models may emit a
              // signature-only block (empty Thinking/Text); that one is not
              // renderable, mirroring meaningfulContent in Message.vue.
              hasThinking = true;
            } else if (content.Type === 5 || content.Type === 7) {
              toolUses.push(content);
              lastToolIndex = index;
            } else if (content.Type === 8 && content.ToolUseID && content.ToolResult) {
              serverToolResults[content.ToolUseID] = content.ToolResult;
            }
          });

          const textString = textContents
            .map((c) => c.Text || "")
            .join("")
            .trim();
          // A turn with only thinking + tool calls (no text) still needs a
          // message item, or the thinking block would never render.
          const needsMessageItem = !!textString || hasThinking;
          // Text written after every tool call is a reply to those calls, so
          // its message item follows them. Everything else (preamble text,
          // thinking-only turns) keeps its historical position ahead of the
          // tools.
          const textFollowsTools =
            !!textString && lastToolIndex >= 0 && firstTextIndex > lastToolIndex;
          if (needsMessageItem && !textFollowsTools) {
            items.push(messageItem(message, carried));
          }

          const wasTruncated = llmData.ExcludedFromContext === true;

          toolUses.forEach((toolUse) => {
            const resultData = toolUse.ID ? toolResultMap[toolUse.ID] : undefined;
            const serverResult = toolUse.ID ? serverToolResults[toolUse.ID] : undefined;
            const displayData = toolUse.ID ? displayDataMap[toolUse.ID] : undefined;
            const isServerSideToolUse = toolUse.Type === 7;
            const hasResult = !!resultData || !!serverResult || wasTruncated || isServerSideToolUse;
            items.push({
              type: "tool",
              generation: message.generation,
              carried,
              // Keep anchor placement stable when the later tool-result row
              // arrives; the tool begins at its assistant invocation.
              sourceSequenceID: message.sequence_id,
              anchorKey: `tool:${toolUse.ID || `${message.message_id}-${toolUse.ToolName || "unknown"}`}`,
              toolUseId: toolUse.ID,
              toolName: toolUse.ToolName,
              toolInput: toolUse.ToolInput,
              toolResult: resultData?.result || serverResult,
              toolError: resultData?.error || (wasTruncated && !resultData && !serverResult),
              // Approximate while running: the exact start time is only sent
              // with the result. Keep it separate so transcript timestamps
              // still use the result's actual start time when available.
              toolInvokedAt: message.created_at,
              toolStartTime: resultData?.startTime,
              toolEndTime: resultData?.endTime,
              hasResult,
              toolInterrupted:
                !!resultData?.interrupted ||
                (!hasResult &&
                  interruptedGeneration !== undefined &&
                  message.generation === interruptedGeneration),
              display: displayData,
            });
          });

          if (needsMessageItem && textFollowsTools) {
            items.push(messageItem(message, carried));
          }
        }
      } catch (err) {
        console.error("Failed to parse message LLM data:", err);
        items.push(messageItem(message, carried));
      }
    } else {
      items.push(messageItem(message, carried));
    }
  });

  return items;
}

function messageItem(message: Message, carried: boolean): CoalescedItem {
  return {
    type: "message",
    generation: message.generation,
    carried,
    message,
    sourceSequenceID: message.sequence_id,
    anchorKey: `message:${message.message_id}`,
  };
}
