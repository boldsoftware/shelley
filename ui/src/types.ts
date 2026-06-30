// Types for Shelley UI
import {
  Conversation as GeneratedConversation,
  ConversationWithStateForTS,
  ApiMessageForTS,
  StreamResponseForTS,
  NotificationEventForTS,
  Usage as GeneratedUsage,
  MessageType as GeneratedMessageType,
} from "./generated-types";

// Re-export generated types
export type Conversation = GeneratedConversation;
export type ConversationWithState = ConversationWithStateForTS;
export type Usage = GeneratedUsage;
export type MessageType = GeneratedMessageType;

// Extend the generated Message type with parsed data
export interface Message extends Omit<ApiMessageForTS, "type"> {
  type: MessageType;
}

// Go backend LLM struct format (capitalized field names)
export interface LLMMessage {
  Role: number; // 0 = user, 1 = assistant
  Content: LLMContent[];
  ToolUse?: unknown;
}

export interface LLMContent {
  ID: string;
  Type: number; // 2 = text, 3 = tool_use, 4 = tool_result, 5 = thinking
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
  base_url?: string;
  api_type?: string;
  ready: boolean;
  max_context_tokens?: number;
  is_default?: boolean;
  supports_images?: boolean;
}

export interface ChatRequest {
  message: string;
  model?: string;
  cwd?: string;
  conversation_options?: {
    type?: "normal" | "orchestrator";
    subagent_backend?: "shelley" | "claude-cli" | "codex-cli";
    tool_overrides?: Record<string, "on" | "off">;
    disable_all_tools?: boolean;
    thinking_level?: "off" | "minimal" | "low" | "medium" | "high" | "xhigh";
  };
  queue?: boolean;
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
  cli_agents?: string[]; // Available CLI agents (e.g., "claude-cli", "codex-cli")
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

// Comment for diff viewer
export interface DiffComment {
  id: string;
  line: number;
  side: "left" | "right";
  text: string;
  selectedText?: string;
  startLine?: number;
  endLine?: number;
  filePath: string;
  diffId: string;
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

// Helper to check if a user message is queued (waiting for agent to finish)
export function isQueuedMessage(message: Message): boolean {
  if (message.type !== "user" || !message.user_data) return false;
  try {
    const userData =
      typeof message.user_data === "string" ? JSON.parse(message.user_data) : message.user_data;
    return !!userData.queued;
  } catch {
    return false;
  }
}
