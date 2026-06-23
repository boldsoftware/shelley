import React, { useState, useEffect, useCallback, useMemo, useRef } from "react";
import type * as Monaco from "monaco-editor";
import {
  ChevronLeftIcon,
  ChevronRightIcon,
  ChevronsLeftIcon,
  ChevronsRightIcon,
  FolderIcon,
  Loader2Icon,
  MessageSquareIcon,
  PencilIcon,
  XIcon,
} from "lucide-react";
import { api } from "../services/api";
import { loadMonaco } from "../services/monaco";
import { isDarkModeActive } from "../services/theme";
import { useVimEnabled, useMonacoVim } from "../hooks/useMonacoVim";
import VimToggle from "./VimToggle";
import { GitDiffInfo, GitFileInfo, GitFileDiff, GitCommitMessage } from "../types";
import DirectoryPickerModal from "./DirectoryPickerModal";
import CommitPicker, { RangeToggle } from "./CommitPicker";
import DiffFileTree, {
  DiffFileTreeEntry,
  COMMIT_MESSAGES_DIR,
  treeRealPathOrder,
} from "./DiffFileTree";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";

interface DiffViewerProps {
  cwd: string;
  isOpen: boolean;
  onClose: () => void;
  onCommentTextChange: (text: string) => void;
  initialCommit?: string; // If set, select this commit when opening
  onCwdChange?: (cwd: string) => void; // Called when user picks a different git directory
}

// Navigation glyphs, mapped onto lucide icons so the chrome stays on the
// shared icon set. Double-chevrons step between files; single-chevrons step
// between changes within a file.
const PrevFileIcon = () => <ChevronsLeftIcon className="size-4" />;
const PrevChangeIcon = () => <ChevronLeftIcon className="size-4" />;
const NextChangeIcon = () => <ChevronRightIcon className="size-4" />;
const NextFileIcon = () => <ChevronsRightIcon className="size-4" />;

type ViewMode = "comment" | "edit";

const COMMIT_MSG_PREFIX = "commit-message:";
const MOBILE_LINE_DECORATIONS_WIDTH = 8;
const DESKTOP_LINE_DECORATIONS_WIDTH = 10;
const MOBILE_SCROLLBAR_SIZE = 8;
const DESKTOP_VERTICAL_SCROLLBAR_SIZE = 14;
const DESKTOP_HORIZONTAL_SCROLLBAR_SIZE = 10;
const MOBILE_OVERVIEW_RULER_LANES = 1;
const DESKTOP_OVERVIEW_RULER_LANES = 3;

function isCommitMessageFile(path: string): boolean {
  return path.startsWith(COMMIT_MSG_PREFIX);
}

function commitHashFromPath(path: string): string {
  return path.slice(COMMIT_MSG_PREFIX.length);
}

function formatCommitMessage(msg: GitCommitMessage): string {
  let text = msg.subject;
  if (msg.body) {
    text += "\n\n" + msg.body;
  }
  return text;
}

function truncateWithEllipsis(text: string, maxLength: number): string {
  if (text.length <= maxLength) return text;
  return text.slice(0, Math.max(0, maxLength - 3)) + "...";
}

function DiffViewer({
  cwd,
  isOpen,
  onClose,
  onCommentTextChange,
  initialCommit,
  onCwdChange,
}: DiffViewerProps) {
  const [diffs, setDiffs] = useState<GitDiffInfo[]>([]);
  const [gitRoot, setGitRoot] = useState<string | null>(null);
  const [showDirPicker, setShowDirPicker] = useState(false);
  const [selectedDiff, setSelectedDiff] = useState<string | null>(null);
  // Right-hand-side bound for the commit range:
  //   "working": through working tree (default)
  //   "self":    only the selected commit
  // Only meaningful when selectedDiff is a commit (not "working").
  const [selectedTo, setSelectedTo] = useState<"working" | "self">("working");
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
  const [showCommentDialog, setShowCommentDialog] = useState<{
    line: number;
    side: "left" | "right";
    selectedText?: string;
    startLine?: number;
    endLine?: number;
  } | null>(null);
  const [commentText, setCommentText] = useState("");
  const [mode, setMode] = useState<ViewMode>("comment");
  const [commitMessages, setCommitMessages] = useState<GitCommitMessage[]>([]);
  // Mirror of commitMessages for reading inside the model-swap effect
  // without forcing it to re-run (and blow away unsaved edits) when the
  // list refreshes but the selected file didn't change.
  const commitMessagesRef = useRef(commitMessages);
  useEffect(() => {
    commitMessagesRef.current = commitMessages;
  }, [commitMessages]);
  const [amendStatus, setAmendStatus] = useState<"idle" | "saving" | "saved" | "error">("idle");
  const amendTimeoutRef = useRef<number | null>(null);
  const [showKeyboardHint, setShowKeyboardHint] = useState(false);
  const hasShownKeyboardHint = useRef(false);

  const [isMobile, setIsMobile] = useState(window.innerWidth < 768);
  const [vimEnabled, setVimEnabled] = useVimEnabled();
  // Desktop-only layout: "header" puts commit + file selectors in the top
  // header row; "sidebar" moves them into a left column with the commit
  // picker stacked above a scrollable list of files. Persisted in
  // localStorage so it survives reloads.
  const [layout, setLayoutState] = useState<"header" | "sidebar">(() => {
    try {
      const v = localStorage.getItem("diff-viewer-layout");
      return v === "sidebar" ? "sidebar" : "header";
    } catch {
      return "header";
    }
  });
  const setLayout = useCallback((v: "header" | "sidebar") => {
    setLayoutState(v);
    try {
      localStorage.setItem("diff-viewer-layout", v);
    } catch {
      // ignore
    }
  }, []);
  // The vim adapter attaches to the modified (right-hand) code editor; we
  // surface it via state because we have the diff editor in a ref.
  const [modifiedEditor, setModifiedEditor] = useState<Monaco.editor.IStandaloneCodeEditor | null>(
    null,
  );
  const [vimStatusNode, setVimStatusNode] = useState<HTMLDivElement | null>(null);
  // :q / :wq / :x and ZZ / ZQ close the diff viewer. The diff viewer's edits
  // are persisted by other paths (handler comments / auto-save), so we treat
  // save+quit the same as plain quit. We pass `onClose` directly (not an
  // inline arrow) so the effect deps stay stable across renders and the
  // vim adapter isn't torn down on every parent re-render.
  // Vim mode only applies in edit mode; in comment mode the editor is read-only
  // and key presses would otherwise be handled by both the comment-mode UI and
  // the vim adapter, causing strange double-handling.
  useMonacoVim(modifiedEditor, vimStatusNode, !isMobile && vimEnabled && mode === "edit", onClose);
  // Mirror of isMobile for handlers attached once at editor-creation time
  // (those handlers must honor the *current* viewport, not the viewport at
  // creation time, because we intentionally don't recreate the editor on
  // resize - see comment on the creation effect below).
  const isMobileRef = useRef(isMobile);
  const editorContainerRef = useRef<HTMLDivElement>(null);
  const editorRef = useRef<Monaco.editor.IStandaloneDiffEditor | null>(null);
  // Real-path order matching how rows render in the sidebar tree, so
  // next/previous-file navigation (the `<`/`>`/`.`/`,` keys) follows
  // the drawer's sort instead of the raw `files` order. Kept in a ref
  // because the nav callbacks are defined above where `treeEntries`
  // (and thus this order) is computed.
  const navOrderRef = useRef<string[]>([]);
  const monacoRef = useRef<typeof Monaco | null>(null);
  const commentInputRef = useRef<HTMLTextAreaElement>(null);
  const modeRef = useRef<ViewMode>(mode);
  const hoverDecorationsRef = useRef<string[]>([]);
  const touchScrolledRef = useRef(false);
  const touchStartPosRef = useRef<{ x: number; y: number } | null>(null);

  // Keep modeRef in sync with mode state and update editor options
  useEffect(() => {
    modeRef.current = mode;
    // Update editor readOnly state when mode changes
    // (but not for commit message files - those have their own editability logic)
    if (editorRef.current && selectedFile && !isCommitMessageFile(selectedFile)) {
      const isWorkingView = selectedDiff === "working" || selectedTo === "working";
      const readOnly = mode === "comment" || !isWorkingView;
      editorRef.current.updateOptions({ readOnly });
      editorRef.current.getModifiedEditor().updateOptions({ readOnly });
    }
  }, [mode, selectedFile, selectedDiff, selectedTo]);

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
    }
  }, [isOpen, isMobile, fileDiff]);

  // Auto-hide keyboard hint after 6 seconds
  useEffect(() => {
    if (showKeyboardHint) {
      const timer = setTimeout(() => setShowKeyboardHint(false), 6000);
      return () => clearTimeout(timer);
    }
  }, [showKeyboardHint]);

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
      setSelectedTo("working");
      setDiffs([]);
      setError(null);
      setShowCommentDialog(null);
      setCommentText("");
      setCommitMessages([]);
      setAmendStatus("idle");
      if (amendTimeoutRef.current) {
        clearTimeout(amendTimeoutRef.current);
        amendTimeoutRef.current = null;
      }
      // The diff editor is disposed by the cleanup of the creation effect
      // below (keyed on isOpen), so we don't dispose it here.
    }
  }, [isOpen, cwd, initialCommit]);

  // Load files when diff (or its `to` bound) is selected
  useEffect(() => {
    if (selectedDiff && cwd) {
      loadFiles(selectedDiff);
    }
  }, [selectedDiff, selectedTo, cwd]);

  // Load file diff when file is selected
  useEffect(() => {
    if (selectedDiff && selectedFile && cwd) {
      loadFileDiff(selectedDiff, selectedFile);
      setCurrentChangeIndex(-1); // Reset change index for new file
    }
  }, [selectedDiff, selectedFile, selectedTo, cwd]);

  // Track current file context for handlers that outlive model swaps.
  // These refs avoid the need to recreate the diff editor (and leak monaco
  // keybinding contributions) every time the user switches files.
  const currentFileIsHeadCommitRef = useRef(false);
  const cwdRef = useRef(cwd);
  useEffect(() => {
    cwdRef.current = cwd;
  }, [cwd]);

  // Create the Monaco diff editor ONCE per monacoLoaded change.
  // Recreating on file switch OR on viewport-breakpoint flip leaks monaco's
  // global keybinding contributions (disposed editors stay referenced by
  // monaco.editor internals), which causes one keypress to fire N cursor
  // commands where N is the number of cumulative editors created. That
  // manifests as backspace deleting multiple characters and arrow keys
  // jumping. So: model swaps + option updates happen in separate effects.
  useEffect(() => {
    if (!isOpen || !monacoLoaded || !editorContainerRef.current || !monacoRef.current) {
      return;
    }

    const monaco = monacoRef.current;

    // Initial readOnly just needs to be safe-by-default; the model-swap
    // effect (which runs right after this one) sets the correct value based
    // on file type (commit message vs regular file) and current mode.
    const initMobile = isMobileRef.current;
    const diffEditor = monaco.editor.createDiffEditor(editorContainerRef.current, {
      theme: isDarkModeActive() ? "vs-dark" : "vs",
      readOnly: true,
      originalEditable: false,
      automaticLayout: true,
      renderSideBySide: !initMobile,
      enableSplitViewResizing: true,
      renderIndicators: true,
      renderMarginRevertIcon: false,
      lineNumbers: initMobile ? "off" : "on",
      minimap: { enabled: false },
      scrollBeyondLastLine: true, // Enable scroll past end for mobile floating buttons
      wordWrap: "on",
      glyphMargin: !initMobile, // Enable glyph margin for comment indicator on hover
      lineDecorationsWidth: initMobile
        ? MOBILE_LINE_DECORATIONS_WIDTH
        : DESKTOP_LINE_DECORATIONS_WIDTH,
      lineNumbersMinChars: initMobile ? 0 : 3,
      scrollbar: {
        verticalScrollbarSize: initMobile ? MOBILE_SCROLLBAR_SIZE : DESKTOP_VERTICAL_SCROLLBAR_SIZE,
        horizontalScrollbarSize: initMobile
          ? MOBILE_SCROLLBAR_SIZE
          : DESKTOP_HORIZONTAL_SCROLLBAR_SIZE,
      },
      overviewRulerLanes: initMobile ? MOBILE_OVERVIEW_RULER_LANES : DESKTOP_OVERVIEW_RULER_LANES,
      quickSuggestions: false,
      suggestOnTriggerCharacters: false,
      lightbulb: { enabled: false },
      codeLens: false,
      contextmenu: false,
      links: false,
      folding: !initMobile,
      padding: initMobile ? { bottom: 80 } : undefined, // Extra padding for floating buttons on mobile
    });

    editorRef.current = diffEditor;
    const modifiedEditor = diffEditor.getModifiedEditor();
    setModifiedEditor(modifiedEditor);

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

    // Desktop: open comment dialog on mousedown (immediate response).
    // The editor is not recreated on viewport resize, so we gate on
    // isMobileRef at call time rather than installing only on desktop.
    modifiedEditor.onMouseDown((e: Monaco.editor.IEditorMouseEvent) => {
      if (isMobileRef.current) return;
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

    // Mobile: use onMouseUp which fires more reliably on touch devices,
    // but only if the user tapped without scrolling (issue #153).
    const editorDom = editorContainerRef.current!;
    const onTouchStart = (e: TouchEvent) => {
      touchScrolledRef.current = false;
      const t = e.touches[0];
      touchStartPosRef.current = { x: t.clientX, y: t.clientY };
    };
    const onTouchMove = (e: TouchEvent) => {
      if (touchScrolledRef.current || !touchStartPosRef.current) return;
      const t = e.touches[0];
      const dx = t.clientX - touchStartPosRef.current.x;
      const dy = t.clientY - touchStartPosRef.current.y;
      if (dx * dx + dy * dy > 100) {
        touchScrolledRef.current = true;
      }
    };
    const onTouchEnd = () => {
      touchStartPosRef.current = null;
    };
    editorDom.addEventListener("touchstart", onTouchStart, { passive: true });
    editorDom.addEventListener("touchmove", onTouchMove, { passive: true });
    editorDom.addEventListener("touchend", onTouchEnd, { passive: true });
    const touchCleanup = () => {
      editorDom.removeEventListener("touchstart", onTouchStart);
      editorDom.removeEventListener("touchmove", onTouchMove);
      editorDom.removeEventListener("touchend", onTouchEnd);
    };

    modifiedEditor.onMouseUp((e: Monaco.editor.IEditorMouseEvent) => {
      if (!isMobileRef.current) return;
      if (modeRef.current !== "comment") return;
      if (touchScrolledRef.current) return;

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

    // Hover highlighting with comment indicator (comment mode only)
    let lastHoveredLine = -1;
    modifiedEditor.onMouseMove((e: Monaco.editor.IEditorMouseEvent) => {
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

    modifiedEditor.onMouseLeave(() => {
      lastHoveredLine = -1;
      hoverDecorationsRef.current = modifiedEditor.deltaDecorations(
        hoverDecorationsRef.current,
        [],
      );
    });

    // Single content change listener; branches based on current file context.
    const contentChangeDisposable = modifiedEditor.onDidChangeModelContent(() => {
      if (currentFileIsHeadCommitRef.current) {
        if (amendTimeoutRef.current) {
          clearTimeout(amendTimeoutRef.current);
        }
        setAmendStatus("saving");
        amendTimeoutRef.current = window.setTimeout(async () => {
          const model = modifiedEditor.getModel();
          if (!model) return;
          const newMessage = model.getValue();
          try {
            await api.amendGitMessage(cwdRef.current, newMessage);
            setAmendStatus("saved");
            setTimeout(() => setAmendStatus("idle"), 2000);
          } catch {
            setAmendStatus("error");
            setTimeout(() => setAmendStatus("idle"), 3000);
          }
        }, 1500);
      } else {
        scheduleSaveRef.current?.();
      }
    });

    return () => {
      contentChangeDisposable.dispose();
      touchCleanup();
      const model = diffEditor.getModel();
      diffEditor.dispose();
      // Dispose the models we created so they don't accumulate.
      model?.original.dispose();
      model?.modified.dispose();
      editorRef.current = null;
      setModifiedEditor(null);
    };
  }, [isOpen, monacoLoaded]);

  // Apply mobile-dependent layout options without recreating the editor.
  useEffect(() => {
    isMobileRef.current = isMobile;
    const diffEditor = editorRef.current;
    if (!diffEditor) return;
    diffEditor.updateOptions({
      renderSideBySide: !isMobile,
      lineNumbers: isMobile ? "off" : "on",
      glyphMargin: !isMobile,
      lineDecorationsWidth: isMobile
        ? MOBILE_LINE_DECORATIONS_WIDTH
        : DESKTOP_LINE_DECORATIONS_WIDTH,
      lineNumbersMinChars: isMobile ? 0 : 3,
      scrollbar: {
        verticalScrollbarSize: isMobile ? MOBILE_SCROLLBAR_SIZE : DESKTOP_VERTICAL_SCROLLBAR_SIZE,
        horizontalScrollbarSize: isMobile
          ? MOBILE_SCROLLBAR_SIZE
          : DESKTOP_HORIZONTAL_SCROLLBAR_SIZE,
      },
      overviewRulerLanes: isMobile ? MOBILE_OVERVIEW_RULER_LANES : DESKTOP_OVERVIEW_RULER_LANES,
      folding: !isMobile,
      padding: isMobile ? { bottom: 80 } : {},
    });
  }, [isMobile]);

  // Swap models into the existing editor when the selected file or its diff
  // changes. This avoids recreating the editor (see comment above).
  useEffect(() => {
    if (!monacoLoaded || !fileDiff || !editorRef.current || !monacoRef.current) {
      return;
    }
    const monaco = monacoRef.current;
    const diffEditor = editorRef.current;

    const isCommitMsg = isCommitMessageFile(fileDiff.path);
    const commitHash = isCommitMsg ? commitHashFromPath(fileDiff.path) : null;
    const isHeadCommit =
      isCommitMsg && commitMessagesRef.current.some((m) => m.hash === commitHash && m.isHead);
    currentFileIsHeadCommitRef.current = isHeadCommit;

    // Language from extension; plaintext for commit messages.
    let language = "plaintext";
    if (!isCommitMsg) {
      const ext = "." + (fileDiff.path.split(".").pop()?.toLowerCase() || "");
      const languages = monaco.languages.getLanguages();
      for (const lang of languages) {
        if (lang.extensions?.includes(ext)) {
          language = lang.id;
          break;
        }
      }
    }

    const timestamp = Date.now();
    const originalUri = monaco.Uri.file(`original-${timestamp}-${fileDiff.path}`);
    const modifiedUri = monaco.Uri.file(`modified-${timestamp}-${fileDiff.path}`);
    const originalModel = monaco.editor.createModel(fileDiff.oldContent, language, originalUri);
    const modifiedModel = monaco.editor.createModel(fileDiff.newContent, language, modifiedUri);

    // Capture the previous models so we can dispose them after swapping.
    const prev = diffEditor.getModel();
    diffEditor.setModel({ original: originalModel, modified: modifiedModel });
    prev?.original.dispose();
    prev?.modified.dispose();

    // Update readOnly based on file type, current mode, and whether we're
    // viewing the working tree (only working-tree views are editable).
    const isWorkingView = selectedDiff === "working" || selectedTo === "working";
    const readOnly = isCommitMsg ? !isHeadCommit : modeRef.current === "comment" || !isWorkingView;
    diffEditor.updateOptions({ readOnly });
    diffEditor.getModifiedEditor().updateOptions({ readOnly });

    // Auto-scroll to first diff once per file load.
    let hasScrolledToFirstChange = false;
    const scrollToFirstChange = () => {
      if (hasScrolledToFirstChange) return;
      const changes = diffEditor.getLineChanges();
      if (changes && changes.length > 0) {
        hasScrolledToFirstChange = true;
        const firstChange = changes[0];
        const targetLine = firstChange.modifiedStartLineNumber || 1;
        const editor = diffEditor.getModifiedEditor();
        editor.revealLineInCenter(targetLine);
        editor.setPosition({ lineNumber: targetLine, column: 1 });
        setCurrentChangeIndex(0);
      }
    };
    scrollToFirstChange();
    const diffUpdateDisposable = diffEditor.onDidUpdateDiff(scrollToFirstChange);

    return () => {
      diffUpdateDisposable.dispose();
    };
  }, [monacoLoaded, fileDiff]);

  const loadDiffs = async () => {
    try {
      setLoading(true);
      setError(null);
      const response = await api.getGitDiffs(cwd);
      setDiffs(response.diffs);
      setGitRoot(response.gitRoot);

      // If initialCommit is set, select that commit and scope the diff to
      // just that commit (parent..commit). Without this we'd inherit the
      // default `selectedTo="working"`, which would show every change
      // between the commit's parent and the working tree — not what the
      // user asked for when they clicked "Open diff" on a specific commit.
      if (initialCommit) {
        const matchingDiff = response.diffs.find(
          (d) => d.id === initialCommit || d.id.startsWith(initialCommit),
        );
        if (matchingDiff) {
          setSelectedDiff(matchingDiff.id);
          setSelectedTo("self");
          return;
        }
      }

      // Default selection: the first commit above merge-base with
      // @{upstream}, with the range running through the working tree.
      // That's the "my branch's changes (so far)" view, which is by far
      // the most useful starting point. We fall back to working changes
      // when there's no merge-base info (detached HEAD, no upstream).
      if (response.diffs.length > 0) {
        const working = response.diffs.find((d) => d.id === "working");
        const commitsOnly = response.diffs.filter((d) => d.id !== "working");
        const mbIdx = commitsOnly.findIndex((d) => d.isMergeBase);
        let topOfBranch: GitDiffInfo | undefined;
        if (mbIdx > 0) {
          // commitsOnly is newest-first; commitsOnly[mbIdx - 1] is the
          // commit one step newer than the merge-base on the branch.
          topOfBranch = commitsOnly[mbIdx - 1];
        }
        if (topOfBranch) {
          setSelectedDiff(topOfBranch.id);
          setSelectedTo("working");
        } else if (working && working.filesCount > 0) {
          setSelectedDiff("working");
        } else if (commitsOnly.length > 0) {
          setSelectedDiff(commitsOnly[0].id);
          setSelectedTo("self");
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
      const toArg = diffId === "working" ? undefined : selectedTo;
      const filesData = await api.getGitDiffFiles(diffId, cwd, toArg);

      // Load commit messages if this is a commit (not working changes)
      let msgs: GitCommitMessage[] = [];
      if (diffId !== "working") {
        try {
          msgs = await api.getGitCommitMessages(cwd, diffId, toArg);
          setCommitMessages(msgs);
        } catch {
          // Non-fatal: just don't show commit messages
          setCommitMessages([]);
        }
      } else {
        setCommitMessages([]);
      }

      // Prepend synthetic commit message entries
      const commitFileEntries: GitFileInfo[] = msgs.map((msg) => ({
        path: COMMIT_MSG_PREFIX + msg.hash,
        status: "added" as const,
        additions: formatCommitMessage(msg).split("\n").length,
        deletions: 0,
        isGenerated: false,
      }));

      const allFiles = [...commitFileEntries, ...(filesData || [])];
      setFiles(allFiles);
      if (allFiles.length > 0) {
        setSelectedFile(allFiles[0].path);
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

      // Handle synthetic commit message files
      if (isCommitMessageFile(filePath)) {
        const hash = commitHashFromPath(filePath);
        const msg = commitMessages.find((m) => m.hash === hash);
        if (msg) {
          setFileDiff({
            path: filePath,
            oldContent: "",
            newContent: formatCommitMessage(msg),
          });
        } else {
          setError("Commit message not found");
        }
        return;
      }

      const toArg = diffId === "working" ? undefined : selectedTo;
      const diffData = await api.getGitFileDiff(diffId, filePath, cwd, toArg);
      setFileDiff(diffData);
    } catch (err) {
      setError(`Failed to load file diff: ${err}`);
    } finally {
      setLoading(false);
    }
  };

  const handleAddComment = () => {
    if (!showCommentDialog || !commentText.trim() || !selectedFile) return;

    const line = showCommentDialog.line;
    const codeSnippet = showCommentDialog.selectedText?.split("\n")[0]?.trim() || "";
    const truncatedCode = truncateWithEllipsis(codeSnippet, 60);

    // For commit message files, use a readable reference
    let fileRef = selectedFile;
    if (isCommitMessageFile(selectedFile)) {
      const hash = commitHashFromPath(selectedFile);
      const msg = commitMessages.find((m) => m.hash === hash);
      fileRef = msg
        ? `commit ${hash.slice(0, 8)} (${truncateWithEllipsis(msg.subject, 40)})`
        : `commit ${hash.slice(0, 8)}`;
    }

    const commentBlock = `> ${fileRef}:${line}: ${truncatedCode}\n${commentText}\n\n`;

    onCommentTextChange(commentBlock);
    setShowCommentDialog(null);
    setCommentText("");
  };

  const goToNextFile = useCallback(() => {
    const order = navOrderRef.current;
    if (order.length === 0 || !selectedFile) return false;
    const idx = order.indexOf(selectedFile);
    if (idx >= 0 && idx < order.length - 1) {
      setSelectedFile(order[idx + 1]);
      setCurrentChangeIndex(-1); // Reset to start of new file
      return true;
    }
    return false;
  }, [selectedFile]);

  const goToPreviousFile = useCallback(() => {
    const order = navOrderRef.current;
    if (order.length === 0 || !selectedFile) return false;
    const idx = order.indexOf(selectedFile);
    if (idx > 0) {
      setSelectedFile(order[idx - 1]);
      setCurrentChangeIndex(-1); // Will go to last change when file loads
      return true;
    }
    return false;
  }, [selectedFile]);

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
    const isWorkingView = selectedDiff === "working" || selectedTo === "working";
    if (
      !editorRef.current ||
      !selectedFile ||
      isCommitMessageFile(selectedFile) ||
      !fileDiff ||
      modeRef.current !== "edit" ||
      !gitRoot ||
      !isWorkingView
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
  }, [selectedFile, fileDiff, gitRoot, selectedDiff, selectedTo]);

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
        // If Monaco's find widget is open, let Monaco handle Escape to close it
        const findWidget = editorContainerRef.current?.querySelector(".find-widget.visible");
        if (findWidget) {
          return; // Let Monaco close its find widget
        }
        // If a nested overlay (commit picker, dir picker) is open, let it
        // handle Escape rather than closing the whole diff viewer.
        if (
          document.querySelector(".commit-picker-popover") ||
          document.querySelector(".commit-picker-modal")
        ) {
          return;
        }
        // If vim mode is active in a non-normal mode (insert/visual/...),
        // let monaco-vim handle Escape (to drop back to normal) instead of
        // closing the modal. We detect non-normal mode via the vim status
        // node, which monaco-vim populates with e.g. "-- INSERT --". Normal
        // mode renders an empty status, so a second Esc still closes the
        // modal as users expect. Mobile doesn't attach vim, so skip it.
        const vimFocused =
          editorContainerRef.current?.contains(document.activeElement) ||
          vimStatusNode?.contains(document.activeElement);
        if (
          !isMobile &&
          vimEnabled &&
          vimFocused &&
          (vimStatusNode?.textContent ?? "").trim() !== ""
        ) {
          return;
        }
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

      // Route Ctrl/Cmd+F to Monaco's find widget instead of browser find
      if ((e.ctrlKey || e.metaKey) && e.key === "f") {
        if (editorRef.current) {
          e.preventDefault();
          e.stopPropagation();
          const modifiedEditor = editorRef.current.getModifiedEditor();
          modifiedEditor.focus();
          modifiedEditor.trigger("keyboard", "actions.find", null);
        }
        return;
      }

      // When Monaco's find widget is open, let all non-modifier keys pass through
      // so typing in the find input works (e.g. "." and "," won't trigger nav)
      const findWidget = editorContainerRef.current?.querySelector(".find-widget.visible");
      if (findWidget) {
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

    // Use capture phase to intercept events before Monaco editor handles
    // them. Important for the vim-mode Esc guard: monaco-vim clears the
    // status bar synchronously when leaving insert/visual mode, so we have
    // to read it before monaco-vim's own keydown handler runs.
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
    vimEnabled,
    vimStatusNode,
    isMobile,
  ]);

  // Sidebar tree input. Computed here — *above* the early `!isOpen`
  // return — so the hook order stays stable across renders. Commit
  // messages get slotted under a synthetic "Commit messages" folder;
  // the short hash (and a HEAD marker) live in the right-aligned
  // decoration lane so the subject — not the hash — is what gets
  // truncated when space is tight.
  const treeEntries = useMemo<DiffFileTreeEntry[]>(() => {
    // Index commit messages by hash once so the per-file pass stays
    // linear instead of O(N·M).
    const msgByHash = new Map(commitMessages.map((m) => [m.hash, m]));
    // First pass: figure out which (sanitized) subjects collide so we
    // know which leaves need a hash suffix to stay distinct.
    const subjectCounts = new Map<string, number>();
    for (const f of files) {
      if (!isCommitMessageFile(f.path)) continue;
      const hash = commitHashFromPath(f.path);
      const msg = msgByHash.get(hash);
      const subject = msg ? msg.subject : hash.slice(0, 8);
      subjectCounts.set(subject, (subjectCounts.get(subject) ?? 0) + 1);
    }
    return files.map((f) => {
      if (isCommitMessageFile(f.path)) {
        const hash = commitHashFromPath(f.path);
        const msg = msgByHash.get(hash);
        const subject = msg ? msg.subject : hash.slice(0, 8);
        const shortHash = hash.slice(0, 8);
        // `treePath` is segmented, so `/` in the subject stays inside
        // the leaf label — no FRACTION-SLASH substitution needed (which
        // used to fall back to a different system font and look weird).
        const collides = (subjectCounts.get(subject) ?? 0) > 1;
        const leaf = collides ? `${subject} (${shortHash})` : subject;
        return {
          realPath: f.path,
          treePath: [COMMIT_MESSAGES_DIR, leaf],
          decoration: msg?.isHead ? "HEAD" : undefined,
          decorationTitle: msg?.isHead ? `${hash} (HEAD)` : undefined,
        };
      }
      return {
        realPath: f.path,
        treePath: f.path.split("/"),
        status: f.status,
      };
    });
  }, [files, commitMessages]);

  // Order used by next/previous-file navigation. Mirrors the order rows
  // render in the sidebar tree so `<`/`>`/`.`/`,` track what the user
  // sees instead of the raw `files` order. Stored in a ref the nav
  // callbacks (defined above) read at call time.
  const navOrder = useMemo(() => treeRealPathOrder(treeEntries), [treeEntries]);
  useEffect(() => {
    navOrderRef.current = navOrder;
  }, [navOrder]);

  // Title shown in the desktop sidebar layout's header: the open
  // file's path, or a commit-message subject (with HEAD suffix) when
  // viewing a synthetic commit-message row. Falls back to a non-
  // breaking space so the header height stays constant.
  let currentTitleText: string | null = null;
  let currentTitleTooltip: string | null = null;
  if (selectedFile) {
    if (isCommitMessageFile(selectedFile)) {
      const hash = commitHashFromPath(selectedFile);
      const msg = commitMessages.find((m) => m.hash === hash);
      const subject = msg ? msg.subject : hash.slice(0, 8);
      currentTitleText = msg?.isHead ? `${subject} — HEAD` : subject;
      currentTitleTooltip = msg?.isHead ? `${hash} (HEAD)\n\n${subject}` : `${hash}\n\n${subject}`;
    } else {
      currentTitleText = selectedFile;
      currentTitleTooltip = selectedFile;
    }
  }

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

  // Index into the tree-render order (not the raw `files` order) so the
  // position indicator and prev/next affordances match the sidebar and
  // the keyboard navigation.
  const currentFileIndex = navOrder.indexOf(selectedFile ?? "");
  const hasNextFile = currentFileIndex >= 0 && currentFileIndex < navOrder.length - 1;
  const hasPrevFile = currentFileIndex > 0;

  // Single combined commit picker (replaces the prior pair of <select>s).
  const commitSelector = (
    <CommitPicker
      diffs={diffs}
      selectedDiff={selectedDiff}
      selectedTo={selectedTo}
      onChange={(diff, to) => {
        setSelectedDiff(diff);
        setSelectedTo(to);
      }}
      isMobile={isMobile}
    />
  );

  // Sidebar commit list. We want a short, scannable list of "interesting"
  // commits: working tree, then everything up to (and including) the
  // merge-base with @{upstream}. When there's no merge-base info (e.g.
  // detached HEAD with no upstream), fall back to the top 10 commits.
  const sidebarCommits = (() => {
    const list: GitDiffInfo[] = [];
    const working = diffs.find((d) => d.id === "working");
    if (working) list.push(working);
    const commitsOnly = diffs.filter((d) => d.id !== "working");
    const mergeBaseIdx = commitsOnly.findIndex((d) => d.isMergeBase);
    if (mergeBaseIdx >= 0) {
      // Include up to and including the merge-base. Cap to keep the
      // sidebar usable for branches with huge divergence.
      list.push(...commitsOnly.slice(0, Math.min(mergeBaseIdx + 1, 50)));
    } else {
      list.push(...commitsOnly.slice(0, 10));
    }
    return list;
  })();

  // Compute which sidebar rows fall inside the active diff range so we
  // can highlight either the single selected commit or the whole span
  // through the working tree. `sidebarCommits` is rendered with the
  // working row first, then commits in newest-first order, matching the
  // git history's natural top-down layout.
  const sidebarSelIdx = selectedDiff ? sidebarCommits.findIndex((d) => d.id === selectedDiff) : -1;
  const inSidebarRange = (idx: number): boolean => {
    if (sidebarSelIdx < 0) return false;
    if (selectedDiff === "working") return idx === sidebarSelIdx;
    if (selectedTo === "self") return idx === sidebarSelIdx;
    // through working tree: highlight from the working row (idx 0) down
    // to and including the selected commit row.
    return idx >= 0 && idx <= sidebarSelIdx;
  };

  const commitList = (
    <ul className="flex flex-col gap-0.5" role="listbox" aria-label="Commits">
      {sidebarCommits.length === 0 && (
        <li className="px-2 py-3 text-center text-xs text-muted-foreground">No commits</li>
      )}
      {sidebarCommits.map((d, idx) => {
        const isWorking = d.id === "working";
        const isSelected = selectedDiff === d.id;
        const inRange = inSidebarRange(idx);
        const subject = isWorking ? "Working Changes" : d.message;
        const refs = d.refs ?? [];
        return (
          <li key={d.id}>
            <button
              type="button"
              className={cn(
                "flex w-full flex-col gap-1 rounded-md border border-transparent px-2 py-1.5 text-left transition-colors hover:bg-muted",
                inRange && "bg-muted/60",
                isSelected && "border-primary/40 bg-primary/10",
                isWorking && "font-medium",
              )}
              onClick={() => {
                // Clicking a row picks that commit; keep the current
                // range mode (only this / through working) so the
                // header toggle stays in charge of the variant.
                if (isWorking) {
                  setSelectedDiff("working");
                } else {
                  setSelectedDiff(d.id);
                }
              }}
              title={isWorking ? "Working changes" : `${d.message}\n${d.id}`}
              role="option"
              aria-selected={isSelected}
            >
              <div className="min-w-0">
                <span className="block truncate text-sm">{subject}</span>
              </div>
              {!isWorking && (refs.length > 0 || d.isMergeBase) && (
                <div className="flex flex-wrap gap-1">
                  {refs.map((ref) => (
                    <span
                      key={ref}
                      className={cn(
                        "rounded-sm px-1 py-0.5 font-mono text-[10px] leading-none",
                        ref === "HEAD"
                          ? "bg-amber-500/15 text-amber-700 dark:text-amber-400"
                          : ref.includes("/")
                            ? "bg-blue-500/15 text-blue-700 dark:text-blue-400"
                            : "bg-muted text-muted-foreground",
                      )}
                    >
                      {ref}
                    </span>
                  ))}
                  {d.isMergeBase && !refs.some((r) => r.includes("/")) && (
                    <span
                      className="rounded-sm bg-muted px-1 py-0.5 font-mono text-[10px] leading-none text-muted-foreground"
                      title="Merge-base with @{upstream}"
                    >
                      merge-base
                    </span>
                  )}
                </div>
              )}
            </button>
          </li>
        );
      })}
    </ul>
  );

  // Sidebar file list: a simple in-house file tree that mixes real
  // files with commit-message rows. Commit-message paths are synthetic
  // ("commit-message:<hash>") and don't belong in the filesystem
  // layout, so we slot them under a synthetic "Commit messages"
  // directory in the tree while keeping the rest of the codebase
  // working with the real paths via DiffFileTreeEntry's realPath /
  // treePath mapping.
  const fileList = (
    <div className="min-h-0" aria-label="Files">
      {files.length === 0 && (
        <div className="px-2 py-3 text-center text-xs text-muted-foreground">No files</div>
      )}
      {files.length > 0 && (
        <div className="py-1">
          <DiffFileTree
            entries={treeEntries}
            selectedRealPath={selectedFile}
            onSelect={(path) => setSelectedFile(path)}
          />
        </div>
      )}
    </div>
  );

  // Chevron-double sidebar toggle. In header mode (no sidebar) we show
  // `«` to invite the user to pull a panel in from the left; once the
  // sidebar is showing, the button at its top edge becomes `»` to push
  // it back away.
  const expandSidebarButton = (
    <Button
      type="button"
      variant="ghost"
      size="icon-sm"
      onClick={() => setLayout("sidebar")}
      aria-label="Show sidebar"
      title="Show sidebar"
    >
      <ChevronsLeftIcon className="size-5" />
    </Button>
  );

  const collapseSidebarButton = (
    <Button
      type="button"
      variant="ghost"
      size="icon-sm"
      onClick={() => setLayout("header")}
      aria-label="Hide sidebar"
      title="Hide sidebar"
    >
      <ChevronsRightIcon className="size-5" />
    </Button>
  );

  const fileIndexIndicator =
    navOrder.length > 1 && currentFileIndex >= 0
      ? `(${currentFileIndex + 1}/${navOrder.length})`
      : null;

  const fileSelector = (
    <div className="flex min-w-0 items-center gap-2">
      <select
        value={selectedFile || ""}
        onChange={(e) => setSelectedFile(e.target.value || null)}
        className="diff-viewer-select h-8 min-w-0 flex-1 rounded-md border border-border bg-background px-2 text-sm text-foreground outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/30 disabled:cursor-not-allowed disabled:opacity-50"
        disabled={files.length === 0}
      >
        <option value="">{files.length === 0 ? "No files" : "Choose file..."}</option>
        {files.map((file) => {
          if (isCommitMessageFile(file.path)) {
            const hash = commitHashFromPath(file.path);
            const msg = commitMessages.find((m) => m.hash === hash);
            const label = msg
              ? `📝 ${truncateWithEllipsis(msg.subject, 50)}`
              : `📝 ${hash.slice(0, 8)}`;
            return (
              <option key={file.path} value={file.path}>
                {label}
                {msg?.isHead ? " [HEAD]" : ""}
              </option>
            );
          }
          return (
            <option key={file.path} value={file.path}>
              {getStatusSymbol(file.status)} {file.path}
              {file.additions > 0 && ` (+${file.additions})`}
              {file.deletions > 0 && ` (-${file.deletions})`}
              {file.isGenerated && " [generated]"}
            </option>
          );
        })}
      </select>
      {fileIndexIndicator && (
        <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
          {fileIndexIndicator}
        </span>
      )}
    </div>
  );

  const modeToggle = (
    <div className="flex gap-0.5 rounded-md border border-border p-0.5">
      <button
        type="button"
        className={cn(
          "flex size-7 items-center justify-center rounded-sm transition-colors",
          mode === "comment"
            ? "bg-primary text-primary-foreground"
            : "text-muted-foreground hover:bg-muted hover:text-foreground",
        )}
        onClick={() => setMode("comment")}
        title="Comment mode"
      >
        <MessageSquareIcon className="size-4" />
      </button>
      <button
        type="button"
        className={cn(
          "flex size-7 items-center justify-center rounded-sm transition-colors",
          mode === "edit"
            ? "bg-primary text-primary-foreground"
            : "text-muted-foreground hover:bg-muted hover:text-foreground",
        )}
        onClick={() => setMode("edit")}
        title="Edit mode"
      >
        <PencilIcon className="size-4" />
      </button>
    </div>
  );

  const navButtons = (
    <div className="flex items-center gap-0.5">
      <Button
        variant="ghost"
        size="icon-sm"
        onClick={goToPreviousFile}
        disabled={!hasPrevFile}
        title="Previous file (<)"
      >
        <PrevFileIcon />
      </Button>
      <Button
        variant="ghost"
        size="icon-sm"
        onClick={goToPreviousChange}
        disabled={!fileDiff}
        title="Previous change (,)"
      >
        <PrevChangeIcon />
      </Button>
      <Button
        variant="ghost"
        size="icon-sm"
        onClick={goToNextChange}
        disabled={!fileDiff}
        title="Next change (.)"
      >
        <NextChangeIcon />
      </Button>
      <Button
        variant="ghost"
        size="icon-sm"
        onClick={() => goToNextFile()}
        disabled={!hasNextFile}
        title="Next file (>)"
      >
        <NextFileIcon />
      </Button>
    </div>
  );

  const dirButton = (
    <Button
      variant="ghost"
      size="icon-sm"
      onClick={() => setShowDirPicker(true)}
      title={`Git directory: ${cwd}\nClick to change`}
      aria-label="Change git directory"
    >
      <FolderIcon className="size-4" />
    </Button>
  );

  const toastBase =
    "pointer-events-none absolute top-3 left-1/2 z-50 -translate-x-1/2 rounded-md border px-3 py-1.5 text-sm shadow-lg";
  const toastTone = (status: "saving" | "saved" | "error") =>
    status === "error"
      ? "border-destructive/40 bg-popover text-destructive"
      : status === "saved"
        ? "border-emerald-500/40 bg-popover text-emerald-600 dark:text-emerald-400"
        : "border-border bg-popover text-popover-foreground";

  return (
    <div className="diff-viewer-overlay fixed inset-0 z-40 flex items-stretch justify-stretch bg-background/80 backdrop-blur-sm">
      <div className="diff-viewer-container relative flex min-h-0 w-full flex-col bg-background text-foreground">
        {/* Toast notification */}
        {saveStatus !== "idle" && (
          <div className={cn(toastBase, toastTone(saveStatus))}>
            {saveStatus === "saving" && "💾 Saving..."}
            {saveStatus === "saved" && "✅ Saved"}
            {saveStatus === "error" && "❌ Error saving"}
          </div>
        )}
        {amendStatus !== "idle" && (
          <div className={cn(toastBase, toastTone(amendStatus))}>
            {amendStatus === "saving" && "💾 Amending..."}
            {amendStatus === "saved" && "✅ Amended"}
            {amendStatus === "error" && "❌ Error amending"}
          </div>
        )}
        {showKeyboardHint && (
          <div className={cn(toastBase, "border-border bg-popover text-popover-foreground")}>
            ⌨️ Use . , for next/prev change, &lt; &gt; for files
          </div>
        )}

        {/* Header - different layout for desktop vs mobile */}
        {isMobile ? (
          // Mobile header: just selectors 50/50
          <div className="flex shrink-0 items-center gap-2 border-b border-border px-2 py-2">
            <div className="grid min-w-0 flex-1 grid-cols-2 gap-2">
              {commitSelector}
              {fileSelector}
            </div>
            {dirButton}
            <Button
              variant="ghost"
              size="icon-sm"
              onClick={onClose}
              title="Close (Esc)"
              aria-label="Close (Esc)"
            >
              <XIcon className="size-5" />
            </Button>
          </div>
        ) : (
          // Desktop header: selectors expand, controls on right.
          // In sidebar layout, the « collapse button is the leftmost
          // element in the top bar (sitting directly above the sidebar);
          // the inline commit/file selectors are hidden in that mode
          // because the sidebar shows commit and file lists instead.
          <div className="shrink-0 border-b border-border px-3 py-2">
            <div className="flex items-center gap-3">
              {layout === "sidebar" && collapseSidebarButton}
              {layout === "header" && expandSidebarButton}
              {layout === "header" ? (
                <div className="flex min-w-0 flex-1 items-end gap-4">
                  <div className="flex min-w-0 flex-1 flex-col gap-1">
                    <label className="text-xs font-medium text-muted-foreground">Commits</label>
                    {commitSelector}
                  </div>
                  <div className="flex min-w-0 flex-1 flex-col gap-1">
                    <label className="text-xs font-medium text-muted-foreground">
                      Commit messages and changed files
                    </label>
                    {fileSelector}
                  </div>
                </div>
              ) : (
                <div
                  className="min-w-0 flex-1 truncate text-sm font-medium"
                  title={currentTitleTooltip ?? undefined}
                >
                  {currentTitleText ?? "\u00a0"}
                </div>
              )}
              <div className="flex shrink-0 items-center gap-2">
                {navButtons}
                {modeToggle}
                <VimToggle enabled={vimEnabled} onChange={setVimEnabled} />
                {dirButton}
                <Button
                  variant="ghost"
                  size="icon-sm"
                  onClick={onClose}
                  title="Close (Esc)"
                  aria-label="Close (Esc)"
                >
                  <XIcon className="size-5" />
                </Button>
              </div>
            </div>
          </div>
        )}

        {/* Error banner */}
        {error && (
          <div className="shrink-0 border-b border-destructive/30 bg-destructive/10 px-4 py-2 text-sm text-destructive">
            {error}
          </div>
        )}

        {/* Main content */}
        <div className="flex min-h-0 flex-1">
          {!isMobile && layout === "sidebar" && (
            <aside className="flex w-72 shrink-0 flex-col border-r border-border">
              <div className="flex min-h-0 flex-1 flex-col border-b border-border">
                <div className="flex shrink-0 items-center justify-between px-3 py-1.5 text-xs font-medium text-muted-foreground">
                  <span>Commits</span>
                </div>
                <div className="shrink-0 px-3 pb-2">
                  <RangeToggle
                    selectedDiff={selectedDiff}
                    selectedTo={selectedTo}
                    onChange={(diff, to) => {
                      setSelectedDiff(diff);
                      setSelectedTo(to);
                    }}
                  />
                </div>
                <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-2">{commitList}</div>
              </div>
              <div className="flex min-h-0 flex-1 flex-col">
                <div className="flex shrink-0 items-center justify-between px-3 py-1.5 text-xs font-medium text-muted-foreground">
                  <span>Commit Messages and Files</span>
                  {fileIndexIndicator && (
                    <span className="tabular-nums">{fileIndexIndicator}</span>
                  )}
                </div>
                <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-2">{fileList}</div>
              </div>
            </aside>
          )}
          <div className="relative flex min-h-0 min-w-0 flex-1 flex-col">
            {loading && !fileDiff && (
              <div className="flex flex-1 flex-col items-center justify-center gap-3 text-muted-foreground">
                <Loader2Icon className="size-6 animate-spin" />
                <span>Loading...</span>
              </div>
            )}

            {!loading && !monacoLoaded && !error && (
              <div className="flex flex-1 flex-col items-center justify-center gap-3 text-muted-foreground">
                <Loader2Icon className="size-6 animate-spin" />
                <span>Loading editor...</span>
              </div>
            )}

            {!loading && monacoLoaded && !fileDiff && !error && (
              <div className="flex flex-1 flex-col items-center justify-center gap-1 text-center text-muted-foreground">
                <p>Select a diff and file to view changes.</p>
                <p className="text-sm">Click on line numbers to add comments.</p>
              </div>
            )}

            {/* Monaco editor container */}
            <div
              ref={editorContainerRef}
              className="diff-viewer-editor min-h-0 flex-1"
              style={{ display: fileDiff && monacoLoaded ? "block" : "none" }}
            />
            {!isMobile && vimEnabled && fileDiff && monacoLoaded && (
              <div ref={setVimStatusNode} className="monaco-vim-status" />
            )}
          </div>
        </div>

        {/* Mobile floating nav buttons at bottom */}
        {isMobile && (
          <div className="pointer-events-auto absolute bottom-4 left-1/2 z-30 flex -translate-x-1/2 items-center gap-1 rounded-full border border-border bg-popover/95 px-2 py-1.5 shadow-lg backdrop-blur">
            <button
              type="button"
              className={cn(
                "flex size-9 items-center justify-center rounded-full text-lg transition-colors",
                mode === "comment"
                  ? "bg-primary text-primary-foreground"
                  : "text-muted-foreground hover:bg-muted hover:text-foreground",
              )}
              onClick={() => setMode(mode === "comment" ? "edit" : "comment")}
              title={
                mode === "comment" ? "Comment mode (tap to switch)" : "Edit mode (tap to switch)"
              }
            >
              {mode === "comment" ? "💬" : "✏️"}
            </button>
            <Button
              variant="ghost"
              size="icon"
              className="rounded-full"
              onClick={goToPreviousFile}
              disabled={!hasPrevFile}
              title="Previous file (<)"
            >
              <PrevFileIcon />
            </Button>
            <Button
              variant="ghost"
              size="icon"
              className="rounded-full"
              onClick={goToPreviousChange}
              disabled={!fileDiff}
              title="Previous change (,)"
            >
              <PrevChangeIcon />
            </Button>
            <Button
              variant="ghost"
              size="icon"
              className="rounded-full"
              onClick={goToNextChange}
              disabled={!fileDiff}
              title="Next change (.)"
            >
              <NextChangeIcon />
            </Button>
            <Button
              variant="ghost"
              size="icon"
              className="rounded-full"
              onClick={() => goToNextFile()}
              disabled={!hasNextFile}
              title="Next file (>)"
            >
              <NextFileIcon />
            </Button>
          </div>
        )}

        {/* Comment dialog */}
        {showCommentDialog && (
          <div className="absolute bottom-4 left-1/2 z-40 flex w-[min(28rem,90vw)] -translate-x-1/2 flex-col gap-3 rounded-lg border border-border bg-popover p-4 text-popover-foreground shadow-lg">
            <h4 className="text-sm font-medium">
              Add Comment (Line
              {showCommentDialog.startLine !== showCommentDialog.endLine
                ? `s ${showCommentDialog.startLine}-${showCommentDialog.endLine}`
                : ` ${showCommentDialog.line}`}
              , {showCommentDialog.side === "left" ? "old" : "new"})
            </h4>
            {showCommentDialog.selectedText && (
              <pre className="max-h-24 overflow-auto rounded-md bg-muted px-2 py-1.5 font-mono text-xs whitespace-pre-wrap break-words text-muted-foreground">
                {showCommentDialog.selectedText}
              </pre>
            )}
            <Textarea
              ref={commentInputRef}
              value={commentText}
              onChange={(e) => setCommentText(e.target.value)}
              placeholder="Enter your comment..."
              autoFocus
            />
            <div className="flex justify-end gap-2">
              <Button variant="outline" size="sm" onClick={() => setShowCommentDialog(null)}>
                Cancel
              </Button>
              <Button size="sm" onClick={handleAddComment} disabled={!commentText.trim()}>
                Add Comment
              </Button>
            </div>
          </div>
        )}
      </div>

      {/* Directory picker for changing git directory */}
      <DirectoryPickerModal
        isOpen={showDirPicker}
        onClose={() => setShowDirPicker(false)}
        onSelect={(path) => {
          onCwdChange?.(path);
          setShowDirPicker(false);
        }}
        initialPath={cwd}
        foldersOnly
      />
    </div>
  );
}

export default DiffViewer;
