import {
  Conversation,
  ConversationWithState,
  StreamResponse,
  ChatRequest,
  GitDiffInfo,
  GitFileInfo,
  GitFileDiff,
} from "../types";

class ApiService {
  private baseUrl = "/api";

  // Common headers for state-changing requests (CSRF protection)
  private postHeaders = {
    "Content-Type": "application/json",
    "X-Shelley-Request": "1",
  };

  async getConversations(): Promise<ConversationWithState[]> {
    const response = await fetch(`${this.baseUrl}/conversations`);
    if (!response.ok) {
      throw new Error(`Failed to get conversations: ${response.statusText}`);
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

  createMessageStream(conversationId: string): EventSource {
    return new EventSource(`${this.baseUrl}/conversation/${conversationId}/stream`);
  }

  async cancelConversation(conversationId: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/cancel`, {
      method: "POST",
      headers: { "X-Shelley-Request": "1" },
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
    entries: Array<{ name: string; is_dir: boolean }>;
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
      headers: { "X-Shelley-Request": "1" },
    });
    if (!response.ok) {
      throw new Error(`Failed to archive conversation: ${response.statusText}`);
    }
    return response.json();
  }

  async unarchiveConversation(conversationId: string): Promise<Conversation> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/unarchive`, {
      method: "POST",
      headers: { "X-Shelley-Request": "1" },
    });
    if (!response.ok) {
      throw new Error(`Failed to unarchive conversation: ${response.statusText}`);
    }
    return response.json();
  }

  async deleteConversation(conversationId: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/delete`, {
      method: "POST",
      headers: { "X-Shelley-Request": "1" },
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
}

export const api = new ApiService();
