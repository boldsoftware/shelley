// Helpers shared by the Vue Message rendering components, ported from the
// inline helpers in components/Message.tsx. Kept framework-agnostic.

// Convert Go struct Type field (number) to string type.
// Based on llm/llm.go constants (iota continues across types in same const
// block): MessageRoleUser = 0, MessageRoleAssistant = 1, ContentTypeText = 2,
// ContentTypeThinking = 3, ContentTypeRedactedThinking = 4,
// ContentTypeToolUse = 5, ContentTypeToolResult = 6,
// ContentTypeServerToolUse = 7, ContentTypeWebSearchToolResult = 8,
// ContentTypeWebSearchResult = 9.
export function getContentType(type: number): string {
  switch (type) {
    case 0:
      return "message_role_user";
    case 1:
      return "message_role_assistant";
    case 2:
      return "text";
    case 3:
      return "thinking";
    case 4:
      return "redacted_thinking";
    case 5:
      return "tool_use";
    case 6:
      return "tool_result";
    case 7:
      return "server_tool_use";
    case 8:
      return "web_search_tool_result";
    case 9:
      return "web_search_result";
    default:
      return "unknown";
  }
}
