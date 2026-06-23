import React, { useMemo } from "react";
import {
  renderMarkdownToSafeHTML,
  classifyImageSrc,
  fileEndpointURL,
} from "../utils/markdownRender";

// The pure markdown pipeline now lives in utils/markdownRender.ts so it can be
// shared by the Vue port. Re-export the functions the test imports from here
// (components/MarkdownContent.test.ts) so the React-side test keeps passing.
export { renderMarkdownToSafeHTML, classifyImageSrc, fileEndpointURL };

interface MarkdownContentProps {
  text: string;
  // When set, local-path markdown images (relative or absolute file paths) are
  // rewritten to the per-message file endpoint and rendered. Without it we
  // cannot authorize a local file, so such images are dropped.
  messageId?: string;
}

function MarkdownContent({ text, messageId }: MarkdownContentProps) {
  const html = useMemo(() => renderMarkdownToSafeHTML(text, messageId), [text, messageId]);

  return (
    <div className="markdown-content break-words" dangerouslySetInnerHTML={{ __html: html }} />
  );
}

export default MarkdownContent;
