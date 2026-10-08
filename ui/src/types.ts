// Types for Shelley UI
import {
  Conversation as GeneratedConversation,
  ConversationWithStateForTS,
  ApiMessageForTS,
  StreamResponseForTS,
  NotificationEventForTS,
  DiskSpaceStatus,
  Usage as GeneratedUsage,
  MessageType as GeneratedMessageType,
} from "./generated-types";

// Re-export generated types
export type Conversation = GeneratedConversation;
export type ConversationWithState = ConversationWithStateForTS;
export type { ConversationParticipant } from "./generated-types";
export type ConversationWithParticipants = Conversation & {
  participants?: import("./generated-types").ConversationParticipant[] | null;
};
export type Usage = GeneratedUsage;
export type MessageType = GeneratedMessageType;
export type { DiskSpaceStatus };

export interface BtwReaderDescriptor {
  readonly conversation_id: string;
  readonly parent_conversation_id: string;
  readonly parent_pointer: {
    readonly generation: number;
    readonly sequence_id: number;
  };
}

export type BtwTurnKind = "question" | "summary";
export type BtwTurnStatus = "pending" | "active" | "completed" | "cancelled" | "failed";

// Presentation-only view model projected from an ordinary child conversation.
export interface BtwTurn {
  id: string;
  question: string;
  answer: string;
  status: BtwTurnStatus;
  error?: string | null;
  kind: BtwTurnKind;
  tool_call_count?: number;
  tool_calls?: BtwToolCall[];
  unresolved_tool_call_count?: number;
}

export interface BtwToolCall {
  name: string;
  command?: string;
}

export interface BtwExchange {
  exchange_id: string;
  reader_slug?: string;
  parent_conversation_id: string;
  status: BtwTurnStatus;
  retryable: boolean;
  parent_pointer: BtwReaderDescriptor["parent_pointer"];
  created_at: string;
  turns: BtwTurn[];
}

// Extend the generated Message type with parsed data
export interface Message extends Omit<ApiMessageForTS, "type"> {
  type: MessageType;
}

// Go backend LLM struct format (capitalized field names)
export interface LLMMessage {
  Role: number; // 0 = user, 1 = assistant
  Content: LLMContent[];
  ToolUse?: unknown;
  EndOfTurn?: boolean;
}

export interface LLMContent {
  ID: string;
  Type: number; // llm.go ContentType: 2=text, 3=thinking, 4=redacted_thinking, 5=tool_use, 6=tool_result
  Text?: string;
  ToolName?: string;
  ToolInput?: unknown;
  ToolResult?: LLMContent[];
  ToolError?: boolean;
  // Other fields from Go struct
  MediaType?: string;
  DisplayImageURL?: string;
  DisplayWidth?: number;
  DisplayHeight?: number;
  Thinking?: string;
  Data?: string;
  Signature?: string;
  ToolUseID?: string;
  ToolUseStartTime?: string | null;
  ToolUseEndTime?: string | null;
  Display?: unknown;
  Cache?: boolean;
  // Web search result fields
  Title?: string;
  URL?: string;
  PageAge?: string;
  EncryptedContent?: string;
  // Citations attached to a text block (Anthropic web search). Serialized as a
  // JSON array on the wire; may arrive as the parsed array or a raw string.
  Citations?: unknown;
}

// API types
export interface Model {
  id: string;
  display_name?: string;
  source?: string; // Human-readable source (e.g., "exe.dev gateway", "$ANTHROPIC_API_KEY")
  mode?: string; // exe.dev integration mode (chatgpt, managed, byok); absent when unknown
  base_url?: string;
  api_type?: string;
  api_model_name?: string; // Native wire name, used with base_url to identify recorded usage
  ready: boolean;
  max_context_tokens?: number;
  is_default?: boolean;
  supports_images?: boolean;
  supports_reasoning?: boolean;
  reasoning_levels?: ("off" | "minimal" | "low" | "medium" | "high" | "xhigh" | "max")[];
  // Tier is 1 for prominent models and 2 for models overshadowed by a better
  // available sibling. The picker keeps tier-2 models behind a "more models"
  // toggle. Absent/0 is treated as tier 1 by the UI.
  tier?: number;
  // Reasoning level applied when a conversation carries no explicit
  // thinking_level override. Empty when the provider picks its own default.
  default_reasoning_level?: string;
}

export interface ChatRequest {
  message: string;
  model?: string;
  cwd?: string;
  /** Settings start from the profile (the default one if absent); those
   *  present override it. The model goes in `model` above. */
  conversation_options?: Omit<SettingsChange, "model"> & {
    disable_all_tools?: boolean;
    disable_notifications?: boolean;
  };
  queue?: boolean;
}

/** A conversation's adjustable settings; see Settings in server/settings.go. */
export interface Settings {
  /** "" in a profile means the server's default model. */
  model: string;
  /** "" means the model's default. */
  thinking_level: string;
  tool_overrides: Record<string, "on" | "off">;
  /** 0 means the default. */
  compact_nudge_tokens: number;
  /** A Go text/template; "" means the built-in one. */
  system_prompt: string;
}

/** A named Settings. Exactly one is the default. */
export interface Profile extends Settings {
  name: string;
  default: boolean;
}

/** GET /api/conversation/<id>/settings: the settings and the profile they
 *  came from, a label. */
export interface ConversationSettings extends Settings {
  profile: string;
}

/** The body of POST /api/conversation/<id>/settings: absent fields keep their
 *  values; profile first replaces them all with the profile's. */
export type SettingsChange = Partial<ConversationSettings>;

export interface SystemPromptInfo {
  template: string;
  variables: { name: string; description: string }[];
}
/** Why a system prompt template can't be rendered; column is 0-based. */
// Lines and columns (in characters) count from 1; left out when unknown.
export interface TemplateProblem {
  line?: number;
  column?: number;
  message: string;
}
// Notification event types
export type NotificationEventType = "agent_done" | "agent_error";

export interface NotificationEvent extends Omit<NotificationEventForTS, "type"> {
  type: NotificationEventType;
}

// ToolProgress represents partial output from a running tool.
export interface ToolProgress {
  tool_use_id: string;
  tool_name: string;
  output: string;
}

// StreamDelta represents a partial text delta from the LLM.
export interface StreamDelta {
  type: string; // "text" or "thinking"
  text: string;
  index: number;
  // seq is a per-conversation, monotonically increasing sequence number
  // assigned by the server to each broadcast delta. Clients can use it to
  // detect dropped or out-of-order partial updates.
  seq: number;
}

// StreamResponse represents the streaming response format
export interface StreamResponse extends Omit<StreamResponseForTS, "messages"> {
  messages?: Message[];
  context_window_size?: number;
  conversation_list_patch?: ConversationListPatchEvent;
  heartbeat?: boolean;
  notification_event?: NotificationEvent;
  disk_space_status?: DiskSpaceStatus;
  tool_progress?: ToolProgress;
  stream_delta?: StreamDelta;
}

// Link represents a custom link that can be added to the UI
export interface Link {
  title: string;
  icon_svg?: string; // SVG path data for the icon
  url: string;
}

// InitData is injected into window by the server
export interface InitData {
  models: Model[];
  default_model: string;
  default_cwd?: string;
  home_dir?: string;
  hostname?: string;
  terminal_url?: string;
  links?: Link[];
  user_agents_md_path?: string;
  notification_channel_types?: import("./services/api").ChannelTypeInfo[];
  exe_notify_available?: boolean; // VM has an exe.dev "notify" integration (push notifications)
  // model_setup_hint explains WHY models is empty, so the UI can give
  // actionable setup advice instead of guessing a model id. Absent when
  // models are available. See server/model_setup_hint.go for the tokens.
  model_setup_hint?: string;
  // is_exe_dev picks exe.dev-specific setup advice when model_setup_hint is
  // absent because the catalog emptied after page load.
  is_exe_dev?: boolean;
  // transcription_available says whether recordings can be transcribed
  // (a gpt-transcribe route exists, e.g. from OPENAI_API_KEY).
  transcription_available?: boolean;
  user_email?: string;
  banner?: string; // If set, shown as a top-of-page banner (e.g. to mark demo instances)
}

// Extend Window interface to include our init data
declare global {
  interface Window {
    __SHELLEY_INIT__?: InitData;
  }
}

// Git diff types
export interface GitDiffInfo {
  id: string;
  message: string;
  author: string;
  timestamp: string;
  filesCount: number;
  additions: number;
  deletions: number;
  // True when this commit has a guided tour git note.
  hasTour?: boolean;
  // Decorating refs (branches, tags, HEAD), like git log --decorate.
  refs?: string[];
  // True if this commit is the merge-base with @{upstream}.
  isMergeBase?: boolean;
}

export interface GitFileInfo {
  path: string;
  status: "added" | "modified" | "deleted";
  additions: number;
  deletions: number;
  isGenerated: boolean;
}

export interface GitFileDiff {
  path: string;
  oldContent: string;
  newContent: string;
}

export interface GitGraphCommit {
  hash: string;
  shortHash: string;
  parents: string[];
  subject: string;
  author: string;
  email: string;
  timestamp: number;
  refs: string[];
  isHead: boolean;
  // True when this commit has a guided tour git note.
  hasTour?: boolean;
  // True if this commit is the merge-base with @{upstream}.
  isMergeBase?: boolean;
}

export interface GitGraphResponse {
  commits: GitGraphCommit[];
  gitRoot: string;
  currentBranch: string;
  githubBase?: string;
}

export interface GitCommitDetailFile {
  path: string;
  additions: number;
  deletions: number;
  binary: boolean;
}

export interface GitCommitDetail {
  hash: string;
  subject: string;
  body: string;
  files: GitCommitDetailFile[];
  insTotal: number;
  delTotal: number;
}

export interface GitCommitMessage {
  hash: string;
  subject: string;
  body: string;
  author: string;
  isHead: boolean;
}

// Conversation list patch stream payload.
export interface ConversationListPatchOp {
  op: "add" | "remove" | "replace" | "move";
  path: string;
  from?: string;
  value?: unknown;
}

export interface ConversationListPatchEvent {
  old_hash?: string | null;
  new_hash: string;
  patch: ConversationListPatchOp[];
  at: string;
  // True when the patch replaces the whole list because the client has no
  // resumable hash. The generic patch applier handles this as a root replace.
  reset?: boolean;
}

// Version check types
export interface VersionInfo {
  current_version: string;
  current_tag?: string;
  current_commit?: string;
  current_commit_time?: string;
  latest_version?: string;
  latest_tag?: string;
  published_at?: string;
  has_update: boolean; // True if minor version is newer (show upgrade button)
  should_notify: boolean; // True if should show red dot (newer + 5 days apart)
  customized: boolean; // True for user-customized builds (upgrade via rebase)
  customization_dir?: string; // Canonical customization checkout (~/.config/shelley/shelley-customization)
  custom_commits?: CommitInfo[]; // Commits the customization branch carries on top of mainline
  download_url?: string;
  executable_path?: string;
  commits?: CommitInfo[];
  checked_at: string;
  error?: string;
  running_under_systemd: boolean; // True if INVOCATION_ID env var is set
  headless_shell_current?: string; // e.g. "Chromium 141.0.7390.55"
  headless_shell_latest?: string; // e.g. "Chromium 147.0.7727.24"
  headless_shell_update: boolean; // True if latest > current
}

export interface CommitInfo {
  sha: string;
  message: string;
  author: string;
  date: string;
}

// Helper to read a message's distill_status value ("in_progress" | "complete"
// | "error"), or null if the message is not a distill status message.
export function distillStatus(message: Message): string | null {
  if (!message.user_data) return null;
  try {
    const userData =
      typeof message.user_data === "string" ? JSON.parse(message.user_data) : message.user_data;
    return userData.distill_status || null;
  } catch {
    return null;
  }
}

// Helper to check if a message is a distill status message
export function isDistillStatusMessage(message: Message): boolean {
  return distillStatus(message) !== null;
}

// The working directory a user-driven cwd change moved to, or null if the
// message isn't one. These are user-role messages because the agent has to read
// them (a marker excluded from context would let it keep using the old
// directory), but they aren't something the user typed, so the UI renders them
// as a status line rather than a chat bubble. See recordCwdChangeNotice.
export function cwdChange(message: Message): { from: string; to: string } | null {
  if (!message.user_data) return null;
  try {
    const userData =
      typeof message.user_data === "string" ? JSON.parse(message.user_data) : message.user_data;
    if (!userData?.cwd_change) return null;
    return { from: userData.from || "", to: userData.to || "" };
  } catch {
    return null;
  }
}

// A context-size nudge (see recordContextNudge): the size line and the
// compact_in_place index sent with it; null if the message isn't one. Like cwd
// changes, the agent reads it but the user didn't type it, so it renders as a
// status line.
export function contextNudge(message: Message): { size: string; index: string } | null {
  if (!message.user_data || !message.llm_data) return null;
  try {
    const userData =
      typeof message.user_data === "string" ? JSON.parse(message.user_data) : message.user_data;
    if (!userData?.context_nudge) return null;
    const llmData =
      typeof message.llm_data === "string" ? JSON.parse(message.llm_data) : message.llm_data;
    return { size: llmData?.Content?.[0]?.Text ?? "", index: llmData?.Content?.[1]?.Text ?? "" };
  } catch {
    return null;
  }
}

// Helper to check if a message was copied verbatim into the current generation
// by a compaction (distill_method=compact). The UI collapses these behind a
// single "messages carried forward" band so the re-played tail isn't
// re-rendered one message at a time.
export function isCompactionCarried(message: Message): boolean {
  if (!message.user_data) return false;
  try {
    const userData =
      typeof message.user_data === "string" ? JSON.parse(message.user_data) : message.user_data;
    return userData.compaction_carried === "true";
  } catch {
    return false;
  }
}

// A queued user message held in the conversation's queued_messages JSON array
// while the agent is busy or while server-side work is preparing the final
// user turn. These are NOT messages rows — they are rendered as ghost/pending
// items at the bottom of the conversation and only become real (immutable)
// messages when the agent drains the queue. Mirror of db.QueuedMessage on the
// Go side.
export interface QueuedTranscription {
  media_path: string;
  contact_sheet_path?: string;
  context?: string;
}

export type QueuedMessageKind = "transcription";
export type QueuedMessageState = "working" | "ready" | "failed";

export interface QueuedMessage {
  // Shared queue metadata. Specialized items omit llm until their result is ready.
  id: string;
  llm?: LLMMessage;
  created_at: string;
  model: string;
  user_data?: unknown;
  kind?: QueuedMessageKind;
  state?: QueuedMessageState;
  transcription?: QueuedTranscription;
  error?: string;
}

// Parse the conversation.queued_messages JSON array into QueuedMessage[].
// Returns [] for empty/invalid input.
export function parseQueuedMessages(raw: string | undefined | null): QueuedMessage[] {
  if (!raw) return [];
  try {
    const arr = JSON.parse(raw);
    return Array.isArray(arr) ? (arr as QueuedMessage[]) : [];
  } catch {
    return [];
  }
}

// Extract the plain text of a queued message for display in the ghost item.
export function queuedMessageText(qm: QueuedMessage): string {
  const content = qm.llm?.Content;
  if (!Array.isArray(content)) return "";
  return content
    .filter((c) => c.Type === 2 && typeof c.Text === "string")
    .map((c) => c.Text)
    .join("");
}

export function queuedMessageRestoreText(qm: QueuedMessage): string {
  if (qm.kind !== "transcription" || qm.state === "ready") return queuedMessageText(qm);
  return qm.transcription?.context?.trim() ?? "";
}

// Only mutable transcription states get the specialized task card. A ready
// transcription is already an ordinary queued user turn and deliberately uses
// the same ghost rendering as every other queued message.
export function queuedTranscriptionTaskState(
  qm: QueuedMessage,
): Extract<QueuedMessageState, "working" | "failed"> | null {
  if (qm.kind !== "transcription") return null;
  return qm.state === "working" || qm.state === "failed" ? qm.state : null;
}

export function queuedTranscriptionPath(qm: QueuedMessage): string {
  return qm.transcription?.media_path ?? "";
}
