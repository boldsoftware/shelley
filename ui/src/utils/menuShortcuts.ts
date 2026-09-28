// Single source of truth for the overflow-menu keyboard shortcuts. Shared by:
//   - ChatOverflowMenu.vue  -> renders the kbd hint on each action row
//   - ChatInterface.vue     -> matches keydown events and invokes the same
//                              handler the menu click would
//
// Command Palette (Cmd/Ctrl+K) and Edit File (Cmd/Ctrl+P) are matched in
// App.vue, which owns those two modals. Recording shortcuts also live in App.vue.
// Their display labels live here too so menus can render consistent hints.
//
// Design notes (see issue #248):
//   - Browsers reserve most single Cmd/Ctrl+<letter> combos (Cmd+T new tab,
//     Cmd+W close, Cmd+N new window, ...) and never deliver them to the page,
//     so we cannot use them. Cmd/Ctrl+Shift+<letter> is mostly available; a few
//     have overridable browser defaults (bookmark-all, find-previous, ...) that
//     we preventDefault. The hard exceptions we avoid: Firefox reserves
//     Cmd/Ctrl+Shift+P for "New Private Window" and never delivers it (so
//     Edit File uses Cmd/Ctrl+P instead), and Chrome's Cmd/Ctrl+Shift+A is
//     tab search that users rely on (so Archive uses X).
//   - We match on KeyboardEvent.code (physical key) so Shift-produced glyphs
//     ("D", "<") and keyboard layouts don't matter.
//
// Edit File uses Cmd/Ctrl+P (the browser's print shortcut, which is
// overridable via preventDefault in all major browsers, including Firefox).

export const isMac =
  typeof navigator !== "undefined" && navigator.platform.toUpperCase().includes("MAC");

export type MenuActionId =
  | "commandPalette"
  | "diffs"
  | "gitGraph"
  | "terminal"
  | "archive"
  | "export"
  | "editAgentsMd"
  | "editFile"
  | "checkVersion"
  | "recordAudio"
  | "recordScreen";

interface Combo {
  // "mod" = Cmd on mac / Ctrl elsewhere. "ctrl" = Ctrl on both platforms.
  mod: "mod" | "ctrl";
  shift: boolean;
  alt?: boolean;
  code: string; // KeyboardEvent.code, e.g. "KeyD", "Comma", "Backquote"
  label: string; // glyph shown in the hint, e.g. "D", ",", "`"
}

export const MENU_COMBOS: Record<MenuActionId, Combo> = {
  commandPalette: { mod: "mod", shift: false, code: "KeyK", label: "K" },
  diffs: { mod: "mod", shift: true, code: "KeyD", label: "D" },
  gitGraph: { mod: "mod", shift: true, code: "KeyG", label: "G" },
  terminal: { mod: "ctrl", shift: false, code: "Backquote", label: "`" },
  archive: { mod: "mod", shift: true, code: "KeyX", label: "X" },
  export: { mod: "mod", shift: true, code: "KeyE", label: "E" },
  editAgentsMd: { mod: "mod", shift: true, code: "Comma", label: "," },
  editFile: { mod: "mod", shift: false, code: "KeyP", label: "P" },
  checkVersion: { mod: "mod", shift: true, code: "KeyU", label: "U" },
  recordAudio: { mod: "mod", shift: true, code: "KeyM", label: "M" },
  recordScreen: { mod: "mod", alt: true, shift: true, code: "KeyM", label: "M" },
};

// Menu actions whose keydown is handled inside ChatInterface (i.e. everything
// except the palette, file finder, and recording, which App.vue owns).
export const CHAT_INTERFACE_ACTIONS: readonly MenuActionId[] = [
  "diffs",
  "gitGraph",
  "terminal",
  "archive",
  "export",
  "editAgentsMd",
  "checkVersion",
];

/** Human-readable hint, e.g. "⌘⇧D" on mac or "Ctrl+Shift+D" elsewhere. */
export function menuShortcutLabel(id: MenuActionId): string {
  const c = MENU_COMBOS[id];
  if (isMac) {
    const ctrl = c.mod === "ctrl" ? "\u2303" : ""; // ⌃
    const cmd = c.mod === "mod" ? "\u2318" : ""; // ⌘
    const shift = c.shift ? "\u21e7" : ""; // ⇧
    const alt = c.alt ? "\u2325" : ""; // ⌥
    return `${ctrl}${cmd}${alt}${shift}${c.label}`;
  }
  const parts = ["Ctrl"]; // both "mod" and "ctrl" are Ctrl off-mac
  if (c.alt) parts.push("Alt");
  if (c.shift) parts.push("Shift");
  parts.push(c.label);
  return parts.join("+");
}

/** Does a keydown event match this combo? Matches physical key + modifiers. */
export function comboMatches(e: KeyboardEvent, c: Combo): boolean {
  if (e.code !== c.code) return false;
  if (e.altKey !== !!c.alt) return false;
  if (c.shift !== e.shiftKey) return false;
  if (c.mod === "ctrl") return e.ctrlKey && !e.metaKey;
  // "mod": Cmd-only on mac, Ctrl-only elsewhere.
  return isMac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
}

/** Which ChatInterface-owned menu action (if any) does this event trigger? */
export function matchChatInterfaceAction(e: KeyboardEvent): MenuActionId | null {
  for (const id of CHAT_INTERFACE_ACTIONS) {
    if (comboMatches(e, MENU_COMBOS[id])) return id;
  }
  return null;
}

/** True when the event originated inside an open xterm terminal. The shell
 * owns its keyboard there (Ctrl+M is Enter, Ctrl+K is kill-line, Ctrl+B is a
 * tmux prefix), so global shortcuts must never hijack it. */
export function isInsideTerminal(e: KeyboardEvent): boolean {
  const target = e.target as HTMLElement | null;
  return !!target?.closest(".xterm");
}
