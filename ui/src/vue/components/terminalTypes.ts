// Shared type for the Vue TerminalPanel port. Vue SFCs cannot cleanly export
// standalone TypeScript types alongside their default component export, so the
// EphemeralTerminal type lives here and is re-exported by TerminalPanel.vue.
// Other code should import it from either this module or TerminalPanel.vue:
//   import type { EphemeralTerminal } from "./components/terminalTypes";
//   import type { EphemeralTerminal } from "./components/TerminalPanel.vue";

export interface EphemeralTerminal {
  id: string;
  command: string;
  cwd: string;
  createdAt: Date;
  // termId is the server-side dtach session id. Set once the websocket reports
  // "attached". When reconnecting to a known session, set this up front so the
  // websocket re-attaches rather than spawning a new session.
  termId?: string;
}
