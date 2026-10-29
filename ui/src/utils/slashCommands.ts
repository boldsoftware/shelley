import { wording } from "./workspaceMode";
export interface SlashCommand {
  command: `/${string}`;
  description: string;
  takesArgs: boolean;
}

export const SLASH_COMMANDS = {
  SKILLS: {
    command: "/skills",
    description: "attaches a skill to your message",
    takesArgs: true,
  },
  BTW: {
    command: "/btw",
    description: "asks a one-off side question",
    takesArgs: true,
  },
  TRANSCRIPTION: {
    command: "/transcription",
    description: "transcribes an uploaded recording",
    takesArgs: true,
  },
  FORK: {
    command: "/fork",
    description: wording("forks this conversation", "forks this thread into a new task"),
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
    description: wording("compacts this conversation", "compacts this thread"),
    takesArgs: true,
  },
  CLEAR: {
    command: "/clear",
    description: wording(
      "clears context, keeping this conversation",
      "clears context, keeping this thread",
    ),
    takesArgs: false,
  },
  NEW: {
    command: "/new",
    description: wording("starts a new conversation", "starts a new task"),
    takesArgs: true,
  },
  ARCHIVE: {
    command: "/archive",
    description: wording("archives this conversation", "archives this task"),
    takesArgs: false,
  },
  RENAME: {
    command: "/rename",
    description: wording("renames this conversation", "renames this task"),
    takesArgs: true,
  },
} as const satisfies Record<string, SlashCommand>;

export function slashCommandsForConversation(isChildConversation: boolean): SlashCommand[] {
  return Object.values(SLASH_COMMANDS).filter(
    (item) => !isChildConversation || item.command !== SLASH_COMMANDS.BTW.command,
  );
}
