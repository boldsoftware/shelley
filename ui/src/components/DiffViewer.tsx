import React, { useState, useEffect, useCallback, useRef } from "react";
import type * as Monaco from "monaco-editor";
import { api } from "../services/api";
import { isDarkModeActive } from "../services/theme";
import { GitDiffInfo, GitFileInfo, GitFileDiff } from "../types";

interface DiffViewerProps {
  cwd: string;
  isOpen: boolean;
  onClose: () => void;
  onCommentTextChange: (text: string) => void;
  initialCommit?: string; // If set, select this commit when opening
}

// Icon components for cleaner JSX
const PrevFileIcon = () => (
  <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
    <path d="M8 2L2 8l6 6V2z" />
    <path d="M14 2L8 8l6 6V2z" />
  </svg>
);

const PrevChangeIcon = () => (
  <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
    <path d="M10 2L4 8l6 6V2z" />
  </svg>
);

const NextChangeIcon = () => (
  <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
    <path d="M6 2l6 6-6 6V2z" />
  </svg>
);

const NextFileIcon = () => (
  <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
    <path d="M2 2l6 6-6 6V2z" />
    <path d="M8 2l6 6-6 6V2z" />
  </svg>
);

// Global Monaco instance - loaded lazily
let monacoInstance: typeof Monaco | null = null;
let monacoLoadPromise: Promise<typeof Monaco> | null = null;

function loadMonaco(): Promise<typeof Monaco> {
  if (monacoInstance) {
    return Promise.resolve(monacoInstance);
  }
  if (monacoLoadPromise) {
    return monacoLoadPromise;
  }

  monacoLoadPromise = (async () => {
    // Configure Monaco environment for web workers before importing
    const monacoEnv: Monaco.Environment = {
      getWorkerUrl: () => "/editor.worker.js",
    };
    (self as Window).MonacoEnvironment = monacoEnv;

    // Load Monaco CSS if not already loaded
    if (!document.querySelector('link[href="/monaco-editor.css"]')) {
      const link = document.createElement("link");
      link.rel = "stylesheet";
      link.href = "/monaco-editor.css";
      document.head.appendChild(link);
    }

    // Load Monaco from our local bundle (runtime URL, cast to proper types)
    // eslint-disable-next-line @typescript-eslint/ban-ts-comment
    // @ts-ignore - dynamic runtime URL import
    const monaco = (await import("/monaco-editor.js")) as typeof Monaco;
    monacoInstance = monaco;
    return monacoInstance;
  })();

  return monacoLoadPromise;
}

type ViewMode = "comment" | "edit";

function DiffViewer({ cwd, isOpen, onClose, onCommentTextChange, initialCommit }: DiffViewerProps) {
  const [diffs, setDiffs] = useState<GitDiffInfo[]>([]);
  const [gitRoot, setGitRoot] = useState<string | null>(null);
  const [selectedDiff, setSelectedDiff] = useState<string | null>(null);
  const [files, setFiles] = useState<GitFileInfo[]>([]);
  const [selectedFile, setSelectedFile] = useState<string | null>(null);
  const [fileDiff, setFileDiff] = useState<GitFileDiff | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [monacoLoaded, setMonacoLoaded] = useState(false);
  const [currentChangeIndex, setCurrentChangeIndex] = useState<number>(-1);
  const [saveStatus, setSaveStatus] = useState<"idle" | "saving" | "saved" | "error">("idle");
  const saveTimeoutRef = useRef<number | null>(null);
  const pendingSaveRef = useRef<(() => Promise<void>) | null>(null);
  const scheduleSaveRef = useRef<(() => void) | null>(null);
  const contentChangeDisposableRef = useRef<Monaco.IDisposable | null>(null);
  const [showCommentDialog, setShowCommentDialog] = useState<{
    line: number;
    side: "left" | "right";
    selectedText?: string;
    startLine?: number;
    endLine?: number;
  } | null>(null);
  const [commentText, setCommentText] = useState("");
  const [mode, setMode] = useState<ViewMode>("comment");
  const [showKeyboardHint, setShowKeyboardHint] = useState(false);
  const hasShownKeyboardHint = useRef(false);

  const [isMobile, setIsMobile] = useState(window.innerWidth < 768);
  const editorContainerRef = useRef<HTMLDivElement>(null);
  const editorRef = useRef<Monaco.editor.IStandaloneDiffEditor | null>(null);
  const monacoRef = useRef<typeof Monaco | null>(null);
  const commentInputRef = useRef<HTMLTextAreaElement>(null);
  const modeRef = useRef<ViewMode>(mode);
  const hoverDecorationsRef = useRef<string[]>([]);

  // Keep modeRef in sync with mode state and update editor options
  useEffect(() => {
    modeRef.current = mode;
    // Update editor readOnly state when mode changes
    if (editorRef.current) {
      const modifiedEditor = editorRef.current.getModifiedEditor();
      modifiedEditor.updateOptions({ readOnly: mode === "comment" });
    }
  }, [mode]);

  // Track viewport size
  useEffect(() => {
    const handleResize = () => {
      setIsMobile(window.innerWidth < 768);
    };
    window.addEventListener("resize", handleResize);
    return () => window.removeEventListener("resize", handleResize);
  }, []);

  // Focus comment input when dialog opens
  useEffect(() => {
    if (showCommentDialog && commentInputRef.current) {
      // Small delay to ensure the dialog is rendered
      setTimeout(() => {
        commentInputRef.current?.focus();
      }, 50);
    }
  }, [showCommentDialog]);

  // Load Monaco when viewer opens
  useEffect(() => {
    if (isOpen && !monacoLoaded) {
      loadMonaco()
        .then((monaco) => {
          monacoRef.current = monaco;
          setMonacoLoaded(true);
        })
        .catch((err) => {
          console.error("Failed to load Monaco:", err);
          setError("Failed to load diff editor");
        });
    }
  }, [isOpen, monacoLoaded]);

  // Show keyboard hint toast on first open (desktop only)
  useEffect(() => {
    if (isOpen && !isMobile && !hasShownKeyboardHint.current && fileDiff) {
      hasShownKeyboardHint.current = true;
      setShowKeyboardHint(true);
      const timer = setTimeout(() => setShowKeyboardHint(false), 6000);
      return () => clearTimeout(timer);
    }
  }, [isOpen, isMobile, fileDiff]);

  // Load diffs when viewer opens, reset state when it closes
  useEffect(() => {
    if (isOpen && cwd) {
      loadDiffs();
    } else if (!isOpen) {
      // Reset state when closing
      setFileDiff(null);
      setSelectedFile(null);
      setFiles([]);
      setSelectedDiff(null);
      setDiffs([]);
      setError(null);
      setShowCommentDialog(null);
      setCommentText("");
      // Dispose editor when closing
      if (editorRef.current) {
        editorRef.current.dispose();
        editorRef.current = null;
      }
    }
  }, [isOpen, cwd, initialCommit]);

  // Load files when diff is selected
  useEffect(() => {
    if (selectedDiff && cwd) {
      loadFiles(selectedDiff);
    }
  }, [selectedDiff, cwd]);

  // Load file diff when file is selected
  useEffect(() => {
    if (selectedDiff && selectedFile && cwd) {
      loadFileDiff(selectedDiff, selectedFile);
      setCurrentChangeIndex(-1); // Reset change index for new file
    }
  }, [selectedDiff, selectedFile, cwd]);

  // Create/update Monaco editor when fileDiff changes
  useEffect(() => {
    if (!monacoLoaded || !fileDiff || !editorContainerRef.current || !monacoRef.current) {
      return;
    }

    const monaco = monacoRef.current;

    // Dispose previous editor
    if (editorRef.current) {
      editorRef.current.dispose();
      editorRef.current = null;
    }

    // Get language from file extension
    const ext = "." + (fileDiff.path.split(".").pop()?.toLowerCase() || "");
    const languages = monaco.languages.getLanguages();
    let language = "plaintext";
    for (const lang of languages) {
      if (lang.extensions?.includes(ext)) {
        language = lang.id;
        break;
      }
    }

    // Create models with unique URIs (include timestamp to avoid conflicts)
    const timestamp = Date.now();
    const originalUri = monaco.Uri.file(`original-${timestamp}-${fileDiff.path}`);
    const modifiedUri = monaco.Uri.file(`modified-${timestamp}-${fileDiff.path}`);

    const originalModel = monaco.editor.createModel(fileDiff.oldContent, language, originalUri);
    const modifiedModel = monaco.editor.createModel(fileDiff.newContent, language, modifiedUri);

    // Create diff editor with mobile-friendly options
    const diffEditor = monaco.editor.createDiffEditor(editorContainerRef.current, {
      theme: isDarkModeActive() ? "vs-dark" : "vs",
      readOnly: true, // Always read-only in diff viewer
      originalEditable: false,
      automaticLayout: true,
      renderSideBySide: !isMobile,
      enableSplitViewResizing: true,
      renderIndicators: true,
      renderMarginRevertIcon: false,
      lineNumbers: isMobile ? "off" : "on",
      minimap: { enabled: false },
      scrollBeyondLastLine: true, // Enable scroll past end for mobile floating buttons
      wordWrap: "on",
      glyphMargin: !isMobile, // Enable glyph margin for comment indicator on hover
      lineDecorationsWidth: isMobile ? 0 : 10,
      lineNumbersMinChars: isMobile ? 0 : 3,
      quickSuggestions: false,
      suggestOnTriggerCharacters: false,
      lightbulb: { enabled: false },
      codeLens: false,
      contextmenu: false,
      links: false,
      folding: !isMobile,
      padding: isMobile ? { bottom: 80 } : undefined, // Extra padding for floating buttons on mobile
    });

    diffEditor.setModel({
      original: originalModel,
      modified: modifiedModel,
    });

    editorRef.current = diffEditor;

    // Auto-scroll to first diff after editor is ready
    // Use setTimeout to allow Monaco to compute the diff
    setTimeout(() => {
      const changes = diffEditor.getLineChanges();
      if (changes && changes.length > 0) {
        const firstChange = changes[0];
        const targetLine = firstChange.modifiedStartLineNumber || 1;
        const editor = diffEditor.getModifiedEditor();
        editor.revealLineInCenter(targetLine);
        editor.setPosition({ lineNumber: targetLine, column: 1 });
        setCurrentChangeIndex(0);
      }
    }, 100);

    // Add click handler for commenting - clicking on a line in comment mode opens dialog
    const modifiedEditor = diffEditor.getModifiedEditor();

    // Handler function for opening comment dialog
    const openCommentDialog = (lineNumber: number) => {
      const model = modifiedEditor.getModel();
      const selection = modifiedEditor.getSelection();
      let selectedText = "";
      let startLine = lineNumber;
      let endLine = lineNumber;

      if (selection && !selection.isEmpty() && model) {
        selectedText = model.getValueInRange(selection);
        startLine = selection.startLineNumber;
        endLine = selection.endLineNumber;
      } else if (model) {
        selectedText = model.getLineContent(lineNumber) || "";
      }

      setShowCommentDialog({
        line: startLine,
        side: "right",
        selectedText,
        startLine,
        endLine,
      });
    };

    modifiedEditor.onMouseDown((e: Monaco.editor.IEditorMouseEvent) => {
      // In comment mode, clicking on line content opens comment dialog
      const isLineClick =
        e.target.type === monaco.editor.MouseTargetType.CONTENT_TEXT ||
        e.target.type === monaco.editor.MouseTargetType.CONTENT_EMPTY;

      if (isLineClick && modeRef.current === "comment") {
        const position = e.target.position;
        if (position) {
          openCommentDialog(position.lineNumber);
        }
      }
    });

    // For mobile: use onMouseUp which fires more reliably on touch devices
    if (isMobile) {
      modifiedEditor.onMouseUp((e: Monaco.editor.IEditorMouseEvent) => {
        if (modeRef.current !== "comment") return;

        const isLineClick =
          e.target.type === monaco.editor.MouseTargetType.CONTENT_TEXT ||
          e.target.type === monaco.editor.MouseTargetType.CONTENT_EMPTY;

        if (isLineClick) {
          const position = e.target.position;
          if (position) {
            openCommentDialog(position.lineNumber);
          }
        }
      });
    }

    // Add hover highlighting with comment indicator (comment mode only)
    let lastHoveredLine = -1;
    modifiedEditor.onMouseMove((e: Monaco.editor.IEditorMouseEvent) => {
      // Only show hover effects in comment mode
      if (modeRef.current !== "comment") {
        if (hoverDecorationsRef.current.length > 0) {
          hoverDecorationsRef.current = modifiedEditor.deltaDecorations(
            hoverDecorationsRef.current,
            [],
          );
        }
        return;
      }

      const position = e.target.position;
      const lineNumber = position?.lineNumber ?? -1;

      if (lineNumber === lastHoveredLine) return;
      lastHoveredLine = lineNumber;

      if (lineNumber > 0) {
        hoverDecorationsRef.current = modifiedEditor.deltaDecorations(hoverDecorationsRef.current, [
          {
            range: new monaco.Range(lineNumber, 1, lineNumber, 1),
            options: {
              isWholeLine: true,
              className: "diff-viewer-line-hover",
              glyphMarginClassName: "diff-viewer-comment-glyph",
            },
          },
        ]);
      } else {
        hoverDecorationsRef.current = modifiedEditor.deltaDecorations(
          hoverDecorationsRef.current,
          [],
        );
      }
    });

    // Clear decorations when mouse leaves editor
    modifiedEditor.onMouseLeave(() => {
      lastHoveredLine = -1;
      hoverDecorationsRef.current = modifiedEditor.deltaDecorations(
        hoverDecorationsRef.current,
        [],
      );
    });

    // Add content change listener for auto-save
    contentChangeDisposableRef.current?.dispose();
    contentChangeDisposableRef.current = modifiedEditor.onDidChangeModelContent(() => {
      scheduleSaveRef.current?.();
    });

    // Cleanup function
    return () => {
      contentChangeDisposableRef.current?.dispose();
      contentChangeDisposableRef.current = null;
      if (editorRef.current) {
        editorRef.current.dispose();
        editorRef.current = null;
      }
    };
  }, [monacoLoaded, fileDiff, isMobile]);

  const loadDiffs = async () => {
    try {
      setLoading(true);
      setError(null);
      const response = await api.getGitDiffs(cwd);
      setDiffs(response.diffs);
      setGitRoot(response.gitRoot);

      // If initialCommit is set, try to select that commit
      if (initialCommit) {
        const matchingDiff = response.diffs.find(
          (d) => d.id === initialCommit || d.id.startsWith(initialCommit),
        );
        if (matchingDiff) {
          setSelectedDiff(matchingDiff.id);
          return;
        }
      }

      // Auto-select working changes if non-empty
      if (response.diffs.length > 0) {
        const working = response.diffs.find((d) => d.id === "working");
        if (working && working.filesCount > 0) {
          setSelectedDiff("working");
        } else if (response.diffs.length > 1) {
          setSelectedDiff(response.diffs[1].id);
        }
      }
    } catch (err) {
      const errStr = String(err);
      if (errStr.toLowerCase().includes("not a git repository")) {
        setError(`Not a git repository: ${cwd}`);
      } else {
        setError(`Failed to load diffs: ${errStr}`);
      }
    } finally {
      setLoading(false);
    }
  };

  const loadFiles = async (diffId: string) => {
    try {
      setLoading(true);
      setError(null);
      const filesData = await api.getGitDiffFiles(diffId, cwd);
      setFiles(filesData || []);
      if (filesData && filesData.length > 0) {
        setSelectedFile(filesData[0].path);
      } else {
        setSelectedFile(null);
        setFileDiff(null);
      }
    } catch (err) {
      setError(`Failed to load files: ${err}`);
    } finally {
      setLoading(false);
    }
  };

  const loadFileDiff = async (diffId: string, filePath: string) => {
    try {
      setLoading(true);
      setError(null);
      const diffData = await api.getGitFileDiff(diffId, filePath, cwd);
      setFileDiff(diffData);
    } catch (err) {
      setError(`Failed to load file diff: ${err}`);
    } finally {
      setLoading(false);
    }
  };

  const handleAddComment = () => {
    if (!showCommentDialog || !commentText.trim() || !selectedFile) return;

    // Format: > filename:123: code
    // Comment...
    const line = showCommentDialog.line;
    const codeSnippet = showCommentDialog.selectedText?.split("\n")[0]?.trim() || "";
    const truncatedCode =
      codeSnippet.length > 60 ? codeSnippet.substring(0, 57) + "..." : codeSnippet;

    const commentBlock = `> ${selectedFile}:${line}: ${truncatedCode}\n${commentText}\n\n`;

    onCommentTextChange(commentBlock);
    setShowCommentDialog(null);
    setCommentText("");
  };

  const goToNextFile = useCallback(() => {
    if (files.length === 0 || !selectedFile) return false;
    const idx = files.findIndex((f) => f.path === selectedFile);
    if (idx < files.length - 1) {
      setSelectedFile(files[idx + 1].path);
      setCurrentChangeIndex(-1); // Reset to start of new file
      return true;
    }
    return false;
  }, [files, selectedFile]);

  const goToPreviousFile = useCallback(() => {
    if (files.length === 0 || !selectedFile) return false;
    const idx = files.findIndex((f) => f.path === selectedFile);
    if (idx > 0) {
      setSelectedFile(files[idx - 1].path);
      setCurrentChangeIndex(-1); // Will go to last change when file loads
      return true;
    }
    return false;
  }, [files, selectedFile]);

  const goToNextChange = useCallback(() => {
    if (!editorRef.current) return;
    const changes = editorRef.current.getLineChanges();
    if (!changes || changes.length === 0) {
      // No changes in this file, try next file
      goToNextFile();
      return;
    }

    const modifiedEditor = editorRef.current.getModifiedEditor();
    const visibleRanges = modifiedEditor.getVisibleRanges();
    const viewBottom = visibleRanges.length > 0 ? visibleRanges[0].endLineNumber : 0;

    // Find the next change that starts below the current view
    // This ensures we always move "down" and never scroll up
    let nextIdx = -1;
    for (let i = 0; i < changes.length; i++) {
      const changeLine = changes[i].modifiedStartLineNumber || 1;
      if (changeLine > viewBottom) {
        nextIdx = i;
        break;
      }
    }

    if (nextIdx === -1) {
      // No more changes below current view, try to go to next file
      if (goToNextFile()) {
        return;
      }
      // No next file, stay where we are
      return;
    }

    const change = changes[nextIdx];
    const targetLine = change.modifiedStartLineNumber || 1;
    modifiedEditor.revealLineInCenter(targetLine);
    modifiedEditor.setPosition({ lineNumber: targetLine, column: 1 });
    setCurrentChangeIndex(nextIdx);
  }, [goToNextFile]);

  const goToPreviousChange = useCallback(() => {
    if (!editorRef.current) return;
    const changes = editorRef.current.getLineChanges();
    if (!changes || changes.length === 0) {
      // No changes in this file, try previous file
      goToPreviousFile();
      return;
    }

    const modifiedEditor = editorRef.current.getModifiedEditor();
    const prevIdx = currentChangeIndex <= 0 ? -1 : currentChangeIndex - 1;

    if (prevIdx < 0) {
      // At start of file, try to go to previous file
      if (goToPreviousFile()) {
        return;
      }
      // No previous file, go to first change
      const change = changes[0];
      const targetLine = change.modifiedStartLineNumber || 1;
      modifiedEditor.revealLineInCenter(targetLine);
      modifiedEditor.setPosition({ lineNumber: targetLine, column: 1 });
      setCurrentChangeIndex(0);
      return;
    }

    const change = changes[prevIdx];
    const targetLine = change.modifiedStartLineNumber || 1;
    modifiedEditor.revealLineInCenter(targetLine);
    modifiedEditor.setPosition({ lineNumber: targetLine, column: 1 });
    setCurrentChangeIndex(prevIdx);
  }, [currentChangeIndex, goToPreviousFile]);

  // Save the current file (in edit mode)
  const saveCurrentFile = useCallback(async () => {
    if (
      !editorRef.current ||
      !selectedFile ||
      !fileDiff ||
      modeRef.current !== "edit" ||
      !gitRoot
    ) {
      return;
    }

    const modifiedEditor = editorRef.current.getModifiedEditor();
    const model = modifiedEditor.getModel();
    if (!model) return;

    const content = model.getValue();
    const fullPath = gitRoot + "/" + selectedFile;

    try {
      setSaveStatus("saving");
      const response = await fetch("/api/write-file", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ path: fullPath, content }),
      });

      if (response.ok) {
        setSaveStatus("saved");
        setTimeout(() => setSaveStatus("idle"), 2000);
      } else {
        setSaveStatus("error");
        setTimeout(() => setSaveStatus("idle"), 3000);
      }
    } catch (err) {
      console.error("Failed to save:", err);
      setSaveStatus("error");
      setTimeout(() => setSaveStatus("idle"), 3000);
    }
  }, [selectedFile, fileDiff, gitRoot]);

  // Debounced auto-save
  const scheduleSave = useCallback(() => {
    if (modeRef.current !== "edit") return; // Only auto-save in edit mode
    if (saveTimeoutRef.current) {
      clearTimeout(saveTimeoutRef.current);
    }
    pendingSaveRef.current = saveCurrentFile;
    saveTimeoutRef.current = window.setTimeout(() => {
      pendingSaveRef.current?.();
      pendingSaveRef.current = null;
      saveTimeoutRef.current = null;
    }, 1000);
  }, [saveCurrentFile]);

  // Keep scheduleSaveRef in sync
  useEffect(() => {
    scheduleSaveRef.current = scheduleSave;
  }, [scheduleSave]);

  // Force immediate save (for Ctrl+S)
  const saveImmediately = useCallback(() => {
    if (saveTimeoutRef.current) {
      clearTimeout(saveTimeoutRef.current);
      saveTimeoutRef.current = null;
    }
    pendingSaveRef.current = null;
    saveCurrentFile();
  }, [saveCurrentFile]);

  // Update Monaco theme when dark mode changes
  useEffect(() => {
    if (!monacoRef.current) return;

    const updateMonacoTheme = () => {
      const theme = isDarkModeActive() ? "vs-dark" : "vs";
      monacoRef.current?.editor.setTheme(theme);
    };

    // Watch for changes to the dark class on documentElement
    const observer = new MutationObserver((mutations) => {
      for (const mutation of mutations) {
        if (mutation.attributeName === "class") {
          updateMonacoTheme();
        }
      }
    });

    observer.observe(document.documentElement, { attributes: true });

    return () => observer.disconnect();
  }, [monacoLoaded]);

  // Keyboard shortcuts
  useEffect(() => {
    if (!isOpen) return;

    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        if (showCommentDialog) {
          setShowCommentDialog(null);
        } else {
          onClose();
        }
        return;
      }
      if ((e.ctrlKey || e.metaKey) && e.key === "s") {
        e.preventDefault();
        saveImmediately();
        return;
      }

      // Intercept PageUp/PageDown to scroll the diff editor instead of background
      if (e.key === "PageUp" || e.key === "PageDown") {
        if (editorRef.current) {
          e.preventDefault();
          e.stopPropagation();
          const modifiedEditor = editorRef.current.getModifiedEditor();
          // Trigger the editor's built-in page up/down action
          modifiedEditor.trigger(
            "keyboard",
            e.key === "PageUp" ? "cursorPageUp" : "cursorPageDown",
            null,
          );
        }
        return;
      }

      // Comment mode navigation shortcuts (only when comment dialog is closed)
      if (mode === "comment" && !showCommentDialog) {
        if (e.key === ".") {
          e.preventDefault();
          goToNextChange();
          return;
        } else if (e.key === ",") {
          e.preventDefault();
          goToPreviousChange();
          return;
        } else if (e.key === ">") {
          e.preventDefault();
          goToNextFile();
          return;
        } else if (e.key === "<") {
          e.preventDefault();
          goToPreviousFile();
          return;
        }
      }

      if (!e.ctrlKey) return;
      if (e.key === "j") {
        e.preventDefault();
        goToNextFile();
      } else if (e.key === "k") {
        e.preventDefault();
        goToPreviousFile();
      }
    };

    // Use capture phase to intercept events before Monaco editor handles them
    window.addEventListener("keydown", handleKeyDown, true);
    return () => window.removeEventListener("keydown", handleKeyDown, true);
  }, [
    isOpen,
    goToNextFile,
    goToPreviousFile,
    goToNextChange,
    goToPreviousChange,
    showCommentDialog,
    onClose,
    saveImmediately,
    mode,
  ]);

  if (!isOpen) return null;

  const getStatusSymbol = (status: string) => {
    switch (status) {
      case "added":
        return "+";
      case "deleted":
        return "-";
      case "modified":
        return "~";
      default:
        return "";
    }
  };

  const currentFileIndex = files.findIndex((f) => f.path === selectedFile);
  const hasNextFile = currentFileIndex < files.length - 1;
  const hasPrevFile = currentFileIndex > 0;

  // Selectors shared between desktop and mobile
  const commitSelector = (
    <select
      value={selectedDiff || ""}
      onChange={(e) => setSelectedDiff(e.target.value || null)}
      className="diff-viewer-select"
    >
      <option value="">Choose base...</option>
      {diffs.map((diff) => {
        const stats = `${diff.filesCount} files, +${diff.additions}/-${diff.deletions}`;
        return (
          <option key={diff.id} value={diff.id}>
            {diff.id === "working"
              ? `Working Changes (${stats})`
              : `${diff.message.slice(0, 40)} (${stats})`}
          </option>
        );
      })}
    </select>
  );

  const fileIndexIndicator =
    files.length > 1 && currentFileIndex >= 0 ? `(${currentFileIndex + 1}/${files.length})` : null;

  const fileSelector = (
    <div className="diff-viewer-file-selector-wrapper">
      <select
        value={selectedFile || ""}
        onChange={(e) => setSelectedFile(e.target.value || null)}
        className="diff-viewer-select"
        disabled={files.length === 0}
      >
        <option value="">{files.length === 0 ? "No files" : "Choose file..."}</option>
        {files.map((file) => (
          <option key={file.path} value={file.path}>
            {getStatusSymbol(file.status)} {file.path}
            {file.additions > 0 && ` (+${file.additions})`}
            {file.deletions > 0 && ` (-${file.deletions})`}
          </option>
        ))}
      </select>
      {fileIndexIndicator && <span className="diff-viewer-file-index">{fileIndexIndicator}</span>}
    </div>
  );

  const modeToggle = (
    <div className="diff-viewer-mode-toggle">
      <button
        className={`diff-viewer-mode-btn ${mode === "comment" ? "active" : ""}`}
        onClick={() => setMode("comment")}
        title="Comment mode"
      >
        💬
      </button>
      <button
        className={`diff-viewer-mode-btn ${mode === "edit" ? "active" : ""}`}
        onClick={() => setMode("edit")}
        title="Edit mode"
      >
        ✏️
      </button>
    </div>
  );

  const navButtons = (
    <div className="diff-viewer-nav-buttons">
      <button
        className="diff-viewer-nav-btn"
        onClick={goToPreviousFile}
        disabled={!hasPrevFile}
        title="Previous file (<)"
      >
        <PrevFileIcon />
      </button>
      <button
        className="diff-viewer-nav-btn"
        onClick={goToPreviousChange}
        disabled={!fileDiff}
        title="Previous change (,)"
      >
        <PrevChangeIcon />
      </button>
      <button
        className="diff-viewer-nav-btn"
        onClick={goToNextChange}
        disabled={!fileDiff}
        title="Next change (.)"
      >
        <NextChangeIcon />
      </button>
      <button
        className="diff-viewer-nav-btn"
        onClick={() => goToNextFile()}
        disabled={!hasNextFile}
        title="Next file (>)"
      >
        <NextFileIcon />
      </button>
    </div>
  );

  return (
    <div className="diff-viewer-overlay">
      <div className="diff-viewer-container">
        {/* Toast notification */}
        {saveStatus !== "idle" && (
          <div className={`diff-viewer-toast diff-viewer-toast-${saveStatus}`}>
            {saveStatus === "saving" && "💾 Saving..."}
            {saveStatus === "saved" && "✅ Saved"}
            {saveStatus === "error" && "❌ Error saving"}
          </div>
        )}
        {showKeyboardHint && (
          <div className="diff-viewer-toast diff-viewer-toast-hint">
            ⌨️ Use . , for next/prev change, &lt; &gt; for files
          </div>
        )}

        {/* Header - different layout for desktop vs mobile */}
        {isMobile ? (
          // Mobile header: just selectors 50/50
          <div className="diff-viewer-header diff-viewer-header-mobile">
            <div className="diff-viewer-mobile-selectors">
              {commitSelector}
              {fileSelector}
            </div>
            <button className="diff-viewer-close" onClick={onClose} title="Close (Esc)">
              ×
            </button>
          </div>
        ) : (
          // Desktop header: selectors expand, controls on right
          <div className="diff-viewer-header">
            <div className="diff-viewer-header-row">
              <div className="diff-viewer-selectors-row">
                {commitSelector}
                {fileSelector}
              </div>
              <div className="diff-viewer-controls-row">
                {navButtons}
                {modeToggle}
                <button className="diff-viewer-close" onClick={onClose} title="Close (Esc)">
                  ×
                </button>
              </div>
            </div>
          </div>
        )}

        {/* Error banner */}
        {error && <div className="diff-viewer-error">{error}</div>}

        {/* Main content */}
        <div className="diff-viewer-content">
          {loading && !fileDiff && (
            <div className="diff-viewer-loading">
              <div className="spinner"></div>
              <span>Loading...</span>
            </div>
          )}

          {!loading && !monacoLoaded && !error && (
            <div className="diff-viewer-loading">
              <div className="spinner"></div>
              <span>Loading editor...</span>
            </div>
          )}

          {!loading && monacoLoaded && !fileDiff && !error && (
            <div className="diff-viewer-empty">
              <p>Select a diff and file to view changes.</p>
              <p className="diff-viewer-hint">Click on line numbers to add comments.</p>
            </div>
          )}

          {/* Monaco editor container */}
          <div
            ref={editorContainerRef}
            className="diff-viewer-editor"
            style={{ display: fileDiff && monacoLoaded ? "block" : "none" }}
          />
        </div>

        {/* Mobile floating nav buttons at bottom */}
        {isMobile && (
          <div className="diff-viewer-mobile-nav">
            <button
              className={`diff-viewer-mobile-nav-btn diff-viewer-mobile-mode-btn ${mode === "comment" ? "active" : ""}`}
              onClick={() => setMode(mode === "comment" ? "edit" : "comment")}
              title={
                mode === "comment" ? "Comment mode (tap to switch)" : "Edit mode (tap to switch)"
              }
            >
              {mode === "comment" ? "💬" : "✏️"}
            </button>
            <button
              className="diff-viewer-mobile-nav-btn"
              onClick={goToPreviousFile}
              disabled={!hasPrevFile}
              title="Previous file (<)"
            >
              <PrevFileIcon />
            </button>
            <button
              className="diff-viewer-mobile-nav-btn"
              onClick={goToPreviousChange}
              disabled={!fileDiff}
              title="Previous change (,)"
            >
              <PrevChangeIcon />
            </button>
            <button
              className="diff-viewer-mobile-nav-btn"
              onClick={goToNextChange}
              disabled={!fileDiff}
              title="Next change (.)"
            >
              <NextChangeIcon />
            </button>
            <button
              className="diff-viewer-mobile-nav-btn"
              onClick={() => goToNextFile()}
              disabled={!hasNextFile}
              title="Next file (>)"
            >
              <NextFileIcon />
            </button>
          </div>
        )}

        {/* Comment dialog */}
        {showCommentDialog && (
          <div className="diff-viewer-comment-dialog">
            <h4>
              Add Comment (Line
              {showCommentDialog.startLine !== showCommentDialog.endLine
                ? `s ${showCommentDialog.startLine}-${showCommentDialog.endLine}`
                : ` ${showCommentDialog.line}`}
              , {showCommentDialog.side === "left" ? "old" : "new"})
            </h4>
            {showCommentDialog.selectedText && (
              <pre className="diff-viewer-selected-text">{showCommentDialog.selectedText}</pre>
            )}
            <textarea
              ref={commentInputRef}
              value={commentText}
              onChange={(e) => setCommentText(e.target.value)}
              placeholder="Enter your comment..."
              className="diff-viewer-comment-input"
              autoFocus
            />
            <div className="diff-viewer-comment-actions">
              <button
                onClick={() => setShowCommentDialog(null)}
                className="diff-viewer-btn diff-viewer-btn-secondary"
              >
                Cancel
              </button>
              <button
                onClick={handleAddComment}
                className="diff-viewer-btn diff-viewer-btn-primary"
                disabled={!commentText.trim()}
              >
                Add Comment
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

export default DiffViewer;
