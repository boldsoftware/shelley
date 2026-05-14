import { useEffect, useState, useCallback } from "react";
import type * as Monaco from "monaco-editor";
import {
  getVimModeEnabled,
  setVimModeEnabled,
  loadMonacoVim,
  ensureVimQuitCommands,
  setVimQuitHandler,
  clearVimQuitHandlerIf,
} from "../services/monaco";

// Tracks the global vim-enabled flag in localStorage so multiple Monaco views
// share state, and updates broadcast to all open editors via a custom event.
const VIM_CHANGE_EVENT = "shelley:monaco-vim-changed";

export function useVimEnabled(): [boolean, (v: boolean) => void] {
  const [enabled, setEnabled] = useState<boolean>(() => getVimModeEnabled());
  useEffect(() => {
    const onChange = () => setEnabled(getVimModeEnabled());
    window.addEventListener(VIM_CHANGE_EVENT, onChange);
    window.addEventListener("storage", onChange);
    return () => {
      window.removeEventListener(VIM_CHANGE_EVENT, onChange);
      window.removeEventListener("storage", onChange);
    };
  }, []);
  const update = useCallback((v: boolean) => {
    setVimModeEnabled(v);
    setEnabled(v);
    window.dispatchEvent(new CustomEvent(VIM_CHANGE_EVENT));
  }, []);
  return [enabled, update];
}

// Attach a monaco-vim adapter to `editor` whenever vim mode is enabled.
// `statusBarNode` is where the vim status bar (mode/command line) renders;
// pass null to skip the status bar entirely. `onQuit` (if provided) is
// invoked when the user runs :q / :wq / :x or presses ZZ / ZQ in normal
// mode; `save` is true for the save+quit variants.
export function useMonacoVim(
  editor: Monaco.editor.IStandaloneCodeEditor | null,
  statusBarNode: HTMLElement | null,
  enabled: boolean,
  onQuit?: (opts: { save: boolean }) => void,
): void {
  useEffect(() => {
    if (!editor || !enabled) return;
    let cancelled = false;
    let adapter: { dispose: () => void } | null = null;
    loadMonacoVim()
      .then(async (mod) => {
        if (cancelled) return;
        await ensureVimQuitCommands();
        if (cancelled) return;
        if (onQuit) setVimQuitHandler(onQuit);
        adapter = mod.initVimMode(editor, statusBarNode ?? undefined);
      })
      .catch((err) => {
        console.error("Failed to load monaco-vim:", err);
      });
    return () => {
      cancelled = true;
      adapter?.dispose();
      if (onQuit) clearVimQuitHandlerIf(onQuit);
      // initVimMode appends children to statusBarNode; clear them on detach
      // so toggling off doesn't leave a stale status line behind.
      if (statusBarNode) statusBarNode.replaceChildren();
    };
  }, [editor, statusBarNode, enabled, onQuit]);
}
