// Shared coalescing logic + types for ChatInterface.vue, extracted from the
// React ChatInterface.tsx so the SFC and ToolPillsRow can share the
// CoalescedItem type. Mirrors the original coalescedItems useMemo body.
import {
  type Message,
  type LLMContent,
  isDistillStatusMessage,
  isCompactionCarried,
} from "../../types";

export interface CoalescedItem {
  type: "message" | "tool";
  generation: number;
  // carried marks an item copied verbatim from the previous generation by a
  // compaction. The UI collapses these behind a single band.
  carried?: boolean;
  message?: Message;
  toolUseId?: string;
  toolName?: string;
  toolInput?: unknown;
  toolResult?: LLMContent[];
  toolError?: boolean;
  toolStartTime?: string | null;
  toolEndTime?: string | null;
  hasResult?: boolean;
  display?: unknown;
}

export function coalesceMessages(messages: Message[]): CoalescedItem[] {
  if (messages.length === 0) return [];

  const items: CoalescedItem[] = [];
  const toolResultMap: Record<
    string,
    { result: LLMContent[]; error: boolean; startTime: string | null; endTime: string | null }
  > = {};
  const displayDataMap: Record<string, unknown> = {};

  // First pass: collect all tool results + display data.
  messages.forEach((message) => {
    if (message.llm_data) {
      try {
        const llmData =
          typeof message.llm_data === "string" ? JSON.parse(message.llm_data) : message.llm_data;
        if (llmData && llmData.Content && Array.isArray(llmData.Content)) {
          llmData.Content.forEach((content: LLMContent) => {
            if (content && content.Type === 6 && content.ToolUseID) {
              toolResultMap[content.ToolUseID] = {
                result: content.ToolResult || [],
                error: content.ToolError || false,
                startTime: content.ToolUseStartTime || null,
                endTime: content.ToolUseEndTime || null,
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
    if (message.type === "system") {
      if (!isDistillStatusMessage(message)) return;
      items.push({ type: "message", generation: message.generation, carried, message });
      return;
    }

    if (message.type === "error" || message.type === "warning") {
      items.push({ type: "message", generation: message.generation, carried, message });
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
      items.push({ type: "message", generation: message.generation, carried, message });
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

          llmData.Content.forEach((content: LLMContent) => {
            if (content.Type === 2) {
              textContents.push(content);
            } else if (content.Type === 5 || content.Type === 7) {
              toolUses.push(content);
            } else if (content.Type === 8 && content.ToolUseID && content.ToolResult) {
              serverToolResults[content.ToolUseID] = content.ToolResult;
            }
          });

          const textString = textContents
            .map((c) => c.Text || "")
            .join("")
            .trim();
          if (textString) {
            items.push({ type: "message", generation: message.generation, carried, message });
          }

          const wasTruncated = llmData.ExcludedFromContext === true;

          toolUses.forEach((toolUse) => {
            const resultData = toolUse.ID ? toolResultMap[toolUse.ID] : undefined;
            const serverResult = toolUse.ID ? serverToolResults[toolUse.ID] : undefined;
            const displayData = toolUse.ID ? displayDataMap[toolUse.ID] : undefined;
            const isServerSideToolUse = toolUse.Type === 7;
            items.push({
              type: "tool",
              generation: message.generation,
              carried,
              toolUseId: toolUse.ID,
              toolName: toolUse.ToolName,
              toolInput: toolUse.ToolInput,
              toolResult: resultData?.result || serverResult,
              toolError: resultData?.error || (wasTruncated && !resultData && !serverResult),
              toolStartTime: resultData?.startTime,
              toolEndTime: resultData?.endTime,
              hasResult: !!resultData || !!serverResult || wasTruncated || isServerSideToolUse,
              display: displayData,
            });
          });
        }
      } catch (err) {
        console.error("Failed to parse message LLM data:", err);
        items.push({ type: "message", generation: message.generation, carried, message });
      }
    } else {
      items.push({ type: "message", generation: message.generation, carried, message });
    }
  });

  return items;
}
