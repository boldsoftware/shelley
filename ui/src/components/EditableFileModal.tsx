import React, { useState, useEffect, useRef, useCallback } from "react";
import { createPortal } from "react-dom";
import type * as Monaco from "monaco-editor";
import { loadMonaco } from "../services/monaco";
import { isDarkModeActive } from "../services/theme";
import { useVimEnabled, useMonacoVim } from "../hooks/useMonacoVim";
import VimToggle from "./VimToggle";

interface EditableFileModalProps {
  isOpen: boolean;
  path: string;
  title?: string;
  language?: string;
  loadUrl?: string;
  onClose: () => void;
  onSaved?: (content: string) => void;
}

type SaveStatus = "idle" | "saving" | "saved" | "error";
type LoadStatus = "loading" | "loaded" | "error";

export default function EditableFileModal({
  isOpen,
  path,
  title,
  language = "markdown",
  loadUrl,
  onClose,
  onSaved,
}: EditableFileModalProps) {
  const [content, setContent] = useState<string | null>(null);
  const [loadStatus, setLoadStatus] = useState<LoadStatus>("loading");
  const [monacoLoaded, setMonacoLoaded] = useState(false);
  const [saveStatus, setSaveStatus] = useState<SaveStatus>("idle");

  const [editor, setEditor] = useState<Monaco.editor.IStandaloneCodeEditor | null>(null);
  const editorRef = useRef<Monaco.editor.IStandaloneCodeEditor | null>(null);
  const monacoRef = useRef<typeof Monaco | null>(null);
  const containerRef = useRef<HTMLDivElement>(null);
  const saveTimeoutRef = useRef<number | null>(null);
  const statusTimeoutRef = useRef<number | null>(null);
  const [vimStatusNode, setVimStatusNode] = useState<HTMLDivElement | null>(null);
  const [vimEnabled, setVimEnabled] = useVimEnabled();
  const [isDesktop, setIsDesktop] = useState(() => window.innerWidth >= 768);

  useEffect(() => {
    const onResize = () => setIsDesktop(window.innerWidth >= 768);
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);
  useMonacoVim(editor, vimStatusNode, isDesktop && vimEnabled);

  useEffect(() => {
    if (!isOpen) return;
    let cancelled = false;
    setLoadStatus("loading");
    (async () => {
      try {
        const response = await fetch(loadUrl || `/api/read?path=${encodeURIComponent(path)}`);
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const text = loadUrl
          ? ((await response.json()) as { content: string }).content
          : await response.text();
        if (cancelled) return;
        setContent(text ?? "");
        setLoadStatus("loaded");
      } catch (err) {
        console.error("Failed to load editable file:", err);
        if (!cancelled) setLoadStatus("error");
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [isOpen, path, loadUrl]);

  useEffect(() => {
    if (isOpen && !monacoLoaded) {
      loadMonaco()
        .then((monaco) => {
          monacoRef.current = monaco;
          setMonacoLoaded(true);
        })
        .catch((err) => console.error("Failed to load Monaco:", err));
    }
  }, [isOpen, monacoLoaded]);

  const saveContent = useCallback(
    async (text: string) => {
      if (!path) return;
      if (statusTimeoutRef.current) clearTimeout(statusTimeoutRef.current);
      try {
        setSaveStatus("saving");
        const response = await fetch("/api/write-file", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ path, content: text }),
        });
        if (response.ok) {
          onSaved?.(text);
        }
        setSaveStatus(response.ok ? "saved" : "error");
        statusTimeoutRef.current = window.setTimeout(
          () => setSaveStatus("idle"),
          response.ok ? 2000 : 3000,
        );
      } catch (err) {
        console.error("Failed to save:", err);
        setSaveStatus("error");
        statusTimeoutRef.current = window.setTimeout(() => setSaveStatus("idle"), 3000);
      }
    },
    [path, onSaved],
  );

  const scheduleSave = useCallback(
    (text: string) => {
      if (saveTimeoutRef.current) clearTimeout(saveTimeoutRef.current);
      saveTimeoutRef.current = window.setTimeout(() => {
        saveContent(text);
        saveTimeoutRef.current = null;
      }, 1000);
    },
    [saveContent],
  );

  useEffect(() => {
    if (!monacoLoaded || content === null || !containerRef.current || !monacoRef.current) return;
    if (editorRef.current) return;

    const monaco = monacoRef.current;
    const nextEditor = monaco.editor.create(containerRef.current, {
      value: content,
      language,
      theme: isDarkModeActive() ? "vs-dark" : "vs",
      minimap: { enabled: false },
      wordWrap: "on",
      lineNumbers: "on",
      scrollBeyondLastLine: false,
      automaticLayout: true,
      fontSize: 14,
      padding: { top: 8 },
    });
    editorRef.current = nextEditor;
    setEditor(nextEditor);

    nextEditor.onDidChangeModelContent(() => scheduleSave(nextEditor.getValue()));
    nextEditor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => {
      if (saveTimeoutRef.current) {
        clearTimeout(saveTimeoutRef.current);
        saveTimeoutRef.current = null;
      }
      saveContent(nextEditor.getValue());
    });

    return () => {
      if (saveTimeoutRef.current) {
        clearTimeout(saveTimeoutRef.current);
        saveTimeoutRef.current = null;
      }
      nextEditor.dispose();
      editorRef.current = null;
      setEditor(null);
    };
  }, [monacoLoaded, content, language, scheduleSave, saveContent]);

  useEffect(() => {
    if (!monacoRef.current) return;
    const updateTheme = () =>
      monacoRef.current?.editor.setTheme(isDarkModeActive() ? "vs-dark" : "vs");
    const observer = new MutationObserver((mutations) => {
      for (const mutation of mutations) if (mutation.attributeName === "class") updateTheme();
    });
    observer.observe(document.documentElement, { attributes: true });
    return () => observer.disconnect();
  }, [monacoLoaded]);

  useEffect(() => {
    if (!isOpen) return;
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      // If vim mode is in a non-normal mode (insert/visual/...), let
      // monaco-vim handle Escape (to drop back to normal) instead of
      // closing the modal. Normal mode leaves the status node empty, so a
      // second Esc still closes the modal as users expect. Vim only
      // attaches on desktop, so skip the guard on mobile.
      const vimFocused =
        containerRef.current?.contains(document.activeElement) ||
        vimStatusNode?.contains(document.activeElement);
      if (
        isDesktop &&
        vimEnabled &&
        vimFocused &&
        (vimStatusNode?.textContent ?? "").trim() !== ""
      ) {
        return;
      }
      onClose();
    };
    // Capture phase so we read the vim status *before* monaco-vim handles
    // the Escape and clears it (insert -> normal flips status to empty
    // synchronously, which would otherwise defeat our guard).
    window.addEventListener("keydown", handleKeyDown, true);
    return () => window.removeEventListener("keydown", handleKeyDown, true);
  }, [isOpen, onClose, vimEnabled, isDesktop, vimStatusNode]);

  useEffect(() => {
    if (isOpen) return;
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
  }, [isOpen, saveContent]);

  if (!isOpen) return null;

  return createPortal(
    <div className="diff-viewer-overlay" role="presentation">
      <div
        className="diff-viewer-container"
        role="dialog"
        aria-modal="true"
        aria-label={title || "Edit file"}
      >
        <div className="diff-viewer-header">
          <div className="diff-viewer-header-row">
            <span className="agents-md-header-title">{title || "Edit file"}</span>
            <code className="agents-md-header-path">{path}</code>
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
        <div className="diff-viewer-content">
          {loadStatus === "error" ? (
            <div className="diff-viewer-loading">
              <span>Failed to load {path}. Editing is disabled.</span>
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
    </div>,
    document.body,
  );
}
