// Vue port of hooks/useMonacoVim.ts. Reuses the framework-agnostic Monaco vim
// helpers in services/monaco.ts; only the React state/effect glue is replaced.
import { ref, watch, onUnmounted, type Ref } from "vue";
import type * as Monaco from "monaco-editor";
import {
  getVimModeEnabled,
  setVimModeEnabled,
  loadMonacoVim,
  ensureVimQuitCommands,
  setVimQuitHandler,
  clearVimQuitHandlerIf,
} from "../../services/monaco";

const VIM_CHANGE_EVENT = "shelley:monaco-vim-changed";

// Shared, app-wide reactive vim-enabled flag (mirrors the localStorage value
// and the cross-editor custom event used by the React hook).
const enabledRef = ref<boolean>(getVimModeEnabled());
let wired = false;
function ensureWired() {
  if (wired) return;
  wired = true;
  const onChange = () => {
    enabledRef.value = getVimModeEnabled();
  };
  window.addEventListener(VIM_CHANGE_EVENT, onChange);
  window.addEventListener("storage", onChange);
}

export function useVimEnabled(): [Ref<boolean>, (v: boolean) => void] {
  ensureWired();
  const update = (v: boolean) => {
    setVimModeEnabled(v);
    enabledRef.value = v;
    window.dispatchEvent(new CustomEvent(VIM_CHANGE_EVENT));
  };
  return [enabledRef, update];
}

// Attach a monaco-vim adapter to `editor` whenever vim mode is enabled.
// Pass reactive getters for editor/statusBar/enabled. `onQuit` (if provided)
// fires on :q/:wq/:x/ZZ/ZQ with { save }.
export function useMonacoVim(
  getEditor: () => Monaco.editor.IStandaloneCodeEditor | null,
  getStatusBar: () => HTMLElement | null,
  getEnabled: () => boolean,
  onQuit?: (opts: { save: boolean }) => void,
): void {
  let adapter: { dispose: () => void } | null = null;
  let cancelled = false;

  const detach = () => {
    cancelled = true;
    adapter?.dispose();
    adapter = null;
    if (onQuit) clearVimQuitHandlerIf(onQuit);
    const sb = getStatusBar();
    if (sb) sb.replaceChildren();
  };

  const attach = () => {
    const editor = getEditor();
    const statusBarNode = getStatusBar();
    if (!editor || !getEnabled()) return;
    cancelled = false;
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
  };

  watch(
    [getEditor, getStatusBar, getEnabled],
    () => {
      detach();
      cancelled = false;
      attach();
    },
    { immediate: true },
  );

  onUnmounted(detach);
}
