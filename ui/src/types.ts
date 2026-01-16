// Types for Shelley UI
import {
  Conversation as GeneratedConversation,
  ConversationWithStateForTS,
  ApiMessageForTS,
  StreamResponseForTS,
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
  ready: boolean;
  max_context_tokens?: number;
}

export interface ChatRequest {
  message: string;
  model?: string;
  cwd?: string;
}
// StreamResponse represents the streaming response format
export interface StreamResponse extends Omit<StreamResponseForTS, "messages"> {
  messages: Message[];
  context_window_size?: number;
  conversation_list_update?: ConversationListUpdate;
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
}
