import type {
  BtwExchange,
  BtwReaderDescriptor,
  BtwToolCall,
  BtwTurn,
  BtwTurnKind,
  Conversation,
  LLMContent,
  LLMMessage,
  Message,
} from "../types";
import type { ConversationCacheRecord, TransientState } from "./messageStore";

const CANCELLED_ANSWER = "[Operation cancelled]";
export const BTW_INTERRUPTED_ERROR = "Turn interrupted before completion.";

interface ProjectedTurn extends BtwTurn {
  retryable: boolean;
  unresolved: Set<string>;
}

type ChildState = Pick<ConversationCacheRecord, "conversation" | "messages">;

function json<T>(value: unknown): T | null {
  if (!value) return null;
  try {
    return (typeof value === "string" ? JSON.parse(value) : value) as T;
  } catch {
    return null;
  }
}

function text(content: readonly LLMContent[]): string {
  return content
    .filter((item) => item.Type === 2)
    .map((item) => item.Text ?? "")
    .join("");
}

function agentText(content: readonly LLMContent[]): { answer: string; cancelled: boolean } {
  let answer = "";
  let cancelled = false;
  for (const item of content) {
    if (item.Type !== 2) continue;
    if (item.Text === CANCELLED_ANSWER) cancelled = true;
    else answer += item.Text ?? "";
  }
  return { answer, cancelled };
}

function isToolResult(content: readonly LLMContent[]): boolean {
  return content.some((item) => item.Type === 6 || item.Type === 8);
}

function turnKind(message: Message): BtwTurnKind {
  const data = json<{ btw_turn_kind?: unknown }>(message.user_data);
  return data?.btw_turn_kind === "summary" ? "summary" : "question";
}

function summaryID(message: Message): string | undefined {
  const id = json<{ btw_summary_id?: unknown }>(message.user_data)?.btw_summary_id;
  return typeof id === "string" ? id : undefined;
}

function retryable(message: Message): boolean {
  return json<{ retryable?: unknown }>(message.user_data)?.retryable === true;
}

function toolCall(content: LLMContent): BtwToolCall {
  const call: BtwToolCall = { name: content.ToolName || "unknown" };
  if (content.ToolName !== "bash") return call;
  const input = json<{ command?: unknown }>(content.ToolInput);
  if (typeof input?.command === "string" && input.command.trim()) call.command = input.command;
  return call;
}

function newTurn(message: Message, content: readonly LLMContent[]): ProjectedTurn {
  const kind = turnKind(message);
  const summary_id = summaryID(message);
  return {
    id: message.message_id,
    question: kind === "summary" ? "Summary for main chat" : text(content),
    answer: "",
    status: "pending",
    error: null,
    retryable: false,
    kind,
    ...(summary_id ? { summary_id } : {}),
    tool_call_count: 0,
    tool_calls: [],
    unresolved_tool_call_count: 0,
    unresolved: new Set(),
  };
}

function applyContent(turn: ProjectedTurn, content: readonly LLMContent[]): void {
  for (const item of content) {
    if (item.Type === 5 || item.Type === 7) {
      turn.tool_call_count = (turn.tool_call_count ?? 0) + 1;
      turn.tool_calls = [...(turn.tool_calls ?? []), toolCall(item)];
      if (item.ID) turn.unresolved.add(item.ID);
    } else if ((item.Type === 6 || item.Type === 8) && item.ToolUseID) {
      turn.unresolved.delete(item.ToolUseID);
    }
  }
  turn.unresolved_tool_call_count = turn.unresolved.size;
}

function failInterrupted(turn: ProjectedTurn): void {
  turn.status = "failed";
  turn.error = BTW_INTERRUPTED_ERROR;
  turn.retryable = true;
}

function isTerminal(turn: ProjectedTurn): boolean {
  return turn.status === "completed" || turn.status === "cancelled" || turn.status === "failed";
}

function presentTurn(turn: ProjectedTurn): BtwTurn {
  return {
    id: turn.id,
    question: turn.question,
    answer: turn.answer,
    status: turn.status,
    error: turn.error,
    kind: turn.kind,
    ...(turn.summary_id ? { summary_id: turn.summary_id } : {}),
    tool_call_count: turn.tool_call_count,
    tool_calls: turn.tool_calls,
    unresolved_tool_call_count: turn.unresolved_tool_call_count,
  };
}

export function projectBtwReader(
  descriptor: BtwReaderDescriptor,
  child: ChildState | null,
  transient: Readonly<TransientState>,
): BtwExchange {
  const conversation: Conversation | null = child?.conversation ?? null;
  const messages = child?.messages ?? [];
  const turns: ProjectedTurn[] = [];
  let cursor = 0;
  const current = () => turns[cursor];
  const activate = (turn: ProjectedTurn) => {
    if (turn.status === "failed") {
      turn.answer = "";
      turn.error = null;
      turn.retryable = false;
    }
    turn.status = "active";
  };
  const advance = () => {
    if (cursor < turns.length - 1) activate(turns[++cursor]);
  };

  for (const message of [...messages].sort((a, b) => a.sequence_id - b.sequence_id)) {
    if (message.type === "system") continue;
    const parsed = json<LLMMessage>(message.llm_data);
    const content = parsed?.Content ?? [];
    if (message.type === "user" && !isToolResult(content)) {
      const turn = newTurn(message, content);
      const prior = current();
      if (
        prior &&
        (prior.status === "active" || prior.status === "pending") &&
        prior.kind === turn.kind &&
        prior.question === turn.question
      ) {
        failInterrupted(prior);
      }
      turns.push(turn);
      if (turns.length === 1 || (prior && isTerminal(prior))) {
        cursor = turns.length - 1;
        activate(turn);
      }
      continue;
    }
    const turn = current();
    if (!turn) continue;
    applyContent(turn, content);
    if (message.type === "agent") {
      activate(turn);
      const { answer, cancelled } = agentText(content);
      if (cancelled) {
        turn.status = "cancelled";
      } else {
        turn.answer += answer;
        if (message.end_of_turn ?? parsed?.EndOfTurn) turn.status = "completed";
      }
      if (turn.status !== "active") advance();
    } else if (message.type === "error") {
      turn.status = "failed";
      turn.error = text(content) || "BTW request failed";
      turn.retryable = retryable(message);
      advance();
    }
  }

  const unfinished = current();
  // A persisted successful/cancelled end-of-turn is authoritative even if
  // the list-backed working flags have not caught up yet. Failed turns are
  // the exception: /retry intentionally reuses the same immutable error row,
  // so a working flag is the only client-visible evidence that retry is live.
  if (unfinished && unfinished.status !== "completed" && unfinished.status !== "cancelled") {
    if (transient.agentWorking || conversation?.agent_working === true) {
      activate(unfinished);
      unfinished.answer += transient.streamingText;
    } else if (conversation?.agent_working === false) {
      for (const turn of turns) {
        if (turn.status !== "active" && turn.status !== "pending") continue;
        failInterrupted(turn);
      }
    }
  }
  const latest = turns.at(-1);

  return {
    exchange_id: descriptor.conversation_id,
    reader_slug: conversation?.slug ?? undefined,
    parent_conversation_id: descriptor.parent_conversation_id,
    status: latest?.status ?? "completed",
    retryable: latest?.retryable ?? false,
    parent_pointer: descriptor.parent_pointer,
    created_at:
      conversation?.created_at ??
      messages.find((message) => message.message_id === turns[0]?.id)?.created_at ??
      "",
    turns: turns.map(presentTurn),
  };
}
