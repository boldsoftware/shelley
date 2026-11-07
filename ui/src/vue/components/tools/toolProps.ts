import type { LLMContent } from "../../../types";

/** The props CoalescedToolCall passes every tool component. */
export interface ToolProps {
  toolInput?: unknown;
  isRunning?: boolean;
  toolInvokedAt?: string | null;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  display?: unknown;
}

/** Whether the call has finished and its result is in. */
export function toolComplete(p: ToolProps): boolean {
  return !p.isRunning && p.toolResult !== undefined;
}

/** The input as an object of fields; {} when it is not one. */
export function inputFields<T extends object = Record<string, unknown>>(
  input: unknown,
): Partial<T> {
  return typeof input === "object" && input !== null ? (input as Partial<T>) : {};
}

/** The input's string field key, or "" when it has none. */
export function inputString(input: unknown, key: string): string {
  const v = inputFields(input)[key];
  return typeof v === "string" ? v : "";
}

/** The text of the result's first part. */
export function firstResultText(result: LLMContent[] | undefined): string {
  return result?.[0]?.Text || "";
}

/** The text of all of the result's parts, joined. */
export function resultText(result: LLMContent[] | undefined): string {
  return (
    result
      ?.map((r) => r.Text)
      .filter(Boolean)
      .join("") || ""
  );
}
