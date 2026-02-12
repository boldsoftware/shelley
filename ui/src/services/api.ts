import {
  Conversation,
  ConversationWithState,
  StreamResponse,
  ChatRequest,
  GitDiffInfo,
  GitFileInfo,
  GitFileDiff,
  VersionInfo,
  CommitInfo,
} from "../types";

class ApiService {
  private baseUrl = "/api";

  private postHeaders = {
    "Content-Type": "application/json",
  };

  async getConversations(): Promise<ConversationWithState[]> {
    const response = await fetch(`${this.baseUrl}/conversations`);
    if (!response.ok) {
      throw new Error(`Failed to get conversations: ${response.statusText}`);
    }
    return response.json();
  }

  async getModels(): Promise<
    Array<{
      id: string;
      display_name?: string;
      source?: string;
      ready: boolean;
      max_context_tokens?: number;
    }>
  > {
    const response = await fetch(`${this.baseUrl}/models`);
    if (!response.ok) {
      throw new Error(`Failed to get models: ${response.statusText}`);
    }
    return response.json();
  }

  async searchConversations(query: string): Promise<ConversationWithState[]> {
    const params = new URLSearchParams({
      q: query,
      search_content: "true",
    });
    const response = await fetch(`${this.baseUrl}/conversations?${params}`);
    if (!response.ok) {
      throw new Error(`Failed to search conversations: ${response.statusText}`);
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
      throw new Error(`Failed to send message: ${response.statusText}`);
    }
    return response.json();
  }

  async continueConversation(
    sourceConversationId: string,
    model?: string,
    cwd?: string,
  ): Promise<{ conversation_id: string }> {
    const response = await fetch(`${this.baseUrl}/conversations/continue`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({
        source_conversation_id: sourceConversationId,
        model: model || "",
        cwd: cwd || "",
      }),
    });
    if (!response.ok) {
      throw new Error(`Failed to continue conversation: ${response.statusText}`);
    }
    return response.json();
  }

  async distillConversation(
    sourceConversationId: string,
    model?: string,
    cwd?: string,
  ): Promise<{ conversation_id: string }> {
    const response = await fetch(`${this.baseUrl}/conversations/distill`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({
        source_conversation_id: sourceConversationId,
        model: model || "",
        cwd: cwd || "",
      }),
    });
    if (!response.ok) {
      throw new Error(`Failed to distill conversation: ${response.statusText}`);
    }
    return response.json();
  }

  async getConversation(conversationId: string): Promise<StreamResponse> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}`);
    if (!response.ok) {
      throw new Error(`Failed to get messages: ${response.statusText}`);
    }
    return response.json();
  }

  async sendMessage(conversationId: string, request: ChatRequest): Promise<void> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/chat`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify(request),
    });
    if (!response.ok) {
      throw new Error(`Failed to send message: ${response.statusText}`);
    }
  }

  createMessageStream(conversationId: string, lastSequenceId?: number): EventSource {
    let url = `${this.baseUrl}/conversation/${conversationId}/stream`;
    if (lastSequenceId !== undefined && lastSequenceId >= 0) {
      url += `?last_sequence_id=${lastSequenceId}`;
    }
    return new EventSource(url);
  }

  async cancelConversation(conversationId: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/cancel`, {
      method: "POST",
    });
    if (!response.ok) {
      throw new Error(`Failed to cancel conversation: ${response.statusText}`);
    }
  }

  async validateCwd(path: string): Promise<{ valid: boolean; error?: string }> {
    const response = await fetch(`${this.baseUrl}/validate-cwd?path=${encodeURIComponent(path)}`);
    if (!response.ok) {
      throw new Error(`Failed to validate cwd: ${response.statusText}`);
    }
    return response.json();
  }

  async listDirectory(path?: string): Promise<{
    path: string;
    parent: string;
    entries: Array<{ name: string; is_dir: boolean; git_head_subject?: string }>;
    git_head_subject?: string;
    git_worktree_root?: string;
    error?: string;
  }> {
    const url = path
      ? `${this.baseUrl}/list-directory?path=${encodeURIComponent(path)}`
      : `${this.baseUrl}/list-directory`;
    const response = await fetch(url);
    if (!response.ok) {
      throw new Error(`Failed to list directory: ${response.statusText}`);
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
      throw new Error(`Failed to create directory: ${response.statusText}`);
    }
    return response.json();
  }

  async getArchivedConversations(): Promise<Conversation[]> {
    const response = await fetch(`${this.baseUrl}/conversations/archived`);
    if (!response.ok) {
      throw new Error(`Failed to get archived conversations: ${response.statusText}`);
    }
    return response.json();
  }

  async archiveConversation(conversationId: string): Promise<Conversation> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/archive`, {
      method: "POST",
    });
    if (!response.ok) {
      throw new Error(`Failed to archive conversation: ${response.statusText}`);
    }
    return response.json();
  }

  async unarchiveConversation(conversationId: string): Promise<Conversation> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/unarchive`, {
      method: "POST",
    });
    if (!response.ok) {
      throw new Error(`Failed to unarchive conversation: ${response.statusText}`);
    }
    return response.json();
  }

  async deleteConversation(conversationId: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/delete`, {
      method: "POST",
    });
    if (!response.ok) {
      throw new Error(`Failed to delete conversation: ${response.statusText}`);
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
      throw new Error(`Failed to get conversation by slug: ${response.statusText}`);
    }
    return response.json();
  }

  // Git diff APIs
  async getGitDiffs(cwd: string): Promise<{ diffs: GitDiffInfo[]; gitRoot: string }> {
    const response = await fetch(`${this.baseUrl}/git/diffs?cwd=${encodeURIComponent(cwd)}`);
    if (!response.ok) {
      const text = await response.text();
      throw new Error(text || response.statusText);
    }
    return response.json();
  }

  async getGitDiffFiles(diffId: string, cwd: string): Promise<GitFileInfo[]> {
    const response = await fetch(
      `${this.baseUrl}/git/diffs/${diffId}/files?cwd=${encodeURIComponent(cwd)}`,
    );
    if (!response.ok) {
      throw new Error(`Failed to get diff files: ${response.statusText}`);
    }
    return response.json();
  }

  async getGitFileDiff(diffId: string, filePath: string, cwd: string): Promise<GitFileDiff> {
    const response = await fetch(
      `${this.baseUrl}/git/file-diff/${diffId}/${filePath}?cwd=${encodeURIComponent(cwd)}`,
    );
    if (!response.ok) {
      throw new Error(`Failed to get file diff: ${response.statusText}`);
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
      throw new Error(`Failed to rename conversation: ${response.statusText}`);
    }
    return response.json();
  }

  async getSubagents(conversationId: string): Promise<Conversation[]> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/subagents`);
    if (!response.ok) {
      throw new Error(`Failed to get subagents: ${response.statusText}`);
    }
    return response.json();
  }

  // Version check APIs
  async checkVersion(forceRefresh = false): Promise<VersionInfo> {
    const url = forceRefresh ? "/version-check?refresh=true" : "/version-check";
    const response = await fetch(url);
    if (!response.ok) {
      throw new Error(`Failed to check version: ${response.statusText}`);
    }
    return response.json();
  }

  async getChangelog(currentTag: string, latestTag: string): Promise<CommitInfo[]> {
    const params = new URLSearchParams({ current: currentTag, latest: latestTag });
    const response = await fetch(`/version-changelog?${params}`);
    if (!response.ok) {
      throw new Error(`Failed to get changelog: ${response.statusText}`);
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

  async exit(): Promise<{ status: string; message: string }> {
    const response = await fetch("/exit", {
      method: "POST",
    });
    if (!response.ok) {
      throw new Error(`Failed to exit: ${response.statusText}`);
    }
    return response.json();
  }

  async getSettings(): Promise<Record<string, string>> {
    const response = await fetch("/settings");
    if (!response.ok) {
      throw new Error(`Failed to get settings: ${response.statusText}`);
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
}

export const api = new ApiService();

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
}

export interface CreateCustomModelRequest {
  display_name: string;
  provider_type: "anthropic" | "openai" | "openai-responses" | "gemini";
  endpoint: string;
  api_key: string;
  model_name: string;
  max_tokens: number;
  tags: string; // Comma-separated tags
}

export interface TestCustomModelRequest {
  model_id?: string; // If provided with empty api_key, use stored key
  provider_type: "anthropic" | "openai" | "openai-responses" | "gemini";
  endpoint: string;
  api_key: string;
  model_name: string;
}

class CustomModelsApi {
  private baseUrl = "/api";

  private postHeaders = {
    "Content-Type": "application/json",
  };

  async getCustomModels(): Promise<CustomModel[]> {
    const response = await fetch(`${this.baseUrl}/custom-models`);
    if (!response.ok) {
      throw new Error(`Failed to get custom models: ${response.statusText}`);
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
      throw new Error(`Failed to create custom model: ${response.statusText}`);
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
      throw new Error(`Failed to update custom model: ${response.statusText}`);
    }
    return response.json();
  }

  async deleteCustomModel(modelId: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/custom-models/${modelId}`, {
      method: "DELETE",
    });
    if (!response.ok) {
      throw new Error(`Failed to delete custom model: ${response.statusText}`);
    }
  }

  async duplicateCustomModel(modelId: string, displayName?: string): Promise<CustomModel> {
    const response = await fetch(`${this.baseUrl}/custom-models/${modelId}/duplicate`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify({ display_name: displayName }),
    });
    if (!response.ok) {
      throw new Error(`Failed to duplicate custom model: ${response.statusText}`);
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
      throw new Error(`Failed to test custom model: ${response.statusText}`);
    }
    return response.json();
  }
}

export const customModelsApi = new CustomModelsApi();

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

  async getChannels(): Promise<NotificationChannelAPI[]> {
    const response = await fetch(`${this.baseUrl}/notification-channels`);
    if (!response.ok) {
      throw new Error(`Failed to get notification channels: ${response.statusText}`);
    }
    return response.json();
  }

  async createChannel(request: CreateNotificationChannelRequest): Promise<NotificationChannelAPI> {
    const response = await fetch(`${this.baseUrl}/notification-channels`, {
      method: "POST",
      headers: this.postHeaders,
      body: JSON.stringify(request),
    });
    if (!response.ok) {
      throw new Error(`Failed to create notification channel: ${response.statusText}`);
    }
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
    if (!response.ok) {
      throw new Error(`Failed to update notification channel: ${response.statusText}`);
    }
    return response.json();
  }

  async deleteChannel(channelId: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/notification-channels/${channelId}`, {
      method: "DELETE",
    });
    if (!response.ok) {
      throw new Error(`Failed to delete notification channel: ${response.statusText}`);
    }
  }

  async testChannel(channelId: string): Promise<{ success: boolean; message: string }> {
    const response = await fetch(`${this.baseUrl}/notification-channels/${channelId}/test`, {
      method: "POST",
      headers: this.postHeaders,
    });
    if (!response.ok) {
      throw new Error(`Failed to test notification channel: ${response.statusText}`);
    }
    return response.json();
  }
}

export const notificationChannelsApi = new NotificationChannelsApi();
