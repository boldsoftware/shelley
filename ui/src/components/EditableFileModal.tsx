import React, { useState, useEffect, useRef, useCallback } from "react";
import { createPortal } from "react-dom";
import { Loader2Icon, XIcon } from "lucide-react";
import type * as Monaco from "monaco-editor";
import { loadMonaco } from "../services/monaco";
import { isDarkModeActive } from "../services/theme";
import { useVimEnabled, useMonacoVim } from "../hooks/useMonacoVim";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
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

  // Quit handler for vim's :q / :wq / :x and ZZ / ZQ. We flush any pending
  // debounced save synchronously when the user asks to save+quit so the
  // modal closes only after the latest content has been persisted.
  const handleVimQuit = useCallback(
    ({ save }: { save: boolean }) => {
      if (save && editorRef.current) {
        if (saveTimeoutRef.current) {
          clearTimeout(saveTimeoutRef.current);
          saveTimeoutRef.current = null;
        }
        saveContent(editorRef.current.getValue());
      }
      onClose();
    },
    [saveContent, onClose],
  );
  useMonacoVim(editor, vimStatusNode, isDesktop && vimEnabled, handleVimQuit);

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
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/30 p-4 supports-backdrop-filter:backdrop-blur-sm"
      role="presentation"
    >
      <div
        className="flex h-[85vh] w-full max-w-5xl flex-col overflow-hidden rounded-lg border border-border bg-popover text-popover-foreground shadow-xl"
        role="dialog"
        aria-modal="true"
        aria-label={title || "Edit file"}
      >
        <div className="flex shrink-0 flex-wrap items-center gap-3 border-b border-border px-4 py-3">
          <span className="font-heading text-base font-medium">{title || "Edit file"}</span>
          <code className="min-w-0 flex-1 truncate font-mono text-xs text-muted-foreground">
            {path}
          </code>
          {saveStatus !== "idle" && (
            <span
              className={cn(
                "text-xs font-medium",
                saveStatus === "saving" && "text-muted-foreground",
                saveStatus === "saved" && "text-emerald-600 dark:text-emerald-500",
                saveStatus === "error" && "text-destructive",
              )}
            >
              {saveStatus === "saving" && "Saving..."}
              {saveStatus === "saved" && "Saved"}
              {saveStatus === "error" && "Error saving"}
            </span>
          )}
          {isDesktop && <VimToggle enabled={vimEnabled} onChange={setVimEnabled} />}
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={onClose}
            title="Close (Esc)"
          >
            <XIcon />
            <span className="sr-only">Close</span>
          </Button>
        </div>
        <div className="relative min-h-0 flex-1">
          {loadStatus === "error" ? (
            <div className="flex h-full items-center justify-center px-4 text-center text-sm text-muted-foreground">
              <span>Failed to load {path}. Editing is disabled.</span>
            </div>
          ) : (
            <>
              {(!monacoLoaded || loadStatus !== "loaded") && (
                <div className="flex h-full items-center justify-center gap-2 text-sm text-muted-foreground">
                  <Loader2Icon className="size-4 animate-spin" />
                  <span>Loading editor...</span>
                </div>
              )}
              <div
                ref={containerRef}
                className="h-full w-full"
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
