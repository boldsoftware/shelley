import React from "react";
import EditableFileModal from "./EditableFileModal";

interface AgentsMdEditorModalProps {
  isOpen: boolean;
  onClose: () => void;
}

export default function AgentsMdEditorModal({ isOpen, onClose }: AgentsMdEditorModalProps) {
  const filePath = window.__SHELLEY_INIT__?.user_agents_md_path || "";

  return (
    <EditableFileModal
      isOpen={isOpen}
      path={filePath}
      title="Edit AGENTS.md"
      loadUrl="/api/user-agents-md"
      onClose={onClose}
    />
  );
}
