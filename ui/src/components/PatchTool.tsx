import React, { useState, useEffect, useCallback, Component, type ReactNode } from "react";
import { PatchDiff, MultiFileDiff } from "@pierre/diffs/react";
import type { FileContents, SupportedLanguages, ThemeTypes, ThemesType } from "@pierre/diffs";
import { LLMContent } from "../types";
import { isDarkModeActive } from "../services/theme";

// LocalStorage key for side-by-side preference
const STORAGE_KEY_SIDE_BY_SIDE = "shelley-diff-side-by-side";

// Get saved side-by-side preference (default: true for desktop)
function getSideBySidePreference(): boolean {
  try {
    const stored = localStorage.getItem(STORAGE_KEY_SIDE_BY_SIDE);
    if (stored !== null) {
      return stored === "true";
    }
    // Default to side-by-side on desktop, inline on mobile
    return window.innerWidth >= 768;
  } catch {
    return window.innerWidth >= 768;
  }
}

function setSideBySidePreference(value: boolean): void {
  try {
    localStorage.setItem(STORAGE_KEY_SIDE_BY_SIDE, value ? "true" : "false");
  } catch {
    // Ignore storage errors
  }
}

// Display data — new payloads have {path, diff}; legacy ones also include oldContent/newContent.
interface PatchDisplayData {
  path: string;
  diff?: string;
  oldContent?: string;
  newContent?: string;
}

interface PatchToolProps {
  // For tool_use (pending state)
  toolInput?: unknown;
  isRunning?: boolean;

  // For tool_result (completed state)
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  display?: unknown; // Display data from the tool_result Content (contains the diff or structured data)
  onCommentTextChange?: (text: string) => void;
}

// Map file extension to language for syntax highlighting
function getLanguageFromPath(path: string): SupportedLanguages {
  const ext = path.split(".").pop()?.toLowerCase() || "";
  const langMap: Record<string, SupportedLanguages> = {
    ts: "typescript",
    tsx: "tsx",
    js: "javascript",
    jsx: "jsx",
    py: "python",
    rb: "ruby",
    go: "go",
    rs: "rust",
    java: "java",
    c: "c",
    cpp: "cpp",
    h: "c",
    hpp: "cpp",
    cs: "csharp",
    php: "php",
    swift: "swift",
    kt: "kotlin",
    scala: "scala",
    sh: "bash",
    bash: "bash",
    zsh: "bash",
    fish: "fish",
    ps1: "powershell",
    sql: "sql",
    html: "html",
    htm: "html",
    css: "css",
    scss: "scss",
    sass: "sass",
    less: "less",
    json: "json",
    xml: "xml",
    yaml: "yaml",
    yml: "yaml",
    toml: "toml",
    ini: "ini",
    md: "markdown",
    markdown: "markdown",
    txt: "text",
    dockerfile: "dockerfile",
    makefile: "makefile",
    cmake: "cmake",
    lua: "lua",
    perl: "perl",
    r: "r",
    vue: "vue",
    svelte: "svelte",
    astro: "astro",
  };
  return langMap[ext] || "text";
}

// Error boundary that catches @pierre/diffs render crashes and falls back to <pre>.
interface DiffErrorBoundaryProps {
  diff: string;
  children: ReactNode;
}

interface DiffErrorBoundaryState {
  hasError: boolean;
}

class DiffErrorBoundary extends Component<DiffErrorBoundaryProps, DiffErrorBoundaryState> {
  state: DiffErrorBoundaryState = { hasError: false };

  static getDerivedStateFromError(): DiffErrorBoundaryState {
    return { hasError: true };
  }

  componentDidUpdate(prevProps: DiffErrorBoundaryProps) {
    // Reset error state when the diff content changes so new diffs get a fresh try.
    if (this.state.hasError && prevProps.diff !== this.props.diff) {
      this.setState({ hasError: false });
    }
  }

  componentDidCatch(error: Error) {
    console.warn("DiffErrorBoundary caught:", error.message);
  }

  render() {
    if (this.state.hasError) {
      return <pre className="patch-tool-raw-diff">{this.props.diff}</pre>;
    }
    return this.props.children;
  }
}

function useThemeType(): ThemeTypes {
  const [themeType, setThemeType] = useState<ThemeTypes>(isDarkModeActive() ? "dark" : "light");

  useEffect(() => {
    const updateTheme = () => setThemeType(isDarkModeActive() ? "dark" : "light");
    const observer = new MutationObserver((mutations) => {
      for (const mutation of mutations) {
        if (mutation.attributeName === "class") updateTheme();
      }
    });
    observer.observe(document.documentElement, { attributes: true });
    return () => observer.disconnect();
  }, []);

  return themeType;
}

const DIFF_THEMES: ThemesType = { dark: "github-dark", light: "github-light" };

// Render a diff from a unified-diff string via PatchDiff.
function PatchDiffView({ patch, sideBySide }: { patch: string; sideBySide: boolean }) {
  const themeType = useThemeType();

  return (
    <div className="patch-tool-diffs-container">
      <DiffErrorBoundary diff={patch}>
        <PatchDiff
          patch={patch}
          options={{
            diffStyle: sideBySide ? "split" : "unified",
            theme: DIFF_THEMES,
            themeType,
            disableFileHeader: true,
          }}
        />
      </DiffErrorBoundary>
    </div>
  );
}

// Render a diff from old/new file contents via MultiFileDiff (legacy data).
function SnapshotDiffView({
  displayData,
  sideBySide,
}: {
  displayData: PatchDisplayData;
  sideBySide: boolean;
}) {
  const themeType = useThemeType();
  const lang = getLanguageFromPath(displayData.path);

  const oldFile: FileContents = {
    name: displayData.path,
    contents: displayData.oldContent ?? "",
    lang,
  };
  const newFile: FileContents = {
    name: displayData.path,
    contents: displayData.newContent ?? "",
    lang,
  };

  return (
    <div className="patch-tool-diffs-container">
      <DiffErrorBoundary diff={displayData.diff ?? ""}>
        <MultiFileDiff
          oldFile={oldFile}
          newFile={newFile}
          options={{
            diffStyle: sideBySide ? "split" : "unified",
            theme: DIFF_THEMES,
            themeType,
            disableFileHeader: true,
          }}
        />
      </DiffErrorBoundary>
    </div>
  );
}

// Picks the right diff renderer based on available data.
function DiffView({
  displayData,
  sideBySide,
}: {
  displayData: PatchDisplayData;
  sideBySide: boolean;
}) {
  // Legacy payloads with full file snapshots — use MultiFileDiff which doesn't
  // rely on parsing the unified diff string (immune to content that looks like
  // diff headers, e.g. SQL "--- " comments).
  if (displayData.oldContent != null && displayData.newContent != null) {
    return <SnapshotDiffView displayData={displayData} sideBySide={sideBySide} />;
  }

  // New payloads with only the unified diff string.
  if (displayData.diff) {
    return <PatchDiffView patch={displayData.diff} sideBySide={sideBySide} />;
  }

  // Nothing renderable.
  return null;
}

// Side-by-side toggle icon component
function DiffModeToggle({ sideBySide, onToggle }: { sideBySide: boolean; onToggle: () => void }) {
  return (
    <button
      className="patch-tool-diff-mode-toggle"
      onClick={(e) => {
        e.stopPropagation();
        onToggle();
      }}
      title={sideBySide ? "Switch to inline diff" : "Switch to side-by-side diff"}
    >
      <svg
        width="14"
        height="14"
        viewBox="0 0 14 14"
        fill="none"
        xmlns="http://www.w3.org/2000/svg"
      >
        {sideBySide ? (
          // Side-by-side icon (two columns)
          <>
            <rect
              x="1"
              y="2"
              width="5"
              height="10"
              rx="1"
              stroke="currentColor"
              strokeWidth="1.5"
              fill="none"
            />
            <rect
              x="8"
              y="2"
              width="5"
              height="10"
              rx="1"
              stroke="currentColor"
              strokeWidth="1.5"
              fill="none"
            />
          </>
        ) : (
          // Inline icon (single column with horizontal lines)
          <>
            <rect
              x="2"
              y="2"
              width="10"
              height="10"
              rx="1"
              stroke="currentColor"
              strokeWidth="1.5"
              fill="none"
            />
            <line x1="4" y1="5" x2="10" y2="5" stroke="currentColor" strokeWidth="1.5" />
            <line x1="4" y1="9" x2="10" y2="9" stroke="currentColor" strokeWidth="1.5" />
          </>
        )}
      </svg>
    </button>
  );
}

function PatchTool({ toolInput, isRunning, toolResult, hasError, display }: PatchToolProps) {
  // Default to collapsed for errors (since agents typically recover), expanded otherwise
  const [isExpanded, setIsExpanded] = useState(!hasError);
  const [isMobile, setIsMobile] = useState(window.innerWidth < 768);
  const [sideBySide, setSideBySide] = useState(() => !isMobile && getSideBySidePreference());

  // Track viewport size
  useEffect(() => {
    const handleResize = () => {
      const mobile = window.innerWidth < 768;
      setIsMobile(mobile);
      // Force unified view on mobile
      if (mobile) {
        setSideBySide(false);
      }
    };
    window.addEventListener("resize", handleResize);
    return () => window.removeEventListener("resize", handleResize);
  }, []);

  // Toggle side-by-side mode
  const toggleSideBySide = useCallback(() => {
    const newValue = !sideBySide;
    setSideBySide(newValue);
    setSideBySidePreference(newValue);
  }, [sideBySide]);

  // Extract path from toolInput
  const path =
    typeof toolInput === "object" &&
    toolInput !== null &&
    "path" in toolInput &&
    typeof toolInput.path === "string"
      ? toolInput.path
      : typeof toolInput === "string"
        ? toolInput
        : "";

  // Accept both {path,diff} (new) and {path,oldContent,newContent,diff} (legacy) payloads.
  const displayData: PatchDisplayData | null =
    display && typeof display === "object" && "path" in display
      ? (display as PatchDisplayData)
      : null;

  // Extract error message from toolResult if present
  const errorMessage =
    toolResult && toolResult.length > 0 && toolResult[0].Text ? toolResult[0].Text : "";

  const isComplete = !isRunning && toolResult !== undefined;

  const hasDiff =
    displayData != null &&
    (displayData.diff || (displayData.oldContent != null && displayData.newContent != null));

  // Extract filename from path or diff headers
  const filename = displayData?.path || path || "patch";

  // Show toggle only on desktop when expanded and complete with diff data
  const showDiffToggle = !isMobile && isExpanded && isComplete && !hasError && hasDiff;

  return (
    <div
      className="patch-tool"
      data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}
    >
      <div className="patch-tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="patch-tool-summary">
          <span className={`patch-tool-emoji ${isRunning ? "running" : ""}`}>🖋️</span>
          <span className="patch-tool-filename">{filename}</span>
          {isComplete && hasError && <span className="patch-tool-error">✗</span>}
          {isComplete && !hasError && <span className="patch-tool-success">✓</span>}
        </div>
        <div className="patch-tool-header-controls">
          {showDiffToggle && <DiffModeToggle sideBySide={sideBySide} onToggle={toggleSideBySide} />}
          <button
            className="patch-tool-toggle"
            aria-label={isExpanded ? "Collapse" : "Expand"}
            aria-expanded={isExpanded}
          >
            <svg
              width="12"
              height="12"
              viewBox="0 0 12 12"
              fill="none"
              xmlns="http://www.w3.org/2000/svg"
              className={`tool-chevron${isExpanded ? " tool-chevron-expanded" : ""}`}
            >
              <path
                d="M4.5 3L7.5 6L4.5 9"
                stroke="currentColor"
                strokeWidth="1.5"
                strokeLinecap="round"
                strokeLinejoin="round"
              />
            </svg>
          </button>
        </div>
      </div>

      {isExpanded && (
        <div className="patch-tool-details">
          {isComplete && !hasError && displayData && hasDiff && (
            <div className="patch-tool-section">
              <DiffView displayData={displayData} sideBySide={sideBySide} />
            </div>
          )}

          {isComplete && hasError && (
            <div className="patch-tool-section">
              <pre className="patch-tool-error-message">{errorMessage || "Patch failed"}</pre>
            </div>
          )}

          {isRunning && (
            <div className="patch-tool-section">
              <div className="patch-tool-label">Applying patch...</div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export default PatchTool;
