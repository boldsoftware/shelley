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
  Thinking?: string;
  Data?: string;
  Signature?: string;
  ToolUseID?: string;
  ToolUseStartTime?: string | null;
  ToolUseEndTime?: string | null;
  Display?: unknown;
  Cache?: boolean;
}

// API types
export interface Model {
  id: string;
  display_name?: string;
  source?: string; // Human-readable source (e.g., "exe.dev gateway", "$ANTHROPIC_API_KEY")
  ready: boolean;
  max_context_tokens?: number;
}

export interface ChatRequest {
  message: string;
  model?: string;
  cwd?: string;
  conversation_options?: {
    type?: "normal" | "orchestrator";
    subagent_backend?: "shelley" | "claude-cli" | "codex-cli";
  };
  queue?: boolean;
}
// Notification event types
export type NotificationEventType = "agent_done" | "agent_error";

export interface NotificationEvent extends Omit<NotificationEventForTS, "type"> {
  type: NotificationEventType;
}

// StreamResponse represents the streaming response format
export interface StreamResponse extends Omit<StreamResponseForTS, "messages"> {
  messages: Message[];
  context_window_size?: number;
  conversation_list_update?: ConversationListUpdate;
  heartbeat?: boolean;
  notification_event?: NotificationEvent;
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
  user_agents_md_content?: string;
  notification_channel_types?: import("./services/api").ChannelTypeInfo[];
  cli_agents?: string[]; // Available CLI agents (e.g., "claude-cli", "codex-cli")
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

// Conversation list streaming update
export interface ConversationListUpdate {
  type: "update" | "delete";
  conversation?: Conversation;
  conversation_id?: string; // For deletes
  git_repo_root?: string;
  git_worktree_root?: string;
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
}

export interface CommitInfo {
  sha: string;
  message: string;
  author: string;
  date: string;
}

// Helper to check if a message is a distill status message
export function isDistillStatusMessage(message: Message): boolean {
  if (message.type !== "system" || !message.user_data) return false;
  try {
    const userData =
      typeof message.user_data === "string" ? JSON.parse(message.user_data) : message.user_data;
    return !!userData.distill_status;
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
