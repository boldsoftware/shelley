import React, { useState } from "react";

interface ThinkingContentProps {
  thinking: string;
}

function ThinkingContent({ thinking }: ThinkingContentProps) {
  const [isExpanded, setIsExpanded] = useState(true);

  // Truncate thinking for display - get first 80 chars
  const truncateThinking = (text: string, maxLen: number = 80) => {
    if (!text) return "";
    const firstLine = text.split("\n")[0];
    if (firstLine.length <= maxLen) return firstLine;
    return firstLine.substring(0, maxLen) + "...";
  };

  const preview = truncateThinking(thinking);

  return (
    <div className="thinking-content thinking-content-wrapper" data-testid="thinking-content">
      <div onClick={() => setIsExpanded(!isExpanded)} className="thinking-clickable-area">
        <span className="thinking-emoji">💭</span>
        <div className="thinking-text">{isExpanded ? thinking : preview}</div>
        <button
          className="thinking-toggle thinking-toggle-button"
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
  );
}

export default ThinkingContent;
