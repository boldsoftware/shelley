import type { ActionRunSummary } from "../services/workspace";

interface ActionStatus {
  label: string;
  tone: "progress" | "success" | "danger" | "warning";
  icon: string;
}

/** The run's one-pill summary: the command's outcome. */
export function runStatus(run: Pick<ActionRunSummary, "status" | "exit_code">): ActionStatus {
  switch (run.status) {
    case "running":
      return { label: "Running", tone: "progress", icon: "pi pi-spin pi-spinner" };
    case "interrupted":
      return { label: "Interrupted", tone: "warning", icon: "pi pi-stop-circle" };
    case "failed":
      return {
        label: `Failed${run.exit_code !== undefined ? ` (exit ${run.exit_code})` : ""}`,
        tone: "danger",
        icon: "pi pi-times-circle",
      };
    case "succeeded":
      return { label: "Succeeded", tone: "success", icon: "pi pi-check-circle" };
  }
}

/** Elapsed time from start to end (or now, while running), e.g. "3m 12s". */
export function elapsed(start?: string, end?: string, now = Date.now()): string {
  if (!start) return "";
  const ms = (end ? Date.parse(end) : now) - Date.parse(start);
  if (!(ms >= 0)) return "";
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
}
