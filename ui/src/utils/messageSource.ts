export interface ConversationMessageSource {
  conversationId: string;
  slug: string;
  relationship: "subagent" | "parent";
}

export interface BackgroundJobOutcome {
  command: string;
  // Null when the job was lost: the host rebooted or the job was killed
  // before it could record its exit status.
  exitCode: number | null;
  // Go duration string, empty when the job was lost.
  duration: string;
  logPath: string;
  tail: string;
}

export interface BackgroundJobMessageSource {
  backgroundJobId: string;
  // Absent on notices recorded before outcomes were stored structurally.
  outcome?: BackgroundJobOutcome;
}

export type MessageSource = ConversationMessageSource | BackgroundJobMessageSource;

// An MCP notice is a user message to the model (that the registered MCP
// servers changed mid-turn) but a system notice to the reader.
export interface McpNoticeMessageSource {
  // "added", "updated" or "removed".
  mcpServerChange: string;
  serverName: string;
}

// Persisted messages carry JSON text; queued ghosts carry the decoded object.
function parseUserData(userData: unknown): Record<string, unknown> | null {
  if (!userData) return null;
  let parsed: unknown = userData;
  if (typeof parsed === "string") {
    try {
      parsed = JSON.parse(parsed);
    } catch {
      return null;
    }
  }
  if (typeof parsed !== "object" || parsed === null) return null;
  return parsed as Record<string, unknown>;
}

// messageSource identifies who, other than a human, sent a user message:
// another conversation, or a backgrounded bash job that finished.
export function messageSource(userData: unknown): MessageSource | null {
  const parsed = parseUserData(userData);
  if (!parsed) return null;

  const { background_job_id: backgroundJobId } = parsed;
  if (typeof backgroundJobId === "string" && backgroundJobId) {
    const outcome = backgroundJobOutcome(parsed);
    return outcome ? { backgroundJobId, outcome } : { backgroundJobId };
  }

  const {
    sender_conversation_id: conversationId,
    sender_slug: slug,
    sender_relationship: relationship,
  } = parsed;
  if (
    typeof conversationId !== "string" ||
    !conversationId ||
    typeof slug !== "string" ||
    (relationship !== "subagent" && relationship !== "parent")
  ) {
    return null;
  }
  return { conversationId, slug, relationship };
}

// mcpNoticeSource identifies a notice that the registered MCP servers changed
// while the agent was working. It is separate from messageSource so the
// MessageSource union (and its consumers) stay unchanged.
export function mcpNoticeSource(userData: unknown): McpNoticeMessageSource | null {
  const parsed = parseUserData(userData);
  if (!parsed) return null;
  const { mcp_server_change: change, server_name: name } = parsed;
  if (typeof change !== "string" || !change) return null;
  return { mcpServerChange: change, serverName: typeof name === "string" ? name : "" };
}

function backgroundJobOutcome(parsed: Record<string, unknown>): BackgroundJobOutcome | null {
  const { command, exit_code: exitCode, duration, log_path: logPath, tail } = parsed;
  if (
    typeof command !== "string" ||
    !command ||
    typeof duration !== "string" ||
    typeof logPath !== "string" ||
    typeof tail !== "string"
  ) {
    return null;
  }
  return {
    command,
    exitCode: typeof exitCode === "number" ? exitCode : null,
    duration,
    logPath,
    tail,
  };
}

// Earlier notices only stored background_job_id and the model-facing text.
// Recover the fields from the exact format produced by BackgroundJobOutcome.Notice
// so existing conversation history uses the same bash card as new notices.
export function parseLegacyBackgroundJobNotice(
  text: string,
  jobId: string,
): BackgroundJobOutcome | null {
  const lines = text.split("\n");
  const header =
    /^Background job (\S+) (?:finished: exit (\d+), (.+?)\. Log: (.+)|lost \(host rebooted or killed\)\. Log: (.+))$/.exec(
      lines[0],
    );
  const command = /^Command: (.+)$/.exec(lines[1] ?? "");
  if (!header || header[1] !== jobId || !command || (lines.length > 2 && lines[2] !== "")) {
    return null;
  }
  const exitCode = header[2] === undefined ? null : Number(header[2]);
  if (exitCode !== null && !Number.isSafeInteger(exitCode)) return null;
  return {
    command: command[1],
    exitCode,
    duration: header[3] ?? "",
    logPath: header[4] ?? header[5],
    tail: lines.slice(3).join("\n"),
  };
}

// A report of a project action run's result is a user message to the model
// but the run's card to the reader.
export interface ActionReportSource {
  runId: string;
  label: string;
  command: string;
  status: string;
  exitCode: number | null;
}

export function actionReportSource(userData: unknown): ActionReportSource | null {
  const parsed = parseUserData(userData);
  if (!parsed) return null;
  const { action_run_id: runId, action_report: report } = parsed;
  if (typeof runId !== "string" || !runId || report !== "result") return null;
  const text = (key: string) => (typeof parsed[key] === "string" ? (parsed[key] as string) : "");
  return {
    runId,
    label: text("label"),
    command: text("command"),
    status: text("status"),
    exitCode: typeof parsed.exit_code === "number" ? parsed.exit_code : null,
  };
}
