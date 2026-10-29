import { apiJSON } from "./api";

export interface Customization {
  conversation_id: string;
  /** installed: on disk, waiting for a restart; running: the server runs it. */
  status:
    | "waiting"
    | "verifying"
    | "ready"
    | "stale"
    | "failed"
    | "installed"
    | "restarting"
    | "running";
  commit: string;
  artifact_sha256: string;
  /** The conversation's work is done: running this build archives it. */
  finished: boolean;
  error: string;
  updated_at: string;
  running_under_systemd: boolean;
  /**
   * Automatic restart into this installed build: pending (after
   * auto_restart_in_ms more of user idleness) or busy (once work finishes).
   */
  auto_restart?: "pending" | "busy";
  auto_restart_in_ms?: number;
}

export const customizationsApi = {
  list: () => apiJSON<Customization[]>("/api/customizations", "Failed to load build status"),
  /** Verifies again, keeping the conversation's finished claim. */
  verify: (state: Customization) =>
    apiJSON<Customization>(
      `/api/conversation/${encodeURIComponent(state.conversation_id)}/customization/verify`,
      "Failed to verify",
      "POST",
      { finished: state.finished },
    ),
  restart: (state: Customization) =>
    apiJSON<Customization>(
      `/api/conversation/${encodeURIComponent(state.conversation_id)}/customization/restart`,
      "Failed to restart Shelley",
      "POST",
      { commit: state.commit, artifact_sha256: state.artifact_sha256 },
    ),
  cancelAutoRestart: (state: Customization) =>
    apiJSON<Customization>(
      `/api/conversation/${encodeURIComponent(state.conversation_id)}/customization/auto-restart/cancel`,
      "Failed to cancel the automatic restart",
      "POST",
      { artifact_sha256: state.artifact_sha256 },
    ),
};
