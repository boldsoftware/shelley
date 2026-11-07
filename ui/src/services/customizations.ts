import { apiJSON } from "./api";

/** A task's latest scripts/install-custom run, kept in memory by the server. */
export interface Customization {
  conversation_id: string;
  /** installed: on disk, waiting for a restart. */
  status: "verifying" | "failed" | "installed" | "restarting";
  commit: string;
  error: string;
  updated_at: string;
  running_under_systemd: boolean;
  /**
   * Automatic restart into this installed build: pending (after
   * auto_restart_in_ms more of user idleness) or busy (once work finishes).
   */
  auto_restart?: "pending" | "busy";
  auto_restart_in_ms?: number;
  /** When the automatic restart is due, by this browser's clock. */
  auto_restart_at?: number;
}

export const customizationsApi = {
  restart: (state: Customization) =>
    apiJSON<Customization>(
      `/api/conversation/${encodeURIComponent(state.conversation_id)}/customization/restart`,
      "Failed to restart Shelley",
      "POST",
      { commit: state.commit },
    ),
  cancelAutoRestart: (state: Customization) =>
    apiJSON<Customization>(
      `/api/conversation/${encodeURIComponent(state.conversation_id)}/customization/auto-restart/cancel`,
      "Failed to cancel the automatic restart",
      "POST",
    ),
};
