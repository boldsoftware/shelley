// The loop appends this sentinel as the final line of a tool result when
// the user cancels an in-progress tool (see cancelledToolResultText in
// loop/loop.go). Preserved tool output may precede it, so check that
// the result *ends with* the sentinel rather than merely contains it —
// otherwise output that happens to mention the phrase (e.g. grepping the
// codebase for it) would render as cancelled.
const CANCELLED_SENTINEL = "Tool execution cancelled by user";

export function isCancelledToolResult(text: string): boolean {
  return text.trimEnd().endsWith(CANCELLED_SENTINEL);
}
