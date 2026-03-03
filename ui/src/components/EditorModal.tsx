import React, { useState, useEffect, useCallback } from "react";
import { useEscapeClose } from "./useEscapeClose";

type Editor = "vscode" | "cursor" | "zed";

interface EditorModalProps {
  isOpen: boolean;
  onClose: () => void;
  hostname: string;
  cwd: string;
}

function editorURL(editor: Editor, hostname: string, path: string): string {
  switch (editor) {
    case "vscode":
      return `vscode://vscode-remote/ssh-remote+${hostname}${path}?windowId=_blank`;
    case "cursor":
      return `cursor://vscode-remote/ssh-remote+${hostname}${path}?windowId=_blank`;
    case "zed":
      return `zed://ssh/${hostname}${path}`;
  }
}

const EDITOR_LABELS: Record<Editor, string> = {
  vscode: "VS Code",
  cursor: "Cursor",
  zed: "Zed",
};

const EDITORS: Editor[] = ["vscode", "cursor", "zed"];

function EditorModal({ isOpen, onClose, hostname, cwd }: EditorModalProps) {
  const [editor, setEditor] = useState<Editor>(() => {
    const saved = localStorage.getItem("shelley-preferred-editor");
    if (saved && EDITORS.includes(saved as Editor)) return saved as Editor;
    return "vscode";
  });
  const [workingDir, setWorkingDir] = useState(cwd);
  const [copied, setCopied] = useState(false);

  useEscapeClose(isOpen, useCallback(() => onClose(), [onClose]));

  useEffect(() => {
    if (isOpen) {
      setWorkingDir(cwd);
      setCopied(false);
    }
  }, [isOpen, cwd]);

  useEffect(() => {
    localStorage.setItem("shelley-preferred-editor", editor);
  }, [editor]);

  if (!isOpen) return null;

  const url = editorURL(editor, hostname, workingDir);

  const handleBackdropClick = (e: React.MouseEvent) => {
    if (e.target === e.currentTarget) onClose();
  };

  const handleCopy = async () => {
    await navigator.clipboard.writeText(url);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <div className="modal-overlay" onClick={handleBackdropClick}>
      <div className="modal editor-modal">
        <div className="modal-header">
          <h2 className="modal-title">Open in Editor</h2>
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

        <div className="modal-body">
          <label className="editor-modal-label">Editor</label>
          <div className="editor-modal-toggle">
            {EDITORS.map((e) => (
              <button
                key={e}
                className={`editor-modal-toggle-btn${editor === e ? " editor-modal-toggle-btn-active" : ""}`}
                onClick={() => setEditor(e)}
              >
                {EDITOR_LABELS[e]}
              </button>
            ))}
          </div>

          <label className="editor-modal-label">Working Directory</label>
          <input
            type="text"
            className="editor-modal-input"
            value={workingDir}
            onChange={(e) => setWorkingDir(e.target.value)}
          />

          <div className="editor-modal-url">{url}</div>
        </div>

        <div className="editor-modal-footer">
          <button
            className="btn-primary editor-modal-action-btn"
            onClick={() => { window.open(url, "_blank"); onClose(); }}
          >
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" style={{ width: "1rem", height: "1rem", marginRight: "0.5rem" }}>
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M10 6H6a2 2 0 00-2 2v10a2 2 0 002 2h10a2 2 0 002-2v-4M14 4h6m0 0v6m0-6L10 14" />
            </svg>
            Open Editor
          </button>
          <button className="btn editor-modal-action-btn" onClick={handleCopy}>
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" style={{ width: "1rem", height: "1rem", marginRight: "0.5rem" }}>
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z" />
            </svg>
            {copied ? "Copied!" : "Copy Link"}
          </button>
        </div>
      </div>
    </div>
  );
}

export default EditorModal;
