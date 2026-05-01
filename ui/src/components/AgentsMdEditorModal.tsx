import React, { useState, useEffect, useRef, useCallback } from "react";
import type * as Monaco from "monaco-editor";
import { loadMonaco } from "../services/monaco";
import { isDarkModeActive } from "../services/theme";
import { useVimEnabled, useMonacoVim } from "../hooks/useMonacoVim";
import VimToggle from "./VimToggle";

interface AgentsMdEditorModalProps {
  isOpen: boolean;
  onClose: () => void;
}

type SaveStatus = "idle" | "saving" | "saved" | "error";
type LoadStatus = "loading" | "loaded" | "error";

export default function AgentsMdEditorModal({ isOpen, onClose }: AgentsMdEditorModalProps) {
  const initialFilePath = window.__SHELLEY_INIT__?.user_agents_md_path || "";

  const [filePath, setFilePath] = useState(initialFilePath);
  const [content, setContent] = useState<string | null>(null);
  const [loadStatus, setLoadStatus] = useState<LoadStatus>("loading");
  const [monacoLoaded, setMonacoLoaded] = useState(false);
  const [saveStatus, setSaveStatus] = useState<SaveStatus>("idle");

  const [editor, setEditor] = useState<Monaco.editor.IStandaloneCodeEditor | null>(null);
  const editorRef = useRef<Monaco.editor.IStandaloneCodeEditor | null>(null);
  const monacoRef = useRef<typeof Monaco | null>(null);
  const containerRef = useRef<HTMLDivElement>(null);
  const [vimStatusNode, setVimStatusNode] = useState<HTMLDivElement | null>(null);
  const [vimEnabled, setVimEnabled] = useVimEnabled();
  // Vim mode is keyboard-driven; only expose it where there's a real keyboard.
  const [isDesktop, setIsDesktop] = useState(() => window.innerWidth >= 768);
  useEffect(() => {
    const onResize = () => setIsDesktop(window.innerWidth >= 768);
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);
  useMonacoVim(editor, vimStatusNode, isDesktop && vimEnabled);
  const saveTimeoutRef = useRef<number | null>(null);
  const statusTimeoutRef = useRef<number | null>(null);

  // Fetch fresh content from the server each time the modal opens, so that
  // reopening after a prior save doesn't show stale page-load content.
  // If the load fails we deliberately do not populate the editor: typing into
  // an empty buffer would otherwise trigger an auto-save that clobbers the
  // real file on disk, which is the exact failure mode we are trying to fix.
  useEffect(() => {
    if (!isOpen) return;
    let cancelled = false;
    setLoadStatus("loading");
    (async () => {
      try {
        const response = await fetch("/api/user-agents-md");
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const data = (await response.json()) as { path: string; content: string };
        if (cancelled) return;
        if (data.path) setFilePath(data.path);
        setContent(data.content ?? "");
        setLoadStatus("loaded");
      } catch (err) {
        console.error("Failed to load AGENTS.md:", err);
        if (!cancelled) setLoadStatus("error");
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [isOpen]);

  // Load Monaco when modal opens
  useEffect(() => {
    if (isOpen && !monacoLoaded) {
      loadMonaco()
        .then((monaco) => {
          monacoRef.current = monaco;
          setMonacoLoaded(true);
        })
        .catch((err) => {
          console.error("Failed to load Monaco:", err);
        });
    }
  }, [isOpen, monacoLoaded]);

  // Save function
  const saveContent = useCallback(
    async (text: string) => {
      if (!filePath) return;
      if (statusTimeoutRef.current) {
        clearTimeout(statusTimeoutRef.current);
      }
      try {
        setSaveStatus("saving");
        const response = await fetch("/api/write-file", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ path: filePath, content: text }),
        });

        if (response.ok) {
          setSaveStatus("saved");
          statusTimeoutRef.current = window.setTimeout(() => setSaveStatus("idle"), 2000);
        } else {
          setSaveStatus("error");
          statusTimeoutRef.current = window.setTimeout(() => setSaveStatus("idle"), 3000);
        }
      } catch (err) {
        console.error("Failed to save:", err);
        setSaveStatus("error");
        statusTimeoutRef.current = window.setTimeout(() => setSaveStatus("idle"), 3000);
      }
    },
    [filePath],
  );

  // Debounced auto-save
  const scheduleSave = useCallback(
    (text: string) => {
      if (saveTimeoutRef.current) {
        clearTimeout(saveTimeoutRef.current);
      }
      saveTimeoutRef.current = window.setTimeout(() => {
        saveContent(text);
        saveTimeoutRef.current = null;
      }, 1000);
    },
    [saveContent],
  );

  // Create editor when both Monaco and content are ready
  useEffect(() => {
    if (!monacoLoaded || content === null || !containerRef.current || !monacoRef.current) return;

    const monaco = monacoRef.current;

    // Don't re-create if editor already exists
    if (editorRef.current) return;

    const editor = monaco.editor.create(containerRef.current, {
      value: content,
      language: "markdown",
      theme: isDarkModeActive() ? "vs-dark" : "vs",
      minimap: { enabled: false },
      wordWrap: "on",
      lineNumbers: "on",
      scrollBeyondLastLine: false,
      automaticLayout: true,
      fontSize: 14,
      padding: { top: 8 },
    });

    editorRef.current = editor;
    setEditor(editor);

    // Auto-save on content change
    editor.onDidChangeModelContent(() => {
      scheduleSave(editor.getValue());
    });

    // Ctrl+S / Cmd+S to force immediate save
    editor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => {
      if (saveTimeoutRef.current) {
        clearTimeout(saveTimeoutRef.current);
        saveTimeoutRef.current = null;
      }
      saveContent(editor.getValue());
    });

    return () => {
      if (saveTimeoutRef.current) {
        clearTimeout(saveTimeoutRef.current);
        saveTimeoutRef.current = null;
      }
      editor.dispose();
      editorRef.current = null;
      setEditor(null);
    };
  }, [monacoLoaded, content, scheduleSave, saveContent]);

  // Update Monaco theme when dark mode changes
  useEffect(() => {
    if (!monacoRef.current) return;

    const updateTheme = () => {
      const theme = isDarkModeActive() ? "vs-dark" : "vs";
      monacoRef.current?.editor.setTheme(theme);
    };

    const observer = new MutationObserver((mutations) => {
      for (const mutation of mutations) {
        if (mutation.attributeName === "class") {
          updateTheme();
        }
      }
    });

    observer.observe(document.documentElement, { attributes: true });
    return () => observer.disconnect();
  }, [monacoLoaded]);

  // Keyboard: Escape to close
  useEffect(() => {
    if (!isOpen) return;
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        onClose();
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [isOpen, onClose]);

  // Cleanup on close: flush pending save, then reset content
  useEffect(() => {
    if (!isOpen) {
      if (saveTimeoutRef.current && editorRef.current) {
        clearTimeout(saveTimeoutRef.current);
        saveTimeoutRef.current = null;
        saveContent(editorRef.current.getValue());
      }
      if (statusTimeoutRef.current) {
        clearTimeout(statusTimeoutRef.current);
        statusTimeoutRef.current = null;
      }
      setContent(null);
    }
  }, [isOpen, saveContent]);

  if (!isOpen) return null;

  return (
    <div className="diff-viewer-overlay">
      <div className="diff-viewer-container">
        {/* Header */}
        <div className="diff-viewer-header">
          <div className="diff-viewer-header-row">
            <code className="agents-md-header-path">{filePath}</code>
            {saveStatus !== "idle" && (
              <span className={`agents-md-save-status agents-md-save-${saveStatus}`}>
                {saveStatus === "saving" && "Saving..."}
                {saveStatus === "saved" && "Saved"}
                {saveStatus === "error" && "Error saving"}
              </span>
            )}
            {isDesktop && <VimToggle enabled={vimEnabled} onChange={setVimEnabled} />}
            <button className="diff-viewer-close" onClick={onClose} title="Close (Esc)">
              ×
            </button>
          </div>
        </div>

        {/* Editor */}
        <div className="diff-viewer-content">
          {loadStatus === "error" ? (
            <div className="diff-viewer-loading">
              <span>
                Failed to load {filePath || "AGENTS.md"}. Close and reopen to retry; editing is
                disabled to avoid clobbering the file on disk.
              </span>
            </div>
          ) : (
            <>
              {(!monacoLoaded || loadStatus !== "loaded") && (
                <div className="diff-viewer-loading">
                  <div className="spinner"></div>
                  <span>Loading editor...</span>
                </div>
              )}
              <div
                ref={containerRef}
                className="diff-viewer-editor"
                style={{
                  display:
                    monacoLoaded && content !== null && loadStatus === "loaded" ? "block" : "none",
                }}
              />
              {isDesktop && vimEnabled && (
                <div ref={setVimStatusNode} className="monaco-vim-status" />
              )}
            </>
          )}
        </div>
      </div>
    </div>
  );
}
