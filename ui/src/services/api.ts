import {
  Conversation,
  ConversationRead,
  ConversationWithParticipants,
  ConversationWithState,
  ResourceKind,
  ResourceStatus,
  StreamResponse,
  ChatRequest,
  BtwReaderDescriptor,
  CommitHighlights,
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
import type { CostTier } from "../utils/tokenCostGraph";
import { ownError, responseDetail } from "./responseDetail";

export type LiveConversationSettings = Pick<ChatRequest, "model" | "cwd" | "conversation_options">;

// A failed request's error, its message "<prefix>: <responseDetail>"; status
// 0 when no response came.
export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
  }
}

export async function responseError(response: Response, prefix: string): Promise<ApiError> {
  return new ApiError(`${prefix}: ${await responseDetail(response)}`, response.status);
}

/** Fetches path, throwing an ApiError "<failure>: <reason>" unless the
 *  response is OK; status 0 when no response came. Aborts throw as they are. */
export async function apiFetch(
  path: string,
  failure: string,
  init: RequestInit = {},
): Promise<Response> {
  const response = await fetch(path, {
    ...init,
    headers: { "X-Shelley-Request": "1", ...(init.headers as Record<string, string>) },
  }).catch((err: unknown) => {
    if (init.signal?.aborted) throw err;
    console.warn(`${path}:`, err);
    throw new ApiError(`${failure}: no response`, 0);
  });
  if (!response.ok) throw await responseError(response, failure);
  return response;
}

/** Calls path, sending body as JSON if given, and returns the JSON reply
 *  (undefined for 204 No Content); a failure throws an ApiError
 *  "<failure>: <reason>", status 0 when no response came. */
export async function apiJSON<T>(
  path: string,
  failure: string,
  method = "GET",
  body?: unknown,
): Promise<T> {
  const response = await apiFetch(path, failure, sendJSON(method, body));
  return response.status === 204 ? (undefined as T) : response.json();
}

/** A request sending body, if given, as JSON. */
export function sendJSON(method: string, body?: unknown, signal?: AbortSignal): RequestInit {
  return body === undefined
    ? { method, signal }
    : {
        method,
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
        signal,
      };
}

// A backgrounded bash command that has not exited (server BackgroundJobInfo).
export interface BackgroundJob {
  job_id: string;
  command: string;
  tail: string;
  started_at: string;
}

// What the status-bar hook printed for a conversation (server StatusBar). All
// empty means show nothing.
export interface StatusBarItem {
  body?: string;
  bgcolor?: string;
  color?: string;
  title?: string;
  // Validated by the server; see HOOKS.md for the actions and their args.
  action?: string;
  args?: { commit?: string; url?: string };
}

export interface StatusBar {
  items?: StatusBarItem[];
  bgcolor?: string;
  color?: string;
  error?: string;
}

export interface SkillDescriptor {
  name: string;
  description: string;
  activate: string;
  source_path?: string;
  origin?: string;
  mcp_server?: string;
  mcp_tool?: string;
  parameters?: ToolSchema;
}

// machineParams are query parameters naming the machine a request's paths are
// on: conversationId's (its VM, if it has one), or with none, this host.
export function machineParams(
  conversationId: string | undefined,
  params: Record<string, string>,
): URLSearchParams {
  const p = new URLSearchParams(params);
  if (conversationId) p.set("conversation_id", conversationId);
  return p;
}

// onMachine is url, which has no query, on conversationId's machine.
export function onMachine(url: string, conversationId: string | undefined): string {
  return conversationId ? `${url}?${machineParams(conversationId, {})}` : url;
}

function mcpActivation(name: string): string {
  return `shelley mcp list '${name.replace(/'/g, "'\\''")}' -schema`;
}

function withMcpSelections(request: ChatRequest): ChatRequest {
  const tools = request.skills?.filter((name) => name.startsWith("mcp:")) ?? [];
  if (!tools.length) return request;
  const instructions = tools.map((name) => {
    const tool = name.slice(4);
    return `Use the MCP tool ${JSON.stringify(tool)} for this task. First read its documentation and input schema with \`${mcpActivation(tool)}\`, then call it with shelley mcp call and the appropriate arguments.`;
  });
  return {
    ...request,
    message: `${request.message}\n\n${instructions.join("\n")}`,
    skills: request.skills?.filter((name) => !name.startsWith("mcp:")),
  };
}

export interface AvailableModel {
  id: string;
  display_name?: string;
  source?: string;
  source_id?: string;
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

export interface ChatAcceptedResponse {
  status?: string;
  btw?: BtwReaderDescriptor;
}

export interface BtwSummaryReceipt {
  status?: string;
  summary_id: string;
  btw: BtwReaderDescriptor;
}

class ApiService {
  async getConversationsSnapshot(): Promise<{
    conversations: ConversationWithState[];
    hash: string;
  }> {
    return apiJSON("/api/conversations/snapshot", "Failed to load conversations");
  }

  async acknowledgeConversationRead(
    conversationId: string,
    readAt: number,
  ): Promise<ConversationRead> {
    return apiJSON(
      `/api/conversation/${conversationId}/read`,
      "Failed to mark conversation as read",
      "POST",
      { read_at: readAt },
    );
  }

  async getModels(): Promise<AvailableModel[]> {
    return apiJSON("/api/models", "Failed to get models");
  }

  async getIntegrations(): Promise<IntegrationsResponse> {
    return apiJSON("/api/integrations", "Failed to load integrations");
  }

  async getIntegrationDetails(name: string, team: boolean): Promise<AttachedIntegration> {
    const params = new URLSearchParams({ details: name, team: String(team) });
    const response = await apiFetch(
      `/api/integrations?${params}`,
      "Failed to load integration details",
    );
    const body: IntegrationsResponse = await response.json();
    return body.integrations[0];
  }

  async sendTestNotification(message: string): Promise<void> {
    await apiFetch(
      "/api/integrations/notify/test",
      "Could not send test notification",
      sendJSON("POST", { message }),
    );
  }

  async sendSlackTest(name: string, team: boolean, message: string): Promise<void> {
    await apiFetch(
      "/api/integrations/slack/test",
      "Could not send Slack test message",
      sendJSON("POST", { name, team, message }),
    );
  }

  async dismissModelUpgrade(from: string, to: string): Promise<void> {
    await apiFetch(
      "/api/model-upgrade/dismiss",
      "Failed to dismiss upgrade",
      sendJSON("POST", { from, to }),
    );
  }

  // Moves the other conversations in the family `from` belongs to onto their
  // upgrades.
  async upgradeAllModels(from: string, to: string): Promise<{ upgraded: number; skipped: number }> {
    return apiJSON(
      "/api/model-upgrade/apply-all",
      "Failed to upgrade other conversations",
      "POST",
      { from, to },
    );
  }

  async dismissUpgradePrompt(): Promise<void> {
    await apiFetch("/api/model-upgrade/prompt-dismissed", "Failed to record the answer", {
      method: "POST",
    });
  }

  async refreshModels(): Promise<AvailableModel[]> {
    return apiJSON("/api/models/refresh", "Failed to refresh models", "POST");
  }

  async getTools(): Promise<{
    tools: Array<{ name: string; summary: string; default_on: boolean }>;
  }> {
    return apiJSON("/api/tools", "Failed to get tools");
  }

  async startSearchChat(
    query: string,
    archived: boolean,
    signal: AbortSignal,
    matches: Array<
      Pick<Conversation, "conversation_id" | "slug"> & {
        search_snippet?: string;
        search_snippet_message_id?: string;
      }
    >,
  ): Promise<{ conversation: ConversationWithState }> {
    return (
      await apiFetch(
        "/api/conversations/search/enhance",
        "Could not start search chat",
        sendJSON(
          "POST",
          {
            q: query,
            archived,
            matches: matches.map((c) => ({
              conversation_id: c.conversation_id,
              slug: c.slug,
              snippet: c.search_snippet ?? "",
              message_id: c.search_snippet_message_id ?? "",
            })),
          },
          signal,
        ),
      )
    ).json();
  }

  // Search active top-level conversations, or only archived ones when requested.
  async searchConversationsFTS(
    query: string,
    signal?: AbortSignal,
    archived = false,
  ): Promise<ConversationWithState[]> {
    const params = new URLSearchParams({ q: query });
    if (archived) params.set("archived", "true");
    return (
      await apiFetch(`/api/conversations/search?${params}`, "Failed to search conversations", {
        signal,
      })
    ).json();
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
    return apiJSON("/api/conversations/draft", "Failed to create draft", "POST", request);
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
    return apiJSON(
      `/api/conversation/${conversationId}/draft`,
      "Failed to update draft",
      "PUT",
      fields,
    );
  }

  async sendMessageWithNewConversation(request: ChatRequest): Promise<{ conversation_id: string }> {
    return apiJSON(
      "/api/conversations/new",
      "Failed to start conversation",
      "POST",
      withMcpSelections(request),
    );
  }

  async startNewGeneration(conversationId: string): Promise<Conversation> {
    return apiJSON(
      `/api/conversation/${conversationId}/new-generation`,
      "Failed to start new generation",
      "POST",
    );
  }

  async getConversationWithProgress(
    conversationId: string,
    onProgress?: (progress: {
      phase: "downloading" | "parsing";
      bytesDownloaded: number;
      bytesTotal?: number;
    }) => void,
  ): Promise<StreamResponse> {
    const response = await apiFetch(
      `/api/conversation/${conversationId}`,
      "Failed to get messages",
    );
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
    return apiJSON(
      `/api/conversation/${conversationId}?last_sequence_id=${sinceSequenceId}`,
      `Failed to get messages since ${sinceSequenceId}`,
    );
  }

  // Saves body in the server's upload directory; resolves to its absolute path.
  async uploadRaw(filename: string, body: Blob): Promise<string> {
    const response = await apiFetch(
      `/api/upload/raw?filename=${encodeURIComponent(filename)}`,
      `Failed to upload ${filename}`,
      {
        method: "POST",
        headers: { "Content-Type": body.type || "application/octet-stream" },
        body,
      },
    );
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
    const response = await apiFetch(
      `/api/conversation/${conversationId}/chat`,
      "Failed to send message",
      sendJSON("POST", withMcpSelections(request), signal),
    );
    const body = await response.text();
    return body ? (JSON.parse(body) as ChatAcceptedResponse) : {};
  }

  // Exchanges a browser WebRTC offer for a GPT-Live session. The server holds
  // the API key; the browser then talks to OpenAI directly.
  async createLiveSession(
    conversationId: string,
    sdp: string,
    signal: AbortSignal,
    settings?: LiveConversationSettings,
  ): Promise<{ session: { id: string }; transport: { sdp: string } }> {
    return (
      await apiFetch(
        `/api/conversation/${conversationId}/live-session`,
        "Failed to start live session",
        sendJSON("POST", { sdp, ...settings }, signal),
      )
    ).json();
  }

  // Live's events for the server's side of a call, which decides what the
  // user's speech becomes and answers on liveCallInstructionsURL.
  async sendLiveEvents(
    conversationId: string,
    sessionId: string,
    events: unknown[],
  ): Promise<void> {
    // Match maxLiveRequestBytes in server/live_message.go, including the JSON envelope.
    const prefix = '{"events":[';
    const suffix = "]}";
    const emptyBytes = prefix.length + suffix.length;
    const encoder = new TextEncoder();
    const url = this.liveCallURL(conversationId, sessionId) + "/events";
    const batch: string[] = [];
    let bytes = emptyBytes;
    const send = async () => {
      await apiFetch(url, "Failed to relay the call to Shelley", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: prefix + batch.join(",") + suffix,
      });
      batch.length = 0;
      bytes = emptyBytes;
    };
    for (const event of events) {
      const json = JSON.stringify(event);
      const size = encoder.encode(json).length;
      if (batch.length && bytes + 1 + size > 128 * 1024) await send();
      bytes += size + (batch.length ? 1 : 0);
      batch.push(json);
    }
    if (batch.length) await send();
  }

  liveCallInstructionsURL(conversationId: string, sessionId: string): string {
    return this.liveCallURL(conversationId, sessionId) + "/stream";
  }

  async endLiveCall(conversationId: string, sessionId: string): Promise<void> {
    await apiFetch(this.liveCallURL(conversationId, sessionId), "Failed to end the call", {
      method: "DELETE",
    });
  }

  private liveCallURL(conversationId: string, sessionId: string): string {
    return `/api/conversation/${encodeURIComponent(conversationId)}/live-session/${encodeURIComponent(sessionId)}`;
  }

  async listBtwReaders(conversationId: string): Promise<BtwReaderDescriptor[]> {
    const response = await apiFetch(
      `/api/conversation/${conversationId}/btw`,
      "Failed to load BTW readers",
    );
    const data = (await response.json()) as { readers?: BtwReaderDescriptor[] };
    return data.readers ?? [];
  }

  async dismissBtwExchange(conversationId: string, exchangeId: string): Promise<void> {
    await apiFetch(
      `/api/conversation/${conversationId}/btw/${exchangeId}/dismiss`,
      "Failed to dismiss BTW",
      { method: "POST" },
    );
  }

  async summarizeBtwExchange(
    conversationId: string,
    exchangeId: string,
  ): Promise<BtwSummaryReceipt> {
    return apiJSON(
      `/api/conversation/${conversationId}/btw/${exchangeId}/summarize`,
      "Failed to summarize BTW",
      "POST",
    );
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
    return new EventSource(`/api/stream2${query ? `?${query}` : ""}`);
  }

  // forkConversation creates a new conversation that copies all messages from
  // the source up to and including the given message (or sequence_id), and
  // returns the new conversation so the caller can navigate to it. With no
  // cutoff, the whole conversation is forked.
  async forkConversation(
    conversationId: string,
    opts: { messageId?: string; sequenceId?: number } = {},
  ): Promise<Conversation> {
    return (
      await apiFetch(
        `/api/conversation/${conversationId}/fork`,
        "Failed to fork conversation",
        sendJSON("POST", {
          message_id: opts.messageId,
          sequence_id: opts.sequenceId,
        }),
      )
    ).json();
  }

  async resumeConversation(conversationId: string): Promise<"resuming" | "not_applicable"> {
    const response = await fetch(`/api/conversation/${conversationId}/resume`, {
      method: "POST",
    });
    if (!response.ok) {
      throw await ownError(response, "Failed to resume conversation");
    }
    const body = (await response.json()) as { status?: string };
    return body.status === "resuming" ? "resuming" : "not_applicable";
  }

  async retryConversation(conversationId: string): Promise<void> {
    const response = await fetch(`/api/conversation/${conversationId}/retry`, {
      method: "POST",
    });
    if (!response.ok) {
      throw await ownError(response, "Failed to retry conversation");
    }
  }

  // Switch models and re-fire the request the previous model declined.
  async continueConversation(conversationId: string, model?: string): Promise<void> {
    const response = await fetch(
      `/api/conversation/${conversationId}/continue`,
      sendJSON("POST", { model: model || "" }),
    );
    if (!response.ok) {
      throw await ownError(response, "Failed to continue conversation");
    }
  }

  async cancelConversation(conversationId: string): Promise<void> {
    await apiFetch(`/api/conversation/${conversationId}/cancel`, "Failed to cancel conversation", {
      method: "POST",
    });
  }

  async cancelQueuedMessages(conversationId: string): Promise<void> {
    await apiFetch(
      `/api/conversation/${conversationId}/cancel-queued`,
      "Failed to cancel queued messages",
      { method: "POST" },
    );
  }

  async sendQueuedMessageNow(conversationId: string, queuedId: string): Promise<void> {
    await apiFetch(
      `/api/conversation/${conversationId}/send-queued?queued_id=${encodeURIComponent(queuedId)}`,
      "Failed to send queued message now",
      { method: "POST" },
    );
  }

  // Cancel a single queued message by its QueuedMessage id.
  async cancelQueuedMessage(conversationId: string, queuedId: string): Promise<void> {
    await apiFetch(
      `/api/conversation/${conversationId}/cancel-queued?queued_id=${encodeURIComponent(queuedId)}`,
      "Failed to cancel queued message",
      { method: "POST" },
    );
  }

  // Retry a failed durable queued item in place.
  async retryQueuedMessage(conversationId: string, queuedId: string): Promise<void> {
    await apiFetch(
      `/api/conversation/${conversationId}/retry-queued?queued_id=${encodeURIComponent(queuedId)}`,
      "Failed to retry queued message",
      { method: "POST" },
    );
  }

  async validateCwd(path: string): Promise<{ valid: boolean; error?: string }> {
    return apiJSON(`/api/validate-cwd?path=${encodeURIComponent(path)}`, "Failed to validate cwd");
  }

  async listDirectory(
    path?: string,
    conversationId?: string,
  ): Promise<{
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
    const params = machineParams(conversationId, path ? { path } : {});
    return apiJSON(`/api/list-directory?${params}`, "Failed to list directory");
  }

  async createDirectory(
    path: string,
    conversationId?: string,
  ): Promise<{ path?: string; error?: string }> {
    return apiJSON(
      onMachine("/api/create-directory", conversationId),
      "Failed to create directory",
      "POST",
      { path },
    );
  }

  async getArchivedConversations(): Promise<ConversationWithParticipants[]> {
    return apiJSON("/api/conversations/archived", "Failed to get archived conversations");
  }

  async archiveConversation(conversationId: string): Promise<Conversation> {
    return apiJSON(
      `/api/conversation/${conversationId}/archive`,
      "Failed to archive conversation",
      "POST",
    );
  }

  async unarchiveConversation(conversationId: string): Promise<Conversation> {
    return apiJSON(
      `/api/conversation/${conversationId}/unarchive`,
      "Failed to unarchive conversation",
      "POST",
    );
  }

  async deleteConversation(conversationId: string): Promise<void> {
    await apiFetch(`/api/conversation/${conversationId}/delete`, "Failed to delete conversation", {
      method: "POST",
    });
  }

  async getConversationBySlug(slug: string): Promise<Conversation | null> {
    const response = await fetch(`/api/conversation-by-slug/${encodeURIComponent(slug)}`);
    if (response.status === 404) {
      return null;
    }
    if (!response.ok) {
      throw await responseError(response, "Failed to get conversation by slug");
    }
    return response.json();
  }

  // Git diff APIs. Each takes the conversation whose machine (this host, or
  // its VM) has cwd; without one, cwd is on this host.
  async getGitDiffs(
    cwd: string,
    conversationId: string | undefined,
    commit?: string,
    includeStats = true,
  ): Promise<{ diffs: GitDiffInfo[]; gitRoot: string }> {
    const params = machineParams(conversationId, { cwd });
    if (commit) params.set("commit", commit);
    if (!includeStats) params.set("stats", "false");
    return apiJSON(`/api/git/diffs?${params}`, "Failed to get diffs");
  }

  // Null when the commit has no highlights, or they no longer match it.
  async getGitHighlights(
    cwd: string,
    conversationId: string | undefined,
    hash: string,
  ): Promise<CommitHighlights | null> {
    const params = machineParams(conversationId, { cwd, hash });
    const response = await fetch(`/api/git/highlights?${params}`);
    if (response.status === 404) return null;
    if (!response.ok) {
      throw await responseError(response, "Failed to get commit highlights");
    }
    return (await response.json()).highlights;
  }

  async getGitGraph(
    cwd: string,
    conversationId: string | undefined,
    limit = 500,
    scope: "all" | "current" = "all",
  ): Promise<import("../types").GitGraphResponse> {
    const params = machineParams(conversationId, { cwd, limit: String(limit), scope });
    return apiJSON(`/api/git/graph?${params}`, "Failed to get git graph");
  }

  async getGitCommitDetail(
    cwd: string,
    conversationId: string | undefined,
    hash: string,
  ): Promise<import("../types").GitCommitDetail> {
    const params = machineParams(conversationId, { cwd, hash });
    return apiJSON(`/api/git/commit-detail?${params}`, "Failed to get commit");
  }

  async getGitDiffFiles(
    diffId: string,
    cwd: string,
    conversationId: string | undefined,
    to?: string,
  ): Promise<GitFileInfo[]> {
    const params = machineParams(conversationId, { cwd });
    if (to) params.set("to", to);
    return apiJSON(`/api/git/diffs/${diffId}/files?${params}`, "Failed to get diff files");
  }

  async getGitFileDiff(
    diffId: string,
    filePath: string,
    cwd: string,
    conversationId: string | undefined,
    to?: string,
  ): Promise<GitFileDiff> {
    const params = machineParams(conversationId, { cwd });
    if (to) params.set("to", to);
    // Encode per segment so names containing '#', '?' or '%' survive the URL.
    const encodedPath = filePath.split("/").map(encodeURIComponent).join("/");
    return apiJSON(
      `/api/git/file-diff/${diffId}/${encodedPath}?${params}`,
      "Failed to get file diff",
    );
  }

  async getGitCommitMessages(
    cwd: string,
    conversationId: string | undefined,
    from: string,
    to?: string,
  ): Promise<{ hash: string; subject: string; body: string; author: string; isHead: boolean }[]> {
    const params = machineParams(conversationId, { cwd, from });
    if (to) params.set("to", to);
    return apiJSON(`/api/git/commit-messages?${params}`, "Failed to get commit messages");
  }

  async amendGitMessage(
    cwd: string,
    conversationId: string | undefined,
    message: string,
  ): Promise<void> {
    await apiFetch(
      onMachine("/api/git/amend-message", conversationId),
      "Failed to amend",
      sendJSON("POST", { cwd, message }),
    );
  }

  async createGitWorktree(cwd: string): Promise<{ path?: string; error?: string }> {
    return apiJSON("/api/git/create-worktree", "Failed to create worktree", "POST", { cwd });
  }

  async getSkills(
    cwd: string,
    conversationId: string | null,
    signal?: AbortSignal,
  ): Promise<{ skills: SkillDescriptor[]; warnings?: string[] }> {
    const params = new URLSearchParams({ cwd });
    if (conversationId) params.set("conversation_id", conversationId);
    const response = await apiFetch(`/api/skills?${params}`, "Failed to load skills", { signal });
    const catalog: { skills: SkillDescriptor[] } = await response.json();
    let servers: McpServer[];
    try {
      servers = await mcpServersApi.list(signal);
    } catch (error) {
      signal?.throwIfAborted();
      return { ...catalog, warnings: [error instanceof Error ? error.message : String(error)] };
    }
    const tools = await Promise.allSettled(
      servers.map(async (server) => {
        const deadline = AbortSignal.timeout(5000);
        const toolSignal = signal ? AbortSignal.any([signal, deadline]) : deadline;
        const available = await mcpServersApi.tools(server.name, toolSignal);
        const valid = available.filter((tool) => /^[A-Za-z0-9_.-]{1,128}$/.test(tool.name));
        return {
          skills: valid.map((tool): SkillDescriptor => {
            const qualifiedName = `${server.name}.${tool.name}`;
            return {
              name: `mcp:${qualifiedName}`,
              mcp_server: server.name,
              mcp_tool: tool.name,
              description: tool.description || `Call ${qualifiedName}.`,
              activate: mcpActivation(qualifiedName),
              origin: "MCP tool",
              source_path: server.name,
              parameters: tool.inputSchema,
            };
          }),
          warnings:
            valid.length < available.length
              ? [`${server.name}: skipped tools with invalid names`]
              : [],
        };
      }),
    );
    signal?.throwIfAborted();
    return {
      skills: [
        ...catalog.skills,
        ...tools.flatMap((result) => (result.status === "fulfilled" ? result.value.skills : [])),
      ],
      warnings: tools.flatMap((result, index) =>
        result.status === "rejected"
          ? [
              `${servers[index].name}: ${result.reason instanceof Error ? result.reason.message : String(result.reason)}`,
            ]
          : result.value.warnings,
      ),
    };
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
    return (
      await apiFetch(`/api/find-files?${params.toString()}`, "Failed to find files", { signal })
    ).json();
  }

  async renameConversation(conversationId: string, slug: string): Promise<Conversation> {
    return apiJSON(
      `/api/conversation/${conversationId}/rename`,
      "Failed to rename conversation",
      "POST",
      { slug },
    );
  }

  // Moves an existing conversation to a different working directory: the
  // user-driven counterpart of the agent's change_dir tool. The server
  // validates the path, updates the live toolset, and tells the agent, so this
  // is deliberately not a draft-style local update. Rejections are meaningful
  // (a missing directory, or a turn in flight), so the message is surfaced.
  async setConversationCwd(conversationId: string, cwd: string): Promise<Conversation> {
    return apiJSON(
      `/api/conversation/${conversationId}/cwd`,
      "Failed to change directory",
      "POST",
      { cwd },
    );
  }

  // Turns the compact_in_place tool on in a conversation started without it
  // (the Compact in Place button). Refused mid-turn; the message says so.
  async enableCompactInPlace(conversationId: string): Promise<Conversation> {
    return apiJSON(
      `/api/conversation/${conversationId}/enable-compact-in-place`,
      "Failed to enable compacting in place",
      "POST",
    );
  }

  // Changes an existing conversation's settings (see SettingsChange). The
  // server records a marker and broadcasts the updated conversation.
  async updateConversationSettings(
    conversationId: string,
    change: SettingsChange,
  ): Promise<ConversationSettings> {
    return apiJSON(
      `/api/conversation/${conversationId}/settings`,
      "Failed to change settings",
      "POST",
      change,
    );
  }

  async updateConversationTags(conversationId: string, tags: string[]): Promise<Conversation> {
    return apiJSON(`/api/conversation/${conversationId}/tags`, "Failed to update tags", "POST", {
      tags,
    });
  }

  async getBackgroundJobs(conversationId: string): Promise<BackgroundJob[]> {
    return apiJSON(
      `/api/conversation/${encodeURIComponent(conversationId)}/background-jobs`,
      "Failed to get background jobs",
    );
  }

  async killBackgroundJob(conversationId: string, jobId: string): Promise<void> {
    await apiFetch(
      `/api/conversation/${encodeURIComponent(conversationId)}/background-jobs/${encodeURIComponent(jobId)}/kill`,
      "Failed to kill background job",
      { method: "POST" },
    );
  }

  async getStatusBar(conversationId: string, signal?: AbortSignal): Promise<StatusBar> {
    return (
      await apiFetch(
        `/api/conversation/${encodeURIComponent(conversationId)}/status-bar`,
        "Failed to get status bar",
        { signal },
      )
    ).json();
  }

  // Version check APIs
  async checkVersion(forceRefresh = false): Promise<VersionInfo> {
    const url = forceRefresh ? "/version-check?refresh=true" : "/version-check";
    return apiJSON(url, "Failed to check version");
  }

  async getChangelog(currentTag: string, latestTag: string): Promise<CommitInfo[]> {
    const params = new URLSearchParams({ current: currentTag, latest: latestTag });
    return apiJSON(`/version-changelog?${params}`, "Failed to get changelog");
  }

  async upgrade(restart: boolean = false): Promise<{ status: string; message: string }> {
    const url = restart ? "/upgrade?restart=true" : "/upgrade";
    return apiJSON(url, "Failed to upgrade", "POST");
  }

  async getVersion(): Promise<{ tag?: string; commit?: string }> {
    // A hard deadline, kept through the body. AbortSignal.timeout() isn't
    // available in every browser we support.
    const ctl = new AbortController();
    const timer = setTimeout(() => ctl.abort(), 5000);
    try {
      const response = await apiFetch("/version", "Failed to get version", { signal: ctl.signal });
      return await response.json();
    } finally {
      clearTimeout(timer);
    }
  }

  async upgradeHeadlessShell(): Promise<{ status: string; message: string; version: string }> {
    return (
      await apiFetch("/upgrade-headless-shell", "Failed to upgrade headless-shell", {
        method: "POST",
      })
    ).json();
  }

  // restart exits Shelley for systemd to start the installed binary;
  // active conversations resume.
  async restart(): Promise<{ status: string; message: string }> {
    return apiJSON("/exit?resume=true", "Failed to restart", "POST");
  }

  async getSettings(): Promise<Record<string, string>> {
    return apiJSON("/settings", "Failed to get settings");
  }

  async setSetting(key: string, value: string): Promise<{ status: string }> {
    return apiJSON("/settings", "Failed to save setting", "POST", { key, value });
  }

  async dismissResourceNotice(kind: ResourceKind, episodeId: number): Promise<ResourceStatus> {
    return apiJSON(`/api/resources/${kind}/dismiss`, "Failed to dismiss", "POST", {
      episode_id: episodeId,
    });
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
    return apiJSON("/feature-flags", "Failed to load feature flags");
  },
  async set(name: string, value: unknown): Promise<void> {
    await apiFetch("/feature-flags", "Failed to set flag", sendJSON("POST", { name, value }));
  },
  async clear(name: string): Promise<void> {
    await apiFetch("/feature-flags", "Failed to clear flag", sendJSON("DELETE", { name }));
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
    return apiJSON("/api/favicon-emoji", "Failed to load favicon emoji");
  },
  async set(emoji: string): Promise<FaviconEmoji> {
    return apiJSON("/api/favicon-emoji", "Failed to save favicon emoji", "PUT", { emoji });
  },
};

// models.dev pricing, USD per million tokens. null = model known but unpriced.
// Mirrors ModelCost in utils/tokenCostGraph; stays structurally compatible so
// the fetched costs feed the cost-graph math directly.
export interface ModelCostDTO {
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
  // Context-size pricing cliffs (ascending by context); absent for flat
  // pricing. See effectiveCost in utils/tokenCostGraph.
  tiers?: CostTier[];
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
      const r = await apiFetch(
        "/api/model-costs",
        "Failed to load model costs",
        sendJSON("POST", { models: missing }),
      );
      const data = (await r.json()) as { costs: Record<string, ModelCostDTO | null> };
      for (const m of missing) modelCostCache.set(m.model, data.costs[m.model] ?? null);
    }
    const out: Record<string, ModelCostDTO | null> = {};
    for (const m of models) out[m.model] = modelCostCache.get(m.model) ?? null;
    return out;
  },
};

// Descendant direct and indirect usage grouped by (model, endpoint).
// One (model, endpoint, effective-rate) aggregate of subagent usage. A model
// whose calls crossed a context-size pricing tier yields more than one row —
// one per rate — each carrying that tier's flat rates in `cost` (no nested
// tiers). The UI regroups rows by model and shows "mixed rates" where they
// differ; estimated_usd is already tier-accurate.
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
    const r = await apiFetch(
      `/api/conversation/${conversationId}/subagent-usage`,
      "Failed to load subagent usage",
      {
        headers: { "X-Shelley-Request": "1" },
        signal,
      },
    );
    return (await r.json()) as SubagentUsageDTO;
  },
};

// Custom models API
export interface CustomModel {
  model_id: string;
  display_name: string;
  provider_type: "anthropic" | "openai" | "openai-responses" | "gemini";
  endpoint: string;
  has_api_key: boolean;
  model_name: string;
  max_tokens: number;
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
  async getCustomModels(): Promise<CustomModel[]> {
    return apiJSON("/api/custom-models", "Failed to get custom models");
  }

  async createCustomModel(request: CreateCustomModelRequest): Promise<CustomModel> {
    return apiJSON("/api/custom-models", "Failed to create custom model", "POST", request);
  }

  async updateCustomModel(
    modelId: string,
    request: Partial<CreateCustomModelRequest>,
  ): Promise<CustomModel> {
    return apiJSON(
      `/api/custom-models/${modelId}`,
      "Failed to update custom model",
      "PUT",
      request,
    );
  }

  async deleteCustomModel(modelId: string): Promise<void> {
    await apiFetch(`/api/custom-models/${modelId}`, "Failed to delete custom model", {
      method: "DELETE",
    });
  }

  async duplicateCustomModel(modelId: string, displayName?: string): Promise<CustomModel> {
    return apiJSON(
      `/api/custom-models/${modelId}/duplicate`,
      "Failed to duplicate custom model",
      "POST",
      { display_name: displayName },
    );
  }

  async testCustomModel(
    request: TestCustomModelRequest,
  ): Promise<{ success: boolean; message: string }> {
    return apiJSON("/api/custom-models-test", "Failed to test custom model", "POST", request);
  }
}

export const customModelsApi = new CustomModelsApi();

export type ModelProviderType = "anthropic" | "openai" | "openai-responses" | "gemini";

export interface ModelProviderInput {
  provider_type: ModelProviderType;
  endpoint: string;
  api_key: string;
  display_name: string;
}

export interface ModelProviderConnection {
  provider_id: string;
  provider_type: ModelProviderType;
  endpoint: string;
  display_name: string;
  has_api_key: boolean;
  model_count: number;
}

export const modelProvidersApi = {
  async check(input: ModelProviderInput, id?: string): Promise<{ id: string; name?: string }[]> {
    const path = id
      ? `/api/model-providers/${encodeURIComponent(id)}/check`
      : "/api/model-providers/check";
    return apiJSON(path, "Failed to check models", "POST", input);
  },

  async list(): Promise<ModelProviderConnection[]> {
    return apiJSON("/api/model-providers", "Failed to load providers");
  },

  async create(input: ModelProviderInput): Promise<ModelProviderConnection> {
    return apiJSON("/api/model-providers", "Failed to add provider", "POST", input);
  },

  async update(id: string, input: ModelProviderInput): Promise<ModelProviderConnection> {
    return apiJSON(
      `/api/model-providers/${encodeURIComponent(id)}`,
      "Failed to update provider",
      "PUT",
      input,
    );
  },

  async refresh(id: string): Promise<ModelProviderConnection> {
    return apiJSON(
      `/api/model-providers/${encodeURIComponent(id)}/refresh`,
      "Failed to refresh provider",
      "POST",
    );
  },

  async remove(id: string): Promise<void> {
    await apiFetch(`/api/model-providers/${encodeURIComponent(id)}`, "Failed to remove provider", {
      method: "DELETE",
    });
  },
};

export interface McpServer {
  name: string;
  url: string;
  description: string;
  headers: Record<string, string>;
  auth?: "" | "logged_in" | "login_required";
  login_url?: string;
  integration?: string;
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
  const response = await apiFetch(`/api/mcp/servers${path}`, failure, init);
  return response;
}

const mcpPath = (name: string) => `/${encodeURIComponent(name)}`;

export const mcpServersApi = {
  async list(signal?: AbortSignal): Promise<McpServer[]> {
    return (await mcpFetch("", "Failed to load MCP servers", { signal })).json();
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
  async tools(name: string, signal?: AbortSignal): Promise<McpTool[]> {
    const response = await mcpFetch(`${mcpPath(name)}/tools`, "Failed to list tools", { signal });
    return ((await response.json()) as { tools: McpTool[] }).tools;
  },
};

export const profilesApi = {
  async list(): Promise<Profile[]> {
    return apiJSON("/api/profiles", "Failed to load profiles");
  },
  /** voicePrompt, if given, is saved too; else a new profile has none and
   *  an updated one keeps its own. */
  async create(name: string, settings: Settings, voicePrompt?: string): Promise<Profile> {
    const response = await apiFetch(
      "/api/profiles",
      "Failed to save profile",
      sendJSON("POST", { name, ...settings, voice_prompt: voicePrompt }),
    );
    return response.json();
  },
  async update(name: string, settings: Settings, voicePrompt?: string): Promise<Profile> {
    const response = await apiFetch(
      `/api/profiles/${encodeURIComponent(name)}`,
      "Failed to save profile",
      sendJSON("PUT", { ...settings, voice_prompt: voicePrompt }),
    );
    return response.json();
  },
  async makeDefault(name: string): Promise<Profile> {
    const response = await apiFetch(
      `/api/profiles/${encodeURIComponent(name)}/default`,
      "Failed to change the default profile",
      { method: "POST" },
    );
    return response.json();
  },
  async restore(name: string): Promise<Profile> {
    const response = await apiFetch(
      `/api/profiles/${encodeURIComponent(name)}/restore`,
      "Failed to restore profile defaults",
      { method: "POST" },
    );
    return response.json();
  },
  async remove(name: string): Promise<void> {
    await apiFetch(`/api/profiles/${encodeURIComponent(name)}`, "Failed to delete profile", {
      method: "DELETE",
    });
  },
  /** A voice prompt written from a profile's model and system prompt, for
   *  the editor; nothing is saved. */
  async generateVoicePrompt(model: string, systemPrompt: string): Promise<string> {
    const response = await apiFetch(
      "/api/profiles/voice-prompt/generate",
      "Failed to generate a voice prompt",
      sendJSON("POST", { model, system_prompt: systemPrompt }),
    );
    return ((await response.json()) as { voice_prompt: string }).voice_prompt;
  },
  async systemPrompt(): Promise<SystemPromptInfo> {
    return apiJSON("/api/system-prompt", "Failed to load the system prompt");
  },
  /** Why template can't be rendered, or null if it can. */
  async checkSystemPrompt(template: string, signal: AbortSignal): Promise<TemplateProblem | null> {
    const response = await apiFetch(
      "/api/system-prompt/check",
      "Failed to check the template",
      sendJSON("POST", { template }, signal),
    );
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
  configured_secrets: string[];
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
  async getChannels(): Promise<NotificationChannelAPI[]> {
    return apiJSON("/api/notification-channels", "Failed to get notification channels");
  }

  async createChannel(request: CreateNotificationChannelRequest): Promise<NotificationChannelAPI> {
    return apiJSON(
      "/api/notification-channels",
      "Failed to create notification channel",
      "POST",
      request,
    );
  }

  async updateChannel(
    channelId: string,
    request: UpdateNotificationChannelRequest,
  ): Promise<NotificationChannelAPI> {
    return apiJSON(
      `/api/notification-channels/${channelId}`,
      "Failed to update notification channel",
      "PUT",
      request,
    );
  }

  async deleteChannel(channelId: string): Promise<void> {
    await apiFetch(
      `/api/notification-channels/${channelId}`,
      "Failed to delete notification channel",
      { method: "DELETE" },
    );
  }

  async testChannel(channelId: string): Promise<{ success: boolean; message: string }> {
    return apiJSON(
      `/api/notification-channels/${channelId}/test`,
      "Failed to test notification channel",
      "POST",
    );
  }
}

export const notificationChannelsApi = new NotificationChannelsApi();

// A stored secret, without its value (server Secret, docs/secrets.md).
export interface Secret {
  id: string;
  name: string;
  kind: string;
  conversation_id: string | null;
  conversation_slug: string | null;
  created_at: string;
  injections: Array<{
    conversation_id: string | null;
    machine: string;
    path: string;
    created_at: string;
  }>;
}

const secretsRequest = (method: string, path: string, body?: unknown) =>
  apiFetch(`/api/secrets${path}`, "Secrets", sendJSON(method, body));

export const secretsApi = {
  async list(): Promise<Secret[]> {
    return (await secretsRequest("GET", "")).json();
  },
  async create(secret: {
    id: string;
    name: string;
    kind: string;
    value: string;
    conversation_id: string | null;
  }): Promise<void> {
    await secretsRequest("POST", "", secret);
  },
  async rename(id: string, name: string): Promise<void> {
    await secretsRequest("PATCH", `/${encodeURIComponent(id)}`, { name });
  },
  async remove(id: string): Promise<void> {
    await secretsRequest("DELETE", `/${encodeURIComponent(id)}`);
  },
  async reveal(id: string): Promise<string> {
    return (await (await secretsRequest("POST", `/${encodeURIComponent(id)}/reveal`)).json()).value;
  },
};

export interface ChatGPTAccount {
  id: string;
  label: string;
  name?: string;
  signed_in: boolean;
  plan_usage: boolean;
}

export interface ChatGPTAccounts {
  enabled: boolean;
  accounts: ChatGPTAccount[];
}

export interface ChatGPTSignIn {
  id: string;
  authorize_url: string;
  redirect_uri: string;
}

export interface ChatGPTSignInResult {
  account: ChatGPTAccount;
  new_registration: boolean;
}

export interface ChatGPTSignInStatus {
  done: boolean;
  result?: ChatGPTSignInResult;
  error?: string;
}

async function chatgptPost<T>(path: string, body: unknown, fallback: string): Promise<T> {
  const response = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!response.ok) throw await responseError(response, fallback);
  return response.json();
}

export const chatgptApi = {
  async accounts(): Promise<ChatGPTAccounts> {
    const response = await fetch("/api/chatgpt/accounts");
    if (!response.ok) throw await responseError(response, "Failed to load ChatGPT accounts");
    return response.json();
  },

  startSignIn(accountId?: string, consent = false): Promise<ChatGPTSignIn> {
    return chatgptPost(
      "/api/chatgpt/sign-in",
      { account_id: accountId ?? "", consent },
      "Failed to start sign-in",
    );
  },

  async status(id: string): Promise<ChatGPTSignInStatus> {
    const response = await fetch(`/api/chatgpt/sign-in/${encodeURIComponent(id)}`);
    if (!response.ok) throw await responseError(response, "Failed to check sign-in");
    return response.json();
  },

  complete(url: string): Promise<ChatGPTSignInResult> {
    return chatgptPost("/api/chatgpt/sign-in/complete", { url }, "Failed to finish sign-in");
  },

  signOut(id: string, remove: boolean): Promise<{ warning?: string }> {
    return chatgptPost(
      `/api/chatgpt/accounts/${encodeURIComponent(id)}/sign-out`,
      { remove },
      "Failed to sign out",
    );
  },
};
