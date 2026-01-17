import React from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { linkifyText } from "../utils/linkify";

interface MarkdownProps {
  children: string;
}

/**
 * Renders markdown content with support for GFM (GitHub Flavored Markdown)
 * including tables, strikethrough, task lists, and auto-linking URLs.
 */
export function Markdown({ children }: MarkdownProps) {
  // For very short content without any markdown indicators, use simple text rendering
  // This avoids wrapping simple text in <p> tags
  const hasMarkdown = /[|#*_`~\[\]\n]/.test(children);
  if (!hasMarkdown && !children.includes("http")) {
    return <span className="whitespace-pre-wrap break-words">{children}</span>;
  }

  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      components={{
        // Custom link rendering to open in new tab
        a: ({ href, children }) => (
          <a
            href={href}
            target="_blank"
            rel="noopener noreferrer"
            className="text-link"
          >
            {children}
          </a>
        ),
        // Preserve code blocks styling
        code: ({ className, children, ...props }) => {
          const isInline = !className;
          if (isInline) {
            return (
              <code className="inline-code" {...props}>
                {children}
              </code>
            );
          }
          return (
            <code className={className} {...props}>
              {children}
            </code>
          );
        },
        // Style pre blocks
        pre: ({ children }) => (
          <pre className="code-block">{children}</pre>
        ),
        // Style tables
        table: ({ children }) => (
          <div className="table-wrapper">
            <table className="markdown-table">{children}</table>
          </div>
        ),
        // Render plain text nodes with linkify for URLs not caught by markdown
        text: ({ children }) => {
          if (typeof children === "string") {
            return <>{linkifyText(children)}</>;
          }
          return <>{children}</>;
        },
      }}
    >
      {children}
    </ReactMarkdown>
  );
}

export default Markdown;
