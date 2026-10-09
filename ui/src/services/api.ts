import {
  Conversation,
  ConversationWithParticipants,
  ConversationWithState,
  DiskSpaceStatus,
  StreamResponse,
  ChatRequest,
  BtwReaderDescriptor,
  GitDiffInfo,
  GitFileInfo,
  GitFileDiff,
  VersionInfo,
  CommitInfo,
  ConversationSettings,
  Profile,
  Settings,
  SettingsChange,
  SystemPromptInfo,
  TemplateProblem,
} from "../types";

// Extract a useful error message from a failed fetch response. Prefers the
// response body (which may contain a server-side detail like a hook error),
// falls back to statusText, then to the numeric status.
export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
  }
}

async function responseError(response: Response, prefix: string): Promise<ApiError> {
  let detail = "";
  try {
    detail = (await response.text()).trim();
  } catch {
    // ignore
  }
  if (!detail) {
    detail = response.statusText || `HTTP ${response.status}`;
  }
  return new ApiError(`${prefix}: ${detail}`, response.status);
}

// A backgrounded bash command that has not exited (server BackgroundJobInfo).
export interface BackgroundJob {
  job_id: string;
  command: string;
  tail: string;
  started_at: string;
}

export interface AvailableModel {
  id: string;
  display_name?: string;
  source?: string;
  mode?: string;
  base_url?: string;
  api_type?: string;
  api_model_name?: string;
  ready: boolean;
  max_context_tokens?: number;
  is_default?: boolean;
  supports_images?: boolean;
}

export interface AttachedIntegration {
  name: string;
  type: string;
  comment?: string;
  help?: string;
  team?: boolean;
  url: string;
  details?: {
    repositories?: { name: string; url: string; clone_command: string }[];
    model_counts?: {
      provider: string;
      mode?: string;
      chat?: number;
      embeddings?: number;
      transcription?: number;
      other?: number;
    }[];
    models_error?: string;
  };
}

export interface IntegrationsResponse {
  integrations: AttachedIntegration[];
}

export interface GitTourHeaderEntry {
  header: string;
}

export interface GitTourPatchEntry {
  patch: string;
  comment?: string;
  trivial?: boolean;
}

/** A screenshot or recording, stored in the repository as a git blob. */
export interface GitTourMediaEntry {
  blob: string;
  mime: string;
  name: string;
  comment?: string;
}

export type GitTourEntry = GitTourHeaderEntry | GitTourPatchEntry | GitTourMediaEntry;

/** A key design decision or a question for the reader. */
export interface GitTourItem {
  title: string;
  body?: string;
}

export interface GitTour {
  version: 1;
  title?: string;
  intro?: string;
  decisions?: GitTourItem[];
  questions?: GitTourItem[];
  chunks: GitTourEntry[];
}

export type GitTourBuildState = "absent" | "building" | "present" | "failed";

export interface GitTourBuildStatus {
  status: GitTourBuildState;
  hash: string;
  repository?: string;
  worker_conversation_id?: string;
  worker_slug?: string;
  error?: string;
}

export interface GitTourResponse {
  hash: string;
  tour: GitTour;
}

export interface ChatAcceptedResponse {
  status?: string;
  btw?: BtwReaderDescriptor;
  tour?: GitTourBuildStatus;
}

export interface BtwSummaryReceipt {
  status?: string;
  message_id: string;
  btw: BtwReaderDescriptor;
}

class ApiService {
  private baseUrl = "/api";

  private postHeaders = {
    "Content-Type": "application/json",
  };

  async getConversations(): Promise<ConversationWithState[]> {
    const response = await fetch(`${this.baseUrl}/conversations`);
    if (!response.ok) {
      throw await responseError(response, "Failed to get conversations");
    }
    return response.json();
  }

  async getConversationsSnapshot(): Promise<{
    conversations: ConversationWithState[];
    hash: string;
  }> {
    const response = await fetch(`${this.baseUrl}/conversations/snapshot`);
    if (!response.ok) {
      throw await responseError(response, "Failed to load conversations");
    }
    return response.json();
  }

  async getModels(): Promise<AvailableModel[]> {
    const response = await fetch(`${this.baseUrl}/models`);
    if (!response.ok) {
      throw await responseError(response, "Failed to get models");
    }
    return response.json();
  }

  async getIntegrations(): Promise<IntegrationsResponse> {
    const response = await fetch(`${this.baseUrl}/integrations`);
    if (!response.ok) {
      throw await responseError(response, "Failed to load integrations");
    }
    return response.json();
  }

  async getIntegrationDetails(name: string, team: boolean): Promise<AttachedIntegration> {
    const params = new URLSearchParams({ details: name, team: String(team) });
    const response = await fetch(`${this.baseUrl}/integrations?${params}`);
    if (!response.ok) {
      throw await responseError(response, "Failed to load integration details");
    }
    const body: IntegrationsResponse = await response.json();
    return body.integrations[0];
  }

  async sendTestNotification(message: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/integrations/notify/test`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({ message }),
    });
    if (!response.ok) {
      throw await responseError(response, "Could not send test notification");
    }
  }

  async sendSlackTest(name: string, team: boolean, message: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/integrations/slack/test`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({ name, team, message }),
    });
    if (!response.ok) {
      throw await responseError(response, "Could not send Slack test message");
    }
  }

  async refreshModels(): Promise<AvailableModel[]> {
    const response = await fetch(`${this.baseUrl}/models/refresh`, {
      method: "POST",
      headers: this.postHeaders,
    });
    if (!response.ok) {
      throw await responseError(response, "Failed to refresh models");
    }
    return response.json();
  }

  async getTools(): Promise<{
    tools: Array<{ name: string; summary: string; default_on: boolean }>;
  }> {
    const response = await fetch(`${this.baseUrl}/tools`);
    if (!response.ok) {
      throw await responseError(response, "Failed to get tools");
    }
    return response.json();
  }

  // searchConversationsFTS performs a full-text search across both active AND
  // archived top-level conversations, using SQLite FTS5 over message bodies.
  async searchConversationsFTS(
    query: string,
    signal?: AbortSignal,
  ): Promise<ConversationWithState[]> {
    const params = new URLSearchParams({ q: query });
    const response = await fetch(`${this.baseUrl}/conversations/search?${params}`, { signal });
    if (!response.ok) {
      throw await responseError(response, "Failed to search conversations");
    }
    return response.json();
  }

  // createDraft creates a draft conversation server-side. Drafts hold the
  // unsent message text in a `draft` column and have no messages until the
  // user actually sends. They appear in the conversation list like any
  // other conversation, can be deleted, and get promoted (is_draft=false)
  // automatically when the first message is posted to them.
  async createDraft(request: {
    draft: string;
    model?: string;
    cwd?: string;
    conversation_options?: ChatRequest["conversation_options"];
  }): Promise<Conversation> {
    const response = await fetch(`${this.baseUrl}/conversations/draft`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify(request),
    });
    if (!response.ok) {
      throw await responseError(response, "Failed to create draft");
    }
    return response.json();
  }

  // updateDraft partially updates a draft conversation in place: the draft
  // text (composer autosave), the model (composer picker), and/or the cwd
  // (command palette). Omitted fields keep their current value, so the
  // three update independently without clobbering each other. Returns 404
  // once the draft has been promoted (the model then travels with the chat
  // POST, and cwd is immutable thereafter); callers treat that as a no-op.
  async updateDraft(
    conversationId: string,
    fields: { draft?: string; model?: string; cwd?: string },
  ): Promise<Conversation> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/draft`, {
      method: "PUT",
      headers: this.postHeaders,
      body: JSON.stringify(fields),
    });
    if (!response.ok) {
      throw await responseError(response, "Failed to update draft");
    }
    return response.json();
  }

  async sendMessageWithNewConversation(request: ChatRequest): Promise<{ conversation_id: string }> {
    const response = await fetch(`${this.baseUrl}/conversations/new`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify(request),
    });
    if (!response.ok) {
      throw await responseError(response, "Failed to start conversation");
    }
    return response.json();
  }

  async distillNewGeneration(
    sourceConversationId: string,
    model?: string,
    cwd?: string,
    method?: "default" | "compact",
    instructions?: string,
  ): Promise<{ conversation_id: string; current_generation: number }> {
    const response = await fetch(`${this.baseUrl}/conversations/distill-new-generation`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({
        source_conversation_id: sourceConversationId,
        model: model || "",
        cwd: cwd || "",
        method: method || "default",
        instructions: instructions || "",
      }),
    });
    if (!response.ok) {
      throw await responseError(response, "Failed to distill into new generation");
    }
    return response.json();
  }

  async startNewGeneration(conversationId: string): Promise<Conversation> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/new-generation`, {
      method: "POST",
    });
    if (!response.ok) {
      throw await responseError(response, "Failed to start new generation");
    }
    return response.json();
  }

  async getConversationWithProgress(
    conversationId: string,
    onProgress?: (progress: {
      phase: "downloading" | "parsing";
      bytesDownloaded: number;
      bytesTotal?: number;
    }) => void,
  ): Promise<StreamResponse> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}`);
    if (!response.ok) {
      throw await responseError(response, "Failed to get messages");
    }

    const contentLengthHeader = response.headers.get("Content-Length");
    const contentLength = contentLengthHeader ? Number(contentLengthHeader) : undefined;

    if (!response.body) {
      onProgress?.({
        phase: "parsing",
        bytesDownloaded: contentLength ?? 0,
        bytesTotal: contentLength,
      });
      return response.json();
    }

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    const chunks: string[] = [];
    let bytesDownloaded = 0;

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      if (!value) continue;
      bytesDownloaded += value.byteLength;
      onProgress?.({
        phase: "downloading",
        bytesDownloaded,
        bytesTotal: contentLength,
      });
      chunks.push(decoder.decode(value, { stream: true }));
    }

    chunks.push(decoder.decode());
    onProgress?.({
      phase: "parsing",
      bytesDownloaded,
      bytesTotal: contentLength,
    });

    try {
      return JSON.parse(chunks.join("")) as StreamResponse;
    } catch {
      throw new Error("Failed to parse conversation response");
    }
  }

  /**
   * Fetch only the messages after `sinceSequenceId`.
   *
   * Used when the local cache already holds a contiguous history and just
   * needs to re-check the tail (e.g. after a stream reconnect). Costs a few
   * hundred bytes instead of re-downloading a whole conversation, which for
   * a long one is megabytes of JSON.
   */
  async getConversationSince(
    conversationId: string,
    sinceSequenceId: number,
  ): Promise<StreamResponse> {
    const response = await fetch(
      `${this.baseUrl}/conversation/${conversationId}?last_sequence_id=${sinceSequenceId}`,
    );
    if (!response.ok) {
      throw await responseError(response, `Failed to get messages since ${sinceSequenceId}`);
    }
    return response.json();
  }

  // Saves body in the server's upload directory; resolves to its absolute path.
  async uploadRaw(filename: string, body: Blob): Promise<string> {
    const response = await fetch(
      `${this.baseUrl}/upload/raw?filename=${encodeURIComponent(filename)}`,
      {
        method: "POST",
        headers: { "Content-Type": body.type || "application/octet-stream" },
        body,
      },
    );
    if (!response.ok) throw await responseError(response, `Failed to upload ${filename}`);
    const { path } = (await response.json()) as { path?: unknown };
    if (typeof path !== "string" || !path)
      throw new Error(`Upload of ${filename} returned no path`);
    return path;
  }

  async sendMessage(
    conversationId: string,
    request: ChatRequest,
    signal?: AbortSignal,
  ): Promise<ChatAcceptedResponse> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/chat`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify(request),
      signal,
    });
    if (!response.ok) {
      throw await responseError(response, "Failed to send message");
    }
    const body = await response.text();
    return body ? (JSON.parse(body) as ChatAcceptedResponse) : {};
  }

  // Exchanges a browser WebRTC offer for a GPT-Live session. The server holds
  // the API key; the browser then talks to OpenAI directly.
  async createLiveSession(
    conversationId: string,
    sdp: string,
    signal: AbortSignal,
  ): Promise<{ session: { id: string }; transport: { sdp: string } }> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/live-session`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({ sdp }),
      signal,
    });
    if (!response.ok) throw await responseError(response, "Failed to start live session");
    return response.json();
  }

  // Only a Live delegation calls this endpoint. The server's small model
  // turns that delegation's voice context into a Shelley task.
  async sendLiveMessage(
    conversationId: string,
    id: string,
    transcript: string,
    voiceContext: string,
    offsetMs: number,
  ): Promise<{ status: "accepted" | "queued"; message: string; task: string }> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/live-message`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({ id, transcript, voice_context: voiceContext, offset_ms: offsetMs }),
    });
    if (!response.ok) throw await responseError(response, "Failed to deliver speech to Shelley");
    return response.json();
  }

  async listBtwReaders(conversationId: string): Promise<BtwReaderDescriptor[]> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/btw`);
    if (!response.ok) throw await responseError(response, "Failed to load BTW readers");
    const data = (await response.json()) as { readers?: BtwReaderDescriptor[] };
    return data.readers ?? [];
  }

  async dismissBtwExchange(conversationId: string, exchangeId: string): Promise<void> {
    const response = await fetch(
      `${this.baseUrl}/conversation/${conversationId}/btw/${exchangeId}/dismiss`,
      { method: "POST", headers: this.postHeaders },
    );
    if (!response.ok) throw await responseError(response, "Failed to dismiss BTW");
  }

  async summarizeBtwExchange(
    conversationId: string,
    exchangeId: string,
  ): Promise<BtwSummaryReceipt> {
    const response = await fetch(
      `${this.baseUrl}/conversation/${conversationId}/btw/${exchangeId}/summarize`,
      { method: "POST", headers: this.postHeaders },
    );
    if (!response.ok) throw await responseError(response, "Failed to summarize BTW");
    return response.json();
  }

  // createStream opens the unified SSE stream. It delivers per-conversation
  // events (messages, conversation, conversation_state, tool_progress,
  // stream_delta, context_window_size) for ALL active conversations on a
  // single connection, plus server-wide events (conversation_list_patch,
  // notification_event, heartbeat). Each per-conversation event carries a
  // top-level conversation_id field for routing.
  createStream(opts: { conversationListHash?: string } = {}): EventSource {
    const params = new URLSearchParams();
    if (opts.conversationListHash) {
      params.set("conversation_list_hash", opts.conversationListHash);
    }
    const query = params.toString();
    return new EventSource(`${this.baseUrl}/stream2${query ? `?${query}` : ""}`);
  }

  // forkConversation creates a new conversation that copies all messages from
  // the source up to and including the given message (or sequence_id), and
  // returns the new conversation so the caller can navigate to it. With no
  // cutoff, the whole conversation is forked.
  async forkConversation(
    conversationId: string,
    opts: { messageId?: string; sequenceId?: number } = {},
  ): Promise<Conversation> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/fork`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({
        message_id: opts.messageId,
        sequence_id: opts.sequenceId,
      }),
    });
    if (!response.ok) {
      throw await responseError(response, "Failed to fork conversation");
    }
    return response.json();
  }

  async resumeConversation(conversationId: string): Promise<"resuming" | "not_applicable"> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/resume`, {
      method: "POST",
    });
    if (!response.ok) {
      let detail = "";
      try {
        detail = (await response.text()).trim();
      } catch {
        // ignore
      }
      throw new Error(detail || `Failed to resume conversation: ${response.statusText}`);
    }
    const body = (await response.json()) as { status?: string };
    return body.status === "resuming" ? "resuming" : "not_applicable";
  }

  async retryConversation(conversationId: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/retry`, {
      method: "POST",
    });
    if (!response.ok) {
      let detail = "";
      try {
        detail = (await response.text()).trim();
      } catch {
        // ignore
      }
      throw new Error(detail || `Failed to retry conversation: ${response.statusText}`);
    }
  }

  // Switch models and re-fire the request the previous model declined.
  async continueConversation(conversationId: string, model?: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/continue`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ model: model || "" }),
    });
    if (!response.ok) {
      let detail = "";
      try {
        detail = (await response.text()).trim();
      } catch {
        // ignore
      }
      throw new Error(detail || `Failed to continue conversation: ${response.statusText}`);
    }
  }

  async cancelConversation(conversationId: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/cancel`, {
      method: "POST",
    });
    if (!response.ok) {
      throw await responseError(response, "Failed to cancel conversation");
    }
  }

  async cancelQueuedMessages(conversationId: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/cancel-queued`, {
      method: "POST",
    });
    if (!response.ok) {
      throw await responseError(response, "Failed to cancel queued messages");
    }
  }

  async sendQueuedMessageNow(conversationId: string, queuedId: string): Promise<void> {
    const response = await fetch(
      `${this.baseUrl}/conversation/${conversationId}/send-queued?queued_id=${encodeURIComponent(queuedId)}`,
      { method: "POST" },
    );
    if (!response.ok) {
      throw await responseError(response, "Failed to send queued message now");
    }
  }

  // Cancel a single queued message by its QueuedMessage id.
  async cancelQueuedMessage(conversationId: string, queuedId: string): Promise<void> {
    const response = await fetch(
      `${this.baseUrl}/conversation/${conversationId}/cancel-queued?queued_id=${encodeURIComponent(queuedId)}`,
      { method: "POST" },
    );
    if (!response.ok) {
      throw await responseError(response, "Failed to cancel queued message");
    }
  }

  // Retry a failed durable queued item in place.
  async retryQueuedMessage(conversationId: string, queuedId: string): Promise<void> {
    const response = await fetch(
      `${this.baseUrl}/conversation/${conversationId}/retry-queued?queued_id=${encodeURIComponent(queuedId)}`,
      { method: "POST" },
    );
    if (!response.ok) {
      throw await responseError(response, "Failed to retry queued message");
    }
  }

  async validateCwd(path: string): Promise<{ valid: boolean; error?: string }> {
    const response = await fetch(`${this.baseUrl}/validate-cwd?path=${encodeURIComponent(path)}`);
    if (!response.ok) {
      throw await responseError(response, "Failed to validate cwd");
    }
    return response.json();
  }

  async listDirectory(path?: string): Promise<{
    path: string;
    parent: string;
    entries: Array<{ name: string; is_dir: boolean; git_head_subject?: string }>;
    git_head_subject?: string;
    /** Toplevel of the worktree containing `path` (if any). For a linked
     *  worktree, this is the worktree's own root, not the main repo. */
    git_repo_root?: string;
    /** Main repository root, set only when `git_repo_root` is a linked
     *  worktree (i.e. different from the main repo). */
    git_worktree_root?: string;
    error?: string;
  }> {
    const url = path
      ? `${this.baseUrl}/list-directory?path=${encodeURIComponent(path)}`
      : `${this.baseUrl}/list-directory`;
    const response = await fetch(url);
    if (!response.ok) {
      throw await responseError(response, "Failed to list directory");
    }
    return response.json();
  }

  async createDirectory(path: string): Promise<{ path?: string; error?: string }> {
    const response = await fetch(`${this.baseUrl}/create-directory`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({ path }),
    });
    if (!response.ok) {
      throw await responseError(response, "Failed to create directory");
    }
    return response.json();
  }

  async getArchivedConversations(): Promise<ConversationWithParticipants[]> {
    const response = await fetch(`${this.baseUrl}/conversations/archived`);
    if (!response.ok) {
      throw await responseError(response, "Failed to get archived conversations");
    }
    return response.json();
  }

  async archiveConversation(conversationId: string): Promise<Conversation> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/archive`, {
      method: "POST",
    });
    if (!response.ok) {
      throw await responseError(response, "Failed to archive conversation");
    }
    return response.json();
  }

  async unarchiveConversation(conversationId: string): Promise<Conversation> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/unarchive`, {
      method: "POST",
    });
    if (!response.ok) {
      throw await responseError(response, "Failed to unarchive conversation");
    }
    return response.json();
  }

  async deleteConversation(conversationId: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/delete`, {
      method: "POST",
    });
    if (!response.ok) {
      throw await responseError(response, "Failed to delete conversation");
    }
  }

  async getConversationBySlug(slug: string): Promise<Conversation | null> {
    const response = await fetch(
      `${this.baseUrl}/conversation-by-slug/${encodeURIComponent(slug)}`,
    );
    if (response.status === 404) {
      return null;
    }
    if (!response.ok) {
      throw await responseError(response, "Failed to get conversation by slug");
    }
    return response.json();
  }

  // Git diff APIs
  async getGitDiffs(
    cwd: string,
    commit?: string,
  ): Promise<{ diffs: GitDiffInfo[]; gitRoot: string }> {
    const params = new URLSearchParams({ cwd });
    if (commit) params.set("commit", commit);
    const response = await fetch(`${this.baseUrl}/git/diffs?${params}`);
    if (!response.ok) {
      const text = await response.text();
      throw new Error(text || response.statusText);
    }
    return response.json();
  }

  async getGitTourStatus(cwd: string, hash: string): Promise<GitTourBuildStatus> {
    const params = new URLSearchParams({ cwd, hash });
    const response = await fetch(`${this.baseUrl}/git/tour/status?${params}`);
    if (!response.ok) {
      throw await responseError(response, "Failed to check commit tour status");
    }
    return response.json();
  }

  async requestGitTour(
    conversationId: string,
    cwd: string,
    hash: string,
  ): Promise<GitTourBuildStatus> {
    const accepted = await this.sendMessage(conversationId, {
      message: `/tour ${hash}\n${cwd}`,
    });
    if (!accepted.tour) throw new Error("Commit tour request returned no status");
    return accepted.tour;
  }

  gitTourMediaURL(cwd: string, blob: string): string {
    return `${this.baseUrl}/git/tour/media?${new URLSearchParams({ cwd, blob })}`;
  }

  async hasGitTour(cwd: string, hash: string): Promise<boolean> {
    const params = new URLSearchParams({ cwd, hash });
    const response = await fetch(`${this.baseUrl}/git/tour?${params}`, { method: "HEAD" });
    if (response.status === 404) return false;
    if (!response.ok) {
      throw await responseError(response, "Failed to check commit tour");
    }
    return true;
  }

  async getGitTour(cwd: string, hash: string): Promise<GitTourResponse> {
    const params = new URLSearchParams({ cwd, hash });
    const response = await fetch(`${this.baseUrl}/git/tour?${params}`);
    if (!response.ok) {
      throw await responseError(response, "Failed to get commit tour");
    }
    return response.json();
  }

  async getGitGraph(
    cwd: string,
    limit = 500,
    scope: "all" | "current" = "all",
  ): Promise<import("../types").GitGraphResponse> {
    const response = await fetch(
      `${this.baseUrl}/git/graph?cwd=${encodeURIComponent(cwd)}&limit=${limit}&scope=${scope}`,
    );
    if (!response.ok) {
      const text = await response.text();
      throw new Error(text || response.statusText);
    }
    return response.json();
  }

  async getGitCommitDetail(cwd: string, hash: string): Promise<import("../types").GitCommitDetail> {
    const response = await fetch(
      `${this.baseUrl}/git/commit-detail?cwd=${encodeURIComponent(cwd)}&hash=${encodeURIComponent(hash)}`,
    );
    if (!response.ok) {
      const text = await response.text();
      throw new Error(text || response.statusText);
    }
    return response.json();
  }

  async getGitDiffFiles(diffId: string, cwd: string, to?: string): Promise<GitFileInfo[]> {
    const toParam = to ? `&to=${encodeURIComponent(to)}` : "";
    const response = await fetch(
      `${this.baseUrl}/git/diffs/${diffId}/files?cwd=${encodeURIComponent(cwd)}${toParam}`,
    );
    if (!response.ok) {
      throw await responseError(response, "Failed to get diff files");
    }
    return response.json();
  }

  // `oldPath` names the left-hand file when the commit renamed it; without it
  // a renamed file comes back with empty old content.
  async getGitFileDiff(
    diffId: string,
    filePath: string,
    cwd: string,
    to?: string,
    oldPath?: string,
  ): Promise<GitFileDiff> {
    const toParam = to ? `&to=${encodeURIComponent(to)}` : "";
    const oldPathParam = oldPath ? `&oldPath=${encodeURIComponent(oldPath)}` : "";
    // Encode per segment so names containing '#', '?' or '%' survive the URL.
    const encodedPath = filePath.split("/").map(encodeURIComponent).join("/");
    const response = await fetch(
      `${this.baseUrl}/git/file-diff/${diffId}/${encodedPath}?cwd=${encodeURIComponent(cwd)}${toParam}${oldPathParam}`,
    );
    if (!response.ok) {
      throw await responseError(response, "Failed to get file diff");
    }
    return response.json();
  }

  async getGitCommitMessages(
    cwd: string,
    from: string,
    to?: string,
  ): Promise<{ hash: string; subject: string; body: string; author: string; isHead: boolean }[]> {
    const toParam = to ? `&to=${encodeURIComponent(to)}` : "";
    const response = await fetch(
      `${this.baseUrl}/git/commit-messages?cwd=${encodeURIComponent(cwd)}&from=${encodeURIComponent(from)}${toParam}`,
    );
    if (!response.ok) {
      throw await responseError(response, "Failed to get commit messages");
    }
    return response.json();
  }

  async amendGitMessage(cwd: string, message: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/git/amend-message`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({ cwd, message }),
    });
    if (!response.ok) {
      const text = await response.text();
      throw new Error(text || `Failed to amend: ${response.statusText}`);
    }
  }

  async createGitWorktree(cwd: string): Promise<{ path?: string; error?: string }> {
    const response = await fetch(`${this.baseUrl}/git/create-worktree`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({ cwd }),
    });
    if (!response.ok) {
      const data = await response.json().catch(() => ({}));
      throw new Error(data.error || `Failed to create worktree: ${response.statusText}`);
    }
    return response.json();
  }

  // findFiles fuzzy-searches files under `dir` server-side. `query` is the
  // fuzzy pattern (empty returns the first files alphabetically); whitespace
  // splits it into terms that are ANDed, so "vm storage s3" matches
  // vm-storage-s3-design.md. A query that announces itself as a path (a
  // leading /, ~, ./ or ../) re-roots the search at the directory it names:
  // `search_dir` is then the directory matches are relative to, and
  // `match_query` the part of the query matched within it. Content search is
  // a second phase: pass `opts.content` "skip" for the fast name-only pass
  // (no snippets), then "only" for git-grep hits alone — each match then
  // carries `line`/`snippet`/`snippet_matched_indexes` and no path highlights
  // — so name matches render immediately while grep catches up. `signal` lets
  // callers abort superseded requests while the user types. `includeDirs` adds
  // folders to name results (is_dir=true, with a trailing slash in path);
  // file-only callers such as the editor's finder keep the existing default.
  async findFiles(
    dir: string,
    query: string,
    signal?: AbortSignal,
    opts?: { content?: "skip" | "only"; includeDirs?: boolean },
  ): Promise<{
    dir: string;
    search_dir: string;
    query: string;
    match_query: string;
    matches: Array<{
      path: string;
      is_dir?: boolean;
      matched_indexes?: number[];
      line?: number;
      snippet?: string;
      snippet_matched_indexes?: number[];
    }>;
    total: number;
    truncated: boolean;
  }> {
    const params = new URLSearchParams({ dir });
    if (query) params.set("q", query);
    if (opts?.content) params.set("content", opts.content);
    if (opts?.includeDirs) params.set("include_dirs", "true");
    const response = await fetch(`${this.baseUrl}/find-files?${params.toString()}`, { signal });
    if (!response.ok) {
      throw await responseError(response, "Failed to find files");
    }
    return response.json();
  }

  async renameConversation(conversationId: string, slug: string): Promise<Conversation> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/rename`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({ slug }),
    });
    if (!response.ok) {
      const text = await response.text();
      throw new Error(text.trim() || `Failed to rename conversation: ${response.statusText}`);
    }
    return response.json();
  }

  // Moves an existing conversation to a different working directory: the
  // user-driven counterpart of the agent's change_dir tool. The server
  // validates the path, updates the live toolset, and tells the agent, so this
  // is deliberately not a draft-style local update. Rejections are meaningful
  // (a missing directory, or a turn in flight), so the message is surfaced.
  async setConversationCwd(conversationId: string, cwd: string): Promise<Conversation> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/cwd`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({ cwd }),
    });
    if (!response.ok) {
      const text = await response.text();
      throw new Error(text.trim() || `Failed to change directory: ${response.statusText}`);
    }
    return response.json();
  }

  // Turns the compact_in_place tool on in a conversation started without it
  // (the Compact in Place button). Refused mid-turn; the message says so.
  async enableCompactInPlace(conversationId: string): Promise<Conversation> {
    const response = await fetch(
      `${this.baseUrl}/conversation/${conversationId}/enable-compact-in-place`,
      { method: "POST", headers: this.postHeaders },
    );
    if (!response.ok) {
      const text = await response.text();
      throw new Error(
        text.trim() || `Failed to enable compacting in place: ${response.statusText}`,
      );
    }
    return response.json();
  }

  // Changes an existing conversation's settings (see SettingsChange). The
  // server records a marker and broadcasts the updated conversation.
  async updateConversationSettings(
    conversationId: string,
    change: SettingsChange,
  ): Promise<ConversationSettings> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/settings`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify(change),
    });
    if (!response.ok) throw await responseError(response, "Failed to change settings");
    return response.json();
  }

  async updateConversationTags(conversationId: string, tags: string[]): Promise<Conversation> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/tags`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({ tags }),
    });
    if (!response.ok) {
      throw await responseError(response, "Failed to update tags");
    }
    return response.json();
  }

  async getBackgroundJobs(conversationId: string): Promise<BackgroundJob[]> {
    const response = await fetch(
      `${this.baseUrl}/conversation/${encodeURIComponent(conversationId)}/background-jobs`,
    );
    if (!response.ok) {
      throw await responseError(response, "Failed to get background jobs");
    }
    return response.json();
  }

  async killBackgroundJob(conversationId: string, jobId: string): Promise<void> {
    const response = await fetch(
      `${this.baseUrl}/conversation/${encodeURIComponent(conversationId)}/background-jobs/${encodeURIComponent(jobId)}/kill`,
      { method: "POST" },
    );
    if (!response.ok) {
      throw await responseError(response, "Failed to kill background job");
    }
  }

  async getSubagents(conversationId: string): Promise<Conversation[]> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/subagents`);
    if (!response.ok) {
      throw await responseError(response, "Failed to get subagents");
    }
    return response.json();
  }

  // Version check APIs
  async checkVersion(forceRefresh = false): Promise<VersionInfo> {
    const url = forceRefresh ? "/version-check?refresh=true" : "/version-check";
    const response = await fetch(url);
    if (!response.ok) {
      throw await responseError(response, "Failed to check version");
    }
    return response.json();
  }

  async getChangelog(currentTag: string, latestTag: string): Promise<CommitInfo[]> {
    const params = new URLSearchParams({ current: currentTag, latest: latestTag });
    const response = await fetch(`/version-changelog?${params}`);
    if (!response.ok) {
      throw await responseError(response, "Failed to get changelog");
    }
    return response.json();
  }

  async upgrade(restart: boolean = false): Promise<{ status: string; message: string }> {
    const url = restart ? "/upgrade?restart=true" : "/upgrade";
    const response = await fetch(url, {
      method: "POST",
      headers: { "X-Shelley-Request": "1" },
    });
    if (!response.ok) {
      const text = await response.text();
      throw new Error(text || response.statusText);
    }
    return response.json();
  }

  async upgradeHeadlessShell(): Promise<{ status: string; message: string; version: string }> {
    const response = await fetch("/upgrade-headless-shell", {
      method: "POST",
      headers: { "X-Shelley-Request": "1" },
    });
    if (!response.ok) {
      const text = await response.text();
      throw new Error(text || response.statusText);
    }
    return response.json();
  }

  async exit(): Promise<{ status: string; message: string }> {
    const response = await fetch("/exit", {
      method: "POST",
    });
    if (!response.ok) {
      throw await responseError(response, "Failed to exit");
    }
    return response.json();
  }

  async getSettings(): Promise<Record<string, string>> {
    const response = await fetch("/settings");
    if (!response.ok) {
      throw await responseError(response, "Failed to get settings");
    }
    return response.json();
  }

  async setSetting(key: string, value: string): Promise<{ status: string }> {
    const response = await fetch("/settings", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-Shelley-Request": "1",
      },
      body: JSON.stringify({ key, value }),
    });
    if (!response.ok) {
      const text = await response.text();
      throw new Error(text || response.statusText);
    }
    return response.json();
  }

  async dismissDiskSpaceNotice(episodeId: number): Promise<DiskSpaceStatus> {
    const response = await fetch("/api/disk-space/dismiss", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-Shelley-Request": "1",
      },
      body: JSON.stringify({ episode_id: episodeId }),
    });
    if (!response.ok) {
      const text = await response.text();
      throw new Error(text || response.statusText);
    }
    return response.json();
  }
}

export const api = new ApiService();

// Feature flags API. Flags are declared in Go (package featureflags); the
// server returns the merged registry+override list. `override === undefined`
// means "no override stored"; deleting an override reverts to `default`.
export interface FeatureFlag {
  name: string;
  description: string;
  default: unknown;
  override?: unknown;
}

export const featureFlagsApi = {
  async list(): Promise<FeatureFlag[]> {
    const r = await fetch("/feature-flags");
    if (!r.ok) throw await responseError(r, "Failed to load feature flags");
    return r.json();
  },
  async set(name: string, value: unknown): Promise<void> {
    const r = await fetch("/feature-flags", {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Shelley-Request": "1" },
      body: JSON.stringify({ name, value }),
    });
    if (!r.ok) throw new Error((await r.text()) || r.statusText);
  },
  async clear(name: string): Promise<void> {
    const r = await fetch("/feature-flags", {
      method: "DELETE",
      headers: { "Content-Type": "application/json", "X-Shelley-Request": "1" },
      body: JSON.stringify({ name }),
    });
    if (!r.ok) throw new Error((await r.text()) || r.statusText);
  },
};

// The favicon emoji comes from exe.dev when the VM has one; otherwise it is
// Shelley's own stored emoji, which set() changes. href is the favicon data URI.
export interface FaviconEmoji {
  emoji: string;
  source: "exe.dev" | "shelley";
  href: string;
}

export const faviconEmojiApi = {
  async get(): Promise<FaviconEmoji> {
    const r = await fetch("/api/favicon-emoji");
    if (!r.ok) throw await responseError(r, "Failed to load favicon emoji");
    return r.json();
  },
  async set(emoji: string): Promise<FaviconEmoji> {
    const r = await fetch("/api/favicon-emoji", {
      method: "PUT",
      headers: { "Content-Type": "application/json", "X-Shelley-Request": "1" },
      body: JSON.stringify({ emoji }),
    });
    if (!r.ok) throw await responseError(r, "Failed to save favicon emoji");
    return r.json();
  },
};

// models.dev pricing, USD per million tokens. null = model known but unpriced.
export interface ModelCostDTO {
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
}

// Module-level cache so reopening the context popup doesn't refetch.
// Models whose fetch failed stay uncached and are retried next call.
const modelCostCache = new Map<string, ModelCostDTO | null>();

export const modelCostsApi = {
  async lookup(
    models: { model: string; url: string }[],
  ): Promise<Record<string, ModelCostDTO | null>> {
    const missing = models.filter((m) => !modelCostCache.has(m.model));
    if (missing.length > 0) {
      const r = await fetch("/api/model-costs", {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-Shelley-Request": "1" },
        body: JSON.stringify({ models: missing }),
      });
      if (!r.ok) throw await responseError(r, "Failed to load model costs");
      const data = (await r.json()) as { costs: Record<string, ModelCostDTO | null> };
      for (const m of missing) modelCostCache.set(m.model, data.costs[m.model] ?? null);
    }
    const out: Record<string, ModelCostDTO | null> = {};
    for (const m of models) out[m.model] = modelCostCache.get(m.model) ?? null;
    return out;
  },
};

// Descendant direct and indirect usage grouped by (model, endpoint).
export interface SubagentModelUsageDTO {
  model: string;
  url: string;
  llm_calls: number;
  input_tokens: number;
  cache_creation_input_tokens: number;
  cache_read_input_tokens: number;
  output_tokens: number;
  estimated_usd: number;
  reported_usd: number;
  cost: ModelCostDTO | null;
}

// Aggregated LLM usage across a conversation's subagents (recursive).
export interface SubagentUsageDTO {
  per_model: SubagentModelUsageDTO[];
  llm_calls: number;
  estimated_usd: number;
  reported_usd: number;
  unpriced_reported_usd: number;
  unpriced_models: string[];
  unpriced_calls: number;
}

export const subagentUsageApi = {
  async get(conversationId: string, signal?: AbortSignal): Promise<SubagentUsageDTO> {
    const r = await fetch(`/api/conversation/${conversationId}/subagent-usage`, {
      headers: { "X-Shelley-Request": "1" },
      signal,
    });
    if (!r.ok) throw await responseError(r, "Failed to load subagent usage");
    return (await r.json()) as SubagentUsageDTO;
  },
};

// Custom models API
export interface CustomModel {
  model_id: string;
  display_name: string;
  provider_type: "anthropic" | "openai" | "openai-responses" | "gemini";
  endpoint: string;
  api_key: string;
  model_name: string;
  max_tokens: number;
  tags: string; // Comma-separated tags (e.g., "slug" for slug generation)
  reasoning_effort: string; // Legacy provider-verbatim default
  reasoning_replay: "auto" | "none" | "reasoning_content";
  resolved_reasoning_replay?: "none" | "reasoning_content";
  reasoning_support: "auto" | "yes" | "no";
  reasoning_map: string;
  supports_reasoning: boolean;
  image_support: "auto" | "yes" | "no";
  supports_images: boolean; // Resolved boolean that image_support evaluates to
}

export interface CreateCustomModelRequest {
  display_name: string;
  provider_type: "anthropic" | "openai" | "openai-responses" | "gemini";
  endpoint: string;
  api_key: string;
  model_name: string;
  max_tokens: number;
  tags: string; // Comma-separated tags
  reasoning_effort: string; // Legacy provider-verbatim default
  reasoning_replay: "auto" | "none" | "reasoning_content";
  reasoning_support: "auto" | "yes" | "no";
  reasoning_map: string;
  image_support: "auto" | "yes" | "no";
}

export interface TestCustomModelRequest {
  model_id?: string; // If provided with empty api_key, use stored key
  provider_type: "anthropic" | "openai" | "openai-responses" | "gemini";
  endpoint: string;
  api_key: string;
  model_name: string;
  max_tokens?: number;
  reasoning_effort?: string;
  reasoning_replay?: "auto" | "none" | "reasoning_content";
  reasoning_support?: "auto" | "yes" | "no";
  reasoning_map?: string;
}

class CustomModelsApi {
  private baseUrl = "/api";

  private postHeaders = {
    "Content-Type": "application/json",
  };

  async getCustomModels(): Promise<CustomModel[]> {
    const response = await fetch(`${this.baseUrl}/custom-models`);
    if (!response.ok) {
      throw await responseError(response, "Failed to get custom models");
    }
    return response.json();
  }

  async createCustomModel(request: CreateCustomModelRequest): Promise<CustomModel> {
    const response = await fetch(`${this.baseUrl}/custom-models`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify(request),
    });
    if (!response.ok) {
      throw await responseError(response, "Failed to create custom model");
    }
    return response.json();
  }

  async updateCustomModel(
    modelId: string,
    request: Partial<CreateCustomModelRequest>,
  ): Promise<CustomModel> {
    const response = await fetch(`${this.baseUrl}/custom-models/${modelId}`, {
      method: "PUT",
      headers: this.postHeaders,
      body: JSON.stringify(request),
    });
    if (!response.ok) {
      throw await responseError(response, "Failed to update custom model");
    }
    return response.json();
  }

  async deleteCustomModel(modelId: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/custom-models/${modelId}`, {
      method: "DELETE",
    });
    if (!response.ok) {
      throw await responseError(response, "Failed to delete custom model");
    }
  }

  async duplicateCustomModel(modelId: string, displayName?: string): Promise<CustomModel> {
    const response = await fetch(`${this.baseUrl}/custom-models/${modelId}/duplicate`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({ display_name: displayName }),
    });
    if (!response.ok) {
      throw await responseError(response, "Failed to duplicate custom model");
    }
    return response.json();
  }

  async testCustomModel(
    request: TestCustomModelRequest,
  ): Promise<{ success: boolean; message: string }> {
    const response = await fetch(`${this.baseUrl}/custom-models-test`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify(request),
    });
    if (!response.ok) {
      throw await responseError(response, "Failed to test custom model");
    }
    return response.json();
  }
}

export const customModelsApi = new CustomModelsApi();

export interface McpServer {
  name: string;
  url: string;
  description: string;
  headers: Record<string, string>;
  auth?: "" | "logged_in" | "login_required";
  login_url?: string;
}

// The parts of a tool's input JSON Schema that ToolDescriptionCard shows.
export interface ToolSchema {
  properties?: Record<string, { type?: string | string[]; description?: string; enum?: unknown[] }>;
  required?: string[];
}

export interface McpTool {
  name: string;
  description?: string;
  inputSchema: ToolSchema;
}

async function mcpFetch(path: string, failure: string, init?: RequestInit): Promise<Response> {
  const response = await fetch(`/api/mcp/servers${path}`, init);
  if (!response.ok) throw await responseError(response, failure);
  return response;
}

const mcpPath = (name: string) => `/${encodeURIComponent(name)}`;

export const mcpServersApi = {
  async list(): Promise<McpServer[]> {
    return (await mcpFetch("", "Failed to load MCP servers")).json();
  },
  async save(server: McpServer, isNew: boolean): Promise<void> {
    await mcpFetch(isNew ? "" : mcpPath(server.name), "Failed to save MCP server", {
      method: isNew ? "POST" : "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(server),
    });
  },
  async remove(name: string): Promise<void> {
    await mcpFetch(mcpPath(name), "Failed to delete MCP server", { method: "DELETE" });
  },
  async logout(name: string): Promise<void> {
    await mcpFetch(`${mcpPath(name)}/logout`, "Failed to log out", { method: "POST" });
  },
  async tools(name: string, signal: AbortSignal): Promise<McpTool[]> {
    const response = await mcpFetch(`${mcpPath(name)}/tools`, "Failed to list tools", { signal });
    return ((await response.json()) as { tools: McpTool[] }).tools;
  },
};

async function profilesFetch(path: string, failure: string, init?: RequestInit) {
  const response = await fetch(`/api${path}`, init);
  if (!response.ok) throw await responseError(response, failure);
  return response;
}

export const profilesApi = {
  async list(): Promise<Profile[]> {
    return (await profilesFetch("/profiles", "Failed to load profiles")).json();
  },
  async create(name: string, settings: Settings): Promise<Profile> {
    const response = await profilesFetch("/profiles", "Failed to save profile", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, ...settings }),
    });
    return response.json();
  },
  async update(name: string, settings: Settings): Promise<Profile> {
    const response = await profilesFetch(
      `/profiles/${encodeURIComponent(name)}`,
      "Failed to save profile",
      {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(settings),
      },
    );
    return response.json();
  },
  async makeDefault(name: string): Promise<Profile> {
    const response = await profilesFetch(
      `/profiles/${encodeURIComponent(name)}/default`,
      "Failed to change the default profile",
      { method: "POST" },
    );
    return response.json();
  },
  async remove(name: string): Promise<void> {
    await profilesFetch(`/profiles/${encodeURIComponent(name)}`, "Failed to delete profile", {
      method: "DELETE",
    });
  },
  async systemPrompt(): Promise<SystemPromptInfo> {
    return (await profilesFetch("/system-prompt", "Failed to load the system prompt")).json();
  },
  /** Why template can't be rendered, or null if it can. */
  async checkSystemPrompt(template: string, signal: AbortSignal): Promise<TemplateProblem | null> {
    const response = await profilesFetch("/system-prompt/check", "Failed to check the template", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ template }),
      signal,
    });
    return ((await response.json()) as { error: TemplateProblem | null }).error;
  },
};

// Notification channels API
export interface NotificationChannelAPI {
  channel_id: string;
  channel_type: string;
  display_name: string;
  enabled: boolean;
  config: Record<string, string>;
}

export interface CreateNotificationChannelRequest {
  channel_type: string;
  display_name: string;
  enabled: boolean;
  config: Record<string, string>;
}

export interface UpdateNotificationChannelRequest {
  display_name: string;
  enabled: boolean;
  config: Record<string, string>;
}

export interface ChannelTypeInfo {
  type: string;
  label: string;
  config_fields: {
    name: string;
    label: string;
    type: string;
    required: boolean;
    placeholder?: string;
    default?: string;
    description?: string;
    options?: string[];
  }[];
}

class NotificationChannelsApi {
  private baseUrl = "/api";

  private postHeaders = {
    "Content-Type": "application/json",
  };

  private async throwIfNotOk(response: Response, fallback: string): Promise<void> {
    if (response.ok) return;
    const body = await response.text().catch(() => "");
    throw new Error(body.trim() || `${fallback}: ${response.statusText}`);
  }

  async getChannels(): Promise<NotificationChannelAPI[]> {
    const response = await fetch(`${this.baseUrl}/notification-channels`);
    await this.throwIfNotOk(response, "Failed to get notification channels");
    return response.json();
  }

  async createChannel(request: CreateNotificationChannelRequest): Promise<NotificationChannelAPI> {
    const response = await fetch(`${this.baseUrl}/notification-channels`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify(request),
    });
    await this.throwIfNotOk(response, "Failed to create notification channel");
    return response.json();
  }

  async updateChannel(
    channelId: string,
    request: UpdateNotificationChannelRequest,
  ): Promise<NotificationChannelAPI> {
    const response = await fetch(`${this.baseUrl}/notification-channels/${channelId}`, {
      method: "PUT",
      headers: this.postHeaders,
      body: JSON.stringify(request),
    });
    await this.throwIfNotOk(response, "Failed to update notification channel");
    return response.json();
  }

  async deleteChannel(channelId: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/notification-channels/${channelId}`, {
      method: "DELETE",
    });
    await this.throwIfNotOk(response, "Failed to delete notification channel");
  }

  async testChannel(channelId: string): Promise<{ success: boolean; message: string }> {
    const response = await fetch(`${this.baseUrl}/notification-channels/${channelId}/test`, {
      method: "POST",
      headers: this.postHeaders,
    });
    await this.throwIfNotOk(response, "Failed to test notification channel");
    return response.json();
  }
}

export const notificationChannelsApi = new NotificationChannelsApi();
