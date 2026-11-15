import type { HookHealthStatus } from "../types";

export function hookDiagnosticPrompt(status: HookHealthStatus): string {
  return [
    "Diagnose Shelley's failing local end-of-turn hook.",
    "",
    "Safe diagnostic summary:",
    "Hook: end-of-turn",
    `Consecutive failed invocations: ${status.failures}`,
    `Latest failure (UTC): ${status.last_failure_at}`,
    `Latest affected conversation ID: ${status.conversation_id}`,
    "",
    "Start read-only: find the configured end-of-turn hook and inspect relevant logs and scripts. " +
      "Identify the underlying cause, including any child script that failed. " +
      "Do not assume the current working directory is Shelley's configuration directory. " +
      "Do not rerun the hook, edit files, disable hooks, restart services, or make other changes without asking me first.",
    "Never print or copy credentials, webhook URLs, tokens, or other secrets. Redact them from any excerpts.",
    "Explain the cause and propose the smallest fix.",
  ].join("\n");
}
