import React, { useState } from "react";
import { Message, LLMContent } from "../types";

interface SystemPromptViewProps {
  message: Message;
}

function SystemPromptView({ message }: SystemPromptViewProps) {
  const [isExpanded, setIsExpanded] = useState(false);

  // Extract system prompt text from llm_data
  let systemPromptText = "";
  if (message.llm_data) {
    try {
      const llmData =
        typeof message.llm_data === "string" ? JSON.parse(message.llm_data) : message.llm_data;
      if (llmData && llmData.Content && Array.isArray(llmData.Content)) {
        const textContent = llmData.Content.find((c: LLMContent) => c.Type === 2 && c.Text);
        if (textContent) {
          systemPromptText = textContent.Text;
        }
      }
    } catch (err) {
      console.error("Failed to parse system prompt:", err);
    }
  }

  if (!systemPromptText) {
    return null;
  }

  // Count lines and approximate size
  const lineCount = systemPromptText.split("\n").length;
  const charCount = systemPromptText.length;
  const sizeKb = (charCount / 1024).toFixed(1);

  return (
    <div className="system-prompt-view">
      <div className="system-prompt-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="system-prompt-summary">
          <span className="system-prompt-icon">📋</span>
          <span className="system-prompt-label">System Prompt</span>
          <span className="system-prompt-meta">
            {lineCount} lines, {sizeKb} KB
          </span>
        </div>
        <button
          className="tool-toggle"
          aria-label={isExpanded ? "Collapse" : "Expand"}
          aria-expanded={isExpanded}
        >
          <svg
            width="12"
            height="12"
            viewBox="0 0 12 12"
            fill="none"
            xmlns="http://www.w3.org/2000/svg"
            style={{
              transform: isExpanded ? "rotate(90deg)" : "rotate(0deg)",
              transition: "transform 0.2s",
            }}
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

      {isExpanded && (
        <div className="system-prompt-content">
          <pre className="system-prompt-text">{systemPromptText}</pre>
        </div>
      )}
    </div>
  );
}

export default SystemPromptView;
