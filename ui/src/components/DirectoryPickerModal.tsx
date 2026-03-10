import React, { useState, useEffect, useRef, useCallback, useId } from "react";
import { api } from "../services/api";
import { useEscapeClose } from "./useEscapeClose";

interface DirectoryEntry {
  name: string;
  is_dir: boolean;
  git_head_subject?: string;
}

interface CachedDirectory {
  path: string;
  parent: string;
  entries: DirectoryEntry[];
  git_head_subject?: string;
  git_worktree_root?: string;
}

interface DirectoryPickerModalProps {
  isOpen: boolean;
  onClose: () => void;
  onSelect: (path: string) => void;
  initialPath?: string;
  foldersOnly?: boolean; // If true, only show directories (hide files)
}

function DirectoryPickerModal({
  isOpen,
  onClose,
  onSelect,
  initialPath,
  foldersOnly,
}: DirectoryPickerModalProps) {
  const [inputPath, setInputPath] = useState(() => {
    if (!initialPath) return "";
    return initialPath.endsWith("/") ? initialPath : initialPath + "/";
  });
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  // State for create directory mode
  const [isCreating, setIsCreating] = useState(false);
  const [newDirName, setNewDirName] = useState("");
  const [createError, setCreateError] = useState<string | null>(null);
  const [createLoading, setCreateLoading] = useState(false);
  const newDirInputRef = useRef<HTMLInputElement>(null);
  const createInputId = useId();

  // Cache for directory listings
  const cacheRef = useRef<Map<string, CachedDirectory>>(new Map());

  // Current directory being displayed (the parent directory of what's being typed)
  const [displayDir, setDisplayDir] = useState<CachedDirectory | null>(null);
  // Filter prefix (the part after the last slash that we're filtering by)
  const [filterPrefix, setFilterPrefix] = useState("");

  // Parse input path into directory and filter prefix
  const parseInputPath = useCallback((path: string): { dirPath: string; prefix: string } => {
    if (!path) {
      return { dirPath: "", prefix: "" };
    }

    // If path ends with /, we're looking at contents of that directory
    if (path.endsWith("/")) {
      return { dirPath: path.slice(0, -1) || "/", prefix: "" };
    }

    // Otherwise, split into directory and prefix
    const lastSlash = path.lastIndexOf("/");
    if (lastSlash === -1) {
      // No slash, treat as prefix in current directory
      return { dirPath: "", prefix: path };
    }
    if (lastSlash === 0) {
      // Root directory with prefix
      return { dirPath: "/", prefix: path.slice(1) };
    }
    return {
      dirPath: path.slice(0, lastSlash),
      prefix: path.slice(lastSlash + 1),
    };
  }, []);

  // Load directory from cache or API
  const loadDirectory = useCallback(async (path: string): Promise<CachedDirectory | null> => {
    const normalizedPath = path || "/";

    // Check cache first
    const cached = cacheRef.current.get(normalizedPath);
    if (cached) {
      return cached;
    }

    // Load from API
    setLoading(true);
    setError(null);
    try {
      const result = await api.listDirectory(path || undefined);
      if (result.error) {
        setError(result.error);
        return null;
      }

      const dirData: CachedDirectory = {
        path: result.path,
        parent: result.parent,
        entries: result.entries || [],
        git_head_subject: result.git_head_subject,
        git_worktree_root: result.git_worktree_root,
      };

      // Cache it
      cacheRef.current.set(result.path, dirData);

      return dirData;
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load directory");
      return null;
    } finally {
      setLoading(false);
    }
  }, []);

  // Track the current expected path to avoid race conditions
  const expectedPathRef = useRef<string>("");

  // Update display when input changes
  useEffect(() => {
    if (!isOpen) return;

    const { dirPath, prefix } = parseInputPath(inputPath);
    setFilterPrefix(prefix);

    // Track which path we expect to display
    const normalizedDirPath = dirPath || "/";
    expectedPathRef.current = normalizedDirPath;

    // Load the directory
    loadDirectory(dirPath).then((dir) => {
      // Only update if this is still the path we want
      if (dir && expectedPathRef.current === normalizedDirPath) {
        setDisplayDir(dir);
        setError(null);
      }
    });
  }, [isOpen, inputPath, parseInputPath, loadDirectory]);

  // Initialize when modal opens
  useEffect(() => {
    if (isOpen) {
      if (!initialPath) {
        setInputPath("");
      } else {
        setInputPath(initialPath.endsWith("/") ? initialPath : initialPath + "/");
      }
      // Clear cache on open to get fresh data
      cacheRef.current.clear();
    }
  }, [isOpen, initialPath]);

  // Focus input when modal opens (but not on mobile to avoid keyboard popup)
  useEffect(() => {
    if (isOpen && inputRef.current) {
      // Check if mobile device (touch-based)
      const isMobile = window.matchMedia("(max-width: 768px)").matches || "ontouchstart" in window;
      if (!isMobile) {
        inputRef.current.focus();
        // Move cursor to end
        const len = inputRef.current.value.length;
        inputRef.current.setSelectionRange(len, len);
      }
    }
  }, [isOpen]);

  // Filter entries based on prefix (case-insensitive)
  const filteredEntries =
    displayDir?.entries.filter((entry) => {
      if (foldersOnly && !entry.is_dir) return false;
      if (!filterPrefix) return true;
      return entry.name.toLowerCase().startsWith(filterPrefix.toLowerCase());
    }) || [];

  const handleEntryClick = (entry: DirectoryEntry) => {
    if (entry.is_dir) {
      const basePath = displayDir?.path || "";
      const newPath = basePath === "/" ? `/${entry.name}/` : `${basePath}/${entry.name}/`;
      setInputPath(newPath);
    }
  };

  const handleParentClick = () => {
    if (displayDir?.parent) {
      const newPath = displayDir.parent === "/" ? "/" : `${displayDir.parent}/`;
      setInputPath(newPath);
    }
  };

  // Track which entry is highlighted for keyboard navigation
  const [selectedEntryIndex, setSelectedEntryIndex] = useState(-1);

  // Reset selection when filter changes
  useEffect(() => {
    setSelectedEntryIndex(-1);
  }, [filterPrefix, displayDir?.path]);

  // Scroll selected entry into view
  useEffect(() => {
    if (selectedEntryIndex >= 0 && listRef.current) {
      const el = listRef.current.querySelector(`[data-entry-index="${selectedEntryIndex}"]`);
      el?.scrollIntoView({ block: "nearest" });
    }
  }, [selectedEntryIndex]);

  const handleInputKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    // Don't submit while IME is composing (e.g., converting Japanese hiragana to kanji)
    if (e.nativeEvent.isComposing) {
      return;
    }
    if (e.key === "Tab" && filteredEntries.length > 0) {
      // Tab-complete first matching entry
      e.preventDefault();
      const idx = selectedEntryIndex >= 0 ? selectedEntryIndex : 0;
      const entry = filteredEntries[idx];
      if (entry?.is_dir) {
        const basePath = displayDir?.path || "";
        const newPath = basePath === "/" ? `/${entry.name}/` : `${basePath}/${entry.name}/`;
        setInputPath(newPath);
      }
      return;
    }
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setSelectedEntryIndex((prev) => Math.min(prev + 1, filteredEntries.length - 1));
      return;
    }
    if (e.key === "ArrowUp") {
      e.preventDefault();
      setSelectedEntryIndex((prev) => Math.max(prev - 1, -1));
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      handleSelect();
    }
  };

  const handleSelect = () => {
    // If an entry is explicitly selected via arrow keys, use it
    if (selectedEntryIndex >= 0 && filteredEntries[selectedEntryIndex]) {
      const entry = filteredEntries[selectedEntryIndex];
      if (entry.is_dir) {
        const basePath = displayDir?.path || "";
        const selectedPath = basePath === "/" ? `/${entry.name}` : `${basePath}/${entry.name}`;
        onSelect(selectedPath);
        onClose();
        return;
      }
    }

    // If there's a filter prefix and exactly one matching directory, select it
    if (filterPrefix) {
      const matchingDirs = filteredEntries.filter((e) => e.is_dir);
      if (matchingDirs.length === 1) {
        const basePath = displayDir?.path || "";
        const selectedPath =
          basePath === "/" ? `/${matchingDirs[0].name}` : `${basePath}/${matchingDirs[0].name}`;
        onSelect(selectedPath);
        onClose();
        return;
      }
      // Multiple matches — navigate into the first one (like shell tab completion)
      if (matchingDirs.length > 0) {
        const basePath = displayDir?.path || "";
        const selectedPath =
          basePath === "/" ? `/${matchingDirs[0].name}` : `${basePath}/${matchingDirs[0].name}`;
        onSelect(selectedPath);
        onClose();
        return;
      }
    }

    // No filter — select the current directory
    const { dirPath } = parseInputPath(inputPath);
    const selectedPath = inputPath.endsWith("/") ? (dirPath === "/" ? "/" : dirPath) : dirPath;
    onSelect(selectedPath || displayDir?.path || "");
    onClose();
  };

  // Focus the new directory input when entering create mode
  useEffect(() => {
    if (isCreating && newDirInputRef.current) {
      newDirInputRef.current.focus();
    }
  }, [isCreating]);

  const handleStartCreate = () => {
    setIsCreating(true);
    setNewDirName("");
    setCreateError(null);
  };

  const handleCancelCreate = () => {
    setIsCreating(false);
    setNewDirName("");
    setCreateError(null);
  };

  const handleCreateDirectory = async () => {
    if (!newDirName.trim()) {
      setCreateError("Directory name is required");
      return;
    }

    // Validate directory name (no path separators or special chars)
    if (newDirName.includes("/") || newDirName.includes("\\")) {
      setCreateError("Directory name cannot contain slashes");
      return;
    }

    const basePath = displayDir?.path || "/";
    const newPath = basePath === "/" ? `/${newDirName}` : `${basePath}/${newDirName}`;

    setCreateLoading(true);
    setCreateError(null);

    try {
      const result = await api.createDirectory(newPath);
      if (result.error) {
        setCreateError(result.error);
        return;
      }

      // Clear the cache for the current directory so it reloads with the new dir
      cacheRef.current.delete(basePath);

      // Exit create mode and navigate to the new directory
      setIsCreating(false);
      setNewDirName("");
      setInputPath(newPath + "/");
    } catch (err) {
      setCreateError(err instanceof Error ? err.message : "Failed to create directory");
    } finally {
      setCreateLoading(false);
    }
  };

  const handleCreateKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.nativeEvent.isComposing) return;
    if (e.key === "Enter") {
      e.preventDefault();
      handleCreateDirectory();
    } else if (e.key === "Escape") {
      e.preventDefault();
      handleCancelCreate();
    }
  };

  useEscapeClose(isOpen, onClose);

  const handleBackdropClick = (e: React.MouseEvent) => {
    if (e.target === e.currentTarget) {
      onClose();
    }
  };

  if (!isOpen) return null;

  // Determine if we should show the parent entry
  const showParent = displayDir?.parent && displayDir.parent !== "";

  return (
    <div className="modal-overlay" onClick={handleBackdropClick}>
      <div className="modal directory-picker-modal">
        {/* Header */}
        <div className="modal-header">
          <h2 className="modal-title">Select Directory</h2>
          <button onClick={onClose} className="btn-icon" aria-label="Close modal">
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M6 18L18 6M6 6l12 12"
              />
            </svg>
          </button>
        </div>

        {/* Content */}
        <div className="modal-body directory-picker-body">
          {/* Path input */}
          <div className="directory-picker-input-container">
            <input
              ref={inputRef}
              type="text"
              value={inputPath}
              onChange={(e) => setInputPath(e.target.value)}
              onKeyDown={handleInputKeyDown}
              className="directory-picker-input"
              placeholder="/path/to/directory"
            />
          </div>

          {/* Current directory indicator */}
          {displayDir && (
            <div
              className={`directory-picker-current${displayDir.git_head_subject ? " directory-picker-current-git" : ""}`}
            >
              <span className="directory-picker-current-path">
                {displayDir.path}
                {filterPrefix && <span className="directory-picker-filter">/{filterPrefix}*</span>}
              </span>
              {displayDir.git_head_subject && (
                <span
                  className="directory-picker-current-subject"
                  title={displayDir.git_head_subject}
                >
                  {displayDir.git_head_subject}
                </span>
              )}
            </div>
          )}

          {/* Go to git root button for worktrees */}
          {displayDir?.git_worktree_root && (
            <button
              className="directory-picker-git-root-btn"
              onClick={() => setInputPath(displayDir.git_worktree_root + "/")}
            >
              <svg
                fill="none"
                stroke="currentColor"
                viewBox="0 0 24 24"
                className="directory-picker-icon"
              >
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M3 10h10a8 8 0 018 8v2M3 10l6 6m-6-6l6-6"
                />
              </svg>
              <span>Go to git root</span>
              <span className="directory-picker-git-root-path">{displayDir.git_worktree_root}</span>
            </button>
          )}

          {/* Error message */}
          {error && <div className="directory-picker-error">{error}</div>}

          {/* Loading state */}
          {loading && (
            <div className="directory-picker-loading">
              <div className="spinner spinner-small"></div>
              <span>Loading...</span>
            </div>
          )}

          {/* Directory listing */}
          {!loading && !error && (
            <div className="directory-picker-list" ref={listRef}>
              {/* Parent directory entry */}
              {showParent && (
                <button
                  className="directory-picker-entry directory-picker-entry-parent"
                  onClick={handleParentClick}
                >
                  <svg
                    fill="none"
                    stroke="currentColor"
                    viewBox="0 0 24 24"
                    className="directory-picker-icon"
                  >
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeWidth={2}
                      d="M11 17l-5-5m0 0l5-5m-5 5h12"
                    />
                  </svg>
                  <span>..</span>
                </button>
              )}

              {/* Directory entries */}
              {filteredEntries.map((entry, index) => (
                <button
                  key={entry.name}
                  className={`directory-picker-entry${entry.git_head_subject ? " directory-picker-entry-git" : ""}${index === selectedEntryIndex ? " directory-picker-entry-selected" : ""}`}
                  onClick={() => handleEntryClick(entry)}
                  data-entry-index={index}
                  onMouseEnter={() => setSelectedEntryIndex(index)}
                >
                  <svg
                    fill="none"
                    stroke="currentColor"
                    viewBox="0 0 24 24"
                    className="directory-picker-icon"
                  >
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeWidth={2}
                      d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"
                    />
                  </svg>
                  <span className="directory-picker-entry-name">
                    {filterPrefix &&
                    entry.name.toLowerCase().startsWith(filterPrefix.toLowerCase()) ? (
                      <>
                        <strong>{entry.name.slice(0, filterPrefix.length)}</strong>
                        {entry.name.slice(filterPrefix.length)}
                      </>
                    ) : (
                      entry.name
                    )}
                  </span>
                  {entry.git_head_subject && (
                    <span className="directory-picker-git-subject" title={entry.git_head_subject}>
                      {entry.git_head_subject}
                    </span>
                  )}
                </button>
              ))}

              {/* Create new directory inline form */}
              {isCreating && (
                <div className="directory-picker-create-form">
                  <svg
                    fill="none"
                    stroke="currentColor"
                    viewBox="0 0 24 24"
                    className="directory-picker-icon"
                  >
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeWidth={2}
                      d="M9 13h6m-3-3v6m-9 1V7a2 2 0 012-2h6l2 2h6a2 2 0 012 2v8a2 2 0 01-2 2H5a2 2 0 01-2-2z"
                    />
                  </svg>
                  <label htmlFor={createInputId} className="sr-only">
                    New folder name
                  </label>
                  <input
                    id={createInputId}
                    ref={newDirInputRef}
                    type="text"
                    value={newDirName}
                    onChange={(e) => setNewDirName(e.target.value)}
                    onKeyDown={handleCreateKeyDown}
                    placeholder="New folder name"
                    className="directory-picker-create-input"
                    disabled={createLoading}
                  />
                  <button
                    className="directory-picker-create-btn"
                    onClick={handleCreateDirectory}
                    disabled={createLoading || !newDirName.trim()}
                    title="Create"
                  >
                    {createLoading ? (
                      <div className="spinner spinner-small"></div>
                    ) : (
                      <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                        <path
                          strokeLinecap="round"
                          strokeLinejoin="round"
                          strokeWidth={2}
                          d="M5 13l4 4L19 7"
                        />
                      </svg>
                    )}
                  </button>
                  <button
                    className="directory-picker-create-btn directory-picker-cancel-btn"
                    onClick={handleCancelCreate}
                    disabled={createLoading}
                    title="Cancel"
                  >
                    <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                      <path
                        strokeLinecap="round"
                        strokeLinejoin="round"
                        strokeWidth={2}
                        d="M6 18L18 6M6 6l12 12"
                      />
                    </svg>
                  </button>
                </div>
              )}

              {/* Create error message */}
              {createError && <div className="directory-picker-create-error">{createError}</div>}

              {/* Empty state */}
              {filteredEntries.length === 0 && !showParent && !isCreating && (
                <div className="directory-picker-empty">
                  {filterPrefix ? "No matching directories" : "No subdirectories"}
                </div>
              )}
            </div>
          )}
        </div>

        {/* Footer */}
        <div className="directory-picker-footer">
          {/* New Folder button */}
          {!isCreating && !loading && !error && (
            <button
              className="btn directory-picker-new-btn"
              onClick={handleStartCreate}
              title="Create new folder"
            >
              <svg
                fill="none"
                stroke="currentColor"
                viewBox="0 0 24 24"
                className="directory-picker-new-icon"
              >
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M9 13h6m-3-3v6m-9 1V7a2 2 0 012-2h6l2 2h6a2 2 0 012 2v8a2 2 0 01-2 2H5a2 2 0 01-2-2z"
                />
              </svg>
              New Folder
            </button>
          )}
          <div className="directory-picker-footer-spacer"></div>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn-primary" onClick={handleSelect} disabled={loading || !!error}>
            Select
          </button>
        </div>
      </div>
    </div>
  );
}

export default DirectoryPickerModal;
