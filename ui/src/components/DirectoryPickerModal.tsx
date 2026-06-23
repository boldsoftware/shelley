import React, { useState, useEffect, useRef, useCallback, useId } from "react";
import {
  CheckIcon,
  CornerLeftUpIcon,
  FolderIcon,
  FolderPlusIcon,
  GitBranchIcon,
  Loader2Icon,
  XIcon,
} from "lucide-react";
import { api } from "../services/api";
import Modal from "./Modal";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";

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
  git_repo_root?: string;
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
        git_repo_root: result.git_repo_root,
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

  const handleInputKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    // Don't submit while IME is composing (e.g., converting Japanese hiragana to kanji)
    if (e.nativeEvent.isComposing) {
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      handleSelect();
    }
  };

  const handleSelect = () => {
    // Use the current directory path for selection
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

  // Determine if we should show the parent entry
  const showParent = displayDir?.parent && displayDir.parent !== "";

  return (
    <Modal isOpen={isOpen} onClose={onClose} title="Select Directory" className="sm:max-w-lg">
      <div className="flex flex-col gap-3">
        {/* Path input */}
        <Input
          ref={inputRef}
          type="text"
          value={inputPath}
          onChange={(e) => setInputPath(e.target.value)}
          onKeyDown={handleInputKeyDown}
          placeholder="/path/to/directory"
          className="font-mono"
        />

        {/* Current directory indicator */}
        {displayDir && (
          <div
            className={cn(
              "flex min-w-0 flex-col gap-0.5 rounded-md px-2.5 py-1.5 text-sm",
              displayDir.git_head_subject ? "bg-amber-500/10" : "bg-muted"
            )}
          >
            <span className="truncate font-mono text-foreground">
              {displayDir.path}
              {filterPrefix && <span className="text-muted-foreground">/{filterPrefix}*</span>}
            </span>
            {displayDir.git_head_subject && (
              <span
                className="truncate text-xs text-muted-foreground"
                title={displayDir.git_head_subject}
              >
                {displayDir.git_head_subject}
              </span>
            )}
          </div>
        )}

        {/* Quick-jump buttons to git worktree root / main repo root */}
        {(displayDir?.git_repo_root || displayDir?.git_worktree_root) && (
          <div className="flex flex-col gap-1.5">
            {displayDir.git_repo_root && displayDir.git_repo_root !== displayDir.path && (
              <Button
                variant="outline"
                size="sm"
                className="h-auto justify-start gap-2 py-1.5 text-left font-normal"
                onClick={() => setInputPath(displayDir.git_repo_root + "/")}
                title={displayDir.git_repo_root}
              >
                <GitBranchIcon className="shrink-0 text-muted-foreground" />
                <span className="shrink-0">Go to git worktree root</span>
                <span className="min-w-0 flex-1 truncate font-mono text-xs text-muted-foreground">
                  {displayDir.git_repo_root}
                </span>
              </Button>
            )}
            {displayDir.git_worktree_root && displayDir.git_worktree_root !== displayDir.path && (
              <Button
                variant="outline"
                size="sm"
                className="h-auto justify-start gap-2 py-1.5 text-left font-normal"
                onClick={() => setInputPath(displayDir.git_worktree_root + "/")}
                title={displayDir.git_worktree_root}
              >
                <GitBranchIcon className="shrink-0 text-muted-foreground" />
                <span className="shrink-0">Go to git root</span>
                <span className="min-w-0 flex-1 truncate font-mono text-xs text-muted-foreground">
                  {displayDir.git_worktree_root}
                </span>
              </Button>
            )}
          </div>
        )}

        {/* Error message */}
        {error && (
          <div className="rounded-md bg-destructive/10 px-2.5 py-1.5 text-sm text-destructive">
            {error}
          </div>
        )}

        {/* Loading state */}
        {loading && (
          <div className="flex items-center gap-2 px-1 py-4 text-sm text-muted-foreground">
            <Loader2Icon className="size-4 animate-spin" />
            <span>Loading...</span>
          </div>
        )}

        {/* Directory listing */}
        {!loading && !error && (
          <div className="flex max-h-[40vh] flex-col gap-0.5 overflow-y-auto rounded-md border border-border p-1">
            {/* Parent directory entry */}
            {showParent && (
              <button
                type="button"
                className="flex items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm text-muted-foreground transition-colors hover:bg-muted focus-visible:bg-muted focus-visible:outline-none"
                onClick={handleParentClick}
              >
                <CornerLeftUpIcon className="size-4 shrink-0" />
                <span>..</span>
              </button>
            )}

            {/* Directory entries */}
            {filteredEntries.map((entry) => (
              <button
                key={entry.name}
                type="button"
                className="flex items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm transition-colors hover:bg-muted focus-visible:bg-muted focus-visible:outline-none"
                onClick={() => handleEntryClick(entry)}
              >
                <FolderIcon
                  className={cn(
                    "size-4 shrink-0",
                    entry.git_head_subject ? "text-amber-500" : "text-muted-foreground"
                  )}
                />
                <span className="min-w-0 truncate">
                  {filterPrefix &&
                  entry.name.toLowerCase().startsWith(filterPrefix.toLowerCase()) ? (
                    <>
                      <strong className="font-semibold text-foreground">
                        {entry.name.slice(0, filterPrefix.length)}
                      </strong>
                      {entry.name.slice(filterPrefix.length)}
                    </>
                  ) : (
                    entry.name
                  )}
                </span>
                {entry.git_head_subject && (
                  <span
                    className="ml-auto min-w-0 truncate text-xs text-muted-foreground"
                    title={entry.git_head_subject}
                  >
                    {entry.git_head_subject}
                  </span>
                )}
              </button>
            ))}

            {/* Create new directory inline form */}
            {isCreating && (
              <div className="flex items-center gap-2 px-2 py-1.5">
                <FolderPlusIcon className="size-4 shrink-0 text-muted-foreground" />
                <Label htmlFor={createInputId} className="sr-only">
                  New folder name
                </Label>
                <Input
                  id={createInputId}
                  ref={newDirInputRef}
                  type="text"
                  value={newDirName}
                  onChange={(e) => setNewDirName(e.target.value)}
                  onKeyDown={handleCreateKeyDown}
                  placeholder="New folder name"
                  className="h-7 flex-1"
                  disabled={createLoading}
                />
                <Button
                  variant="ghost"
                  size="icon-sm"
                  onClick={handleCreateDirectory}
                  disabled={createLoading || !newDirName.trim()}
                  title="Create"
                >
                  {createLoading ? (
                    <Loader2Icon className="animate-spin" />
                  ) : (
                    <CheckIcon className="text-emerald-600 dark:text-emerald-500" />
                  )}
                </Button>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  onClick={handleCancelCreate}
                  disabled={createLoading}
                  title="Cancel"
                >
                  <XIcon />
                </Button>
              </div>
            )}

            {/* Create error message */}
            {createError && (
              <div className="px-2 py-1 text-xs text-destructive">{createError}</div>
            )}

            {/* Empty state */}
            {filteredEntries.length === 0 && !showParent && !isCreating && (
              <div className="px-2 py-4 text-center text-sm text-muted-foreground">
                {filterPrefix ? "No matching directories" : "No subdirectories"}
              </div>
            )}
          </div>
        )}

        {/* Footer */}
        <div className="flex items-center gap-2 border-t border-border pt-3">
          {/* New Folder button */}
          {!isCreating && !loading && !error && (
            <Button
              variant="outline"
              size="sm"
              onClick={handleStartCreate}
              title="Create new folder"
            >
              <FolderPlusIcon />
              New Folder
            </Button>
          )}
          <div className="flex-1" />
          <Button variant="outline" size="sm" onClick={onClose}>
            Cancel
          </Button>
          <Button size="sm" onClick={handleSelect} disabled={loading || !!error}>
            Select
          </Button>
        </div>
      </div>
    </Modal>
  );
}

export default DirectoryPickerModal;
