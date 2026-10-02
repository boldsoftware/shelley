export interface SlashCommand {
  command: `/${string}`;
  description: string;
  takesArgs: boolean;
}

export const SLASH_COMMANDS = {
  SKILLS: {
    command: "/skills",
    description: "adds skill instructions to your draft",
    takesArgs: true,
  },
  BTW: {
    command: "/btw",
    description: "asks a one-off side question",
    takesArgs: true,
  },
  TOUR: {
    command: "/tour",
    description: "builds a guided commit tour",
    takesArgs: true,
  },
  TRANSCRIPTION: {
    command: "/transcription",
    description: "transcribes an uploaded recording",
    takesArgs: true,
  },
  FORK: {
    command: "/fork",
    description: "forks this conversation",
    takesArgs: false,
  },
  DIFF: {
    command: "/diff",
    description: "opens the diff viewer",
    takesArgs: false,
  },
  SHELL: {
    command: "/shell",
    description: "runs in shell (! alias)",
    takesArgs: true,
  },
  COMPACT: {
    command: "/compact",
    description: "compacts this conversation",
    takesArgs: true,
  },
  DISTILL: {
    // Legacy alias for /compact, kept for compatibility. Compacts too.
    command: "/distill",
    description: "compacts this conversation (alias for /compact)",
    takesArgs: true,
  },
  CLEAR: {
    command: "/clear",
    description: "clears context, keeping this conversation",
    takesArgs: false,
  },
  NEW: {
    command: "/new",
    description: "starts a new conversation",
    takesArgs: true,
  },
  ARCHIVE: {
    command: "/archive",
    description: "archives this conversation",
    takesArgs: false,
  },
  RENAME: {
    command: "/rename",
    description: "renames this conversation",
    takesArgs: true,
  },
} as const satisfies Record<string, SlashCommand>;

export function slashCommandsForConversation(isChildConversation: boolean): SlashCommand[] {
  return Object.values(SLASH_COMMANDS).filter(
    (item) =>
      !isChildConversation ||
      (item.command !== SLASH_COMMANDS.BTW.command && item.command !== SLASH_COMMANDS.TOUR.command),
  );
}
