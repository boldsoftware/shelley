// Framework-agnostic helpers for the Vue TerminalPanel port. These mirror the
// module-scope helpers in components/TerminalPanel.tsx verbatim.

export function base64ToUint8Array(base64String: string): Uint8Array {
  // @ts-expect-error Uint8Array.fromBase64 is a newer API
  if (Uint8Array.fromBase64) {
    // @ts-expect-error Uint8Array.fromBase64 is a newer API
    return Uint8Array.fromBase64(base64String);
  }
  const binaryString = atob(base64String);
  return Uint8Array.from(binaryString, (char) => char.charCodeAt(0));
}

export type TermStatus = "connecting" | "running" | "exited" | "error";

// Theme colors for xterm.js
export function getTerminalTheme(isDark: boolean): Record<string, string> {
  if (isDark) {
    return {
      background: "#1a1b26",
      foreground: "#c0caf5",
      cursor: "#c0caf5",
      cursorAccent: "#1a1b26",
      selectionBackground: "#364a82",
      selectionForeground: "#c0caf5",
      black: "#32344a",
      red: "#f7768e",
      green: "#9ece6a",
      yellow: "#e0af68",
      blue: "#7aa2f7",
      magenta: "#ad8ee6",
      cyan: "#449dab",
      white: "#9699a8",
      brightBlack: "#444b6a",
      brightRed: "#ff7a93",
      brightGreen: "#b9f27c",
      brightYellow: "#ff9e64",
      brightBlue: "#7da6ff",
      brightMagenta: "#bb9af7",
      brightCyan: "#0db9d7",
      brightWhite: "#acb0d0",
    };
  }
  return {
    background: "#f8f9fa",
    foreground: "#383a42",
    cursor: "#526eff",
    cursorAccent: "#f8f9fa",
    selectionBackground: "#bfceff",
    selectionForeground: "#383a42",
    black: "#383a42",
    red: "#e45649",
    green: "#50a14f",
    yellow: "#c18401",
    blue: "#4078f2",
    magenta: "#a626a4",
    cyan: "#0184bc",
    white: "#a0a1a7",
    brightBlack: "#4f525e",
    brightRed: "#e06c75",
    brightGreen: "#98c379",
    brightYellow: "#e5c07b",
    brightBlue: "#61afef",
    brightMagenta: "#c678dd",
    brightCyan: "#56b6c2",
    brightWhite: "#ffffff",
  };
}
