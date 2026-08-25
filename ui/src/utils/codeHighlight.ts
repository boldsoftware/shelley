import type { HighlightLines } from "../services/markdownHighlight";

export function applyHighlightTokens(
  code: HTMLElement,
  source: string,
  lines: HighlightLines,
): void {
  // Shiki must never be able to alter what the model wrote. Its output is
  // structured token data rather than HTML; verify it before constructing DOM.
  if (lines.map((line) => line.map((token) => token.content).join("")).join("\n") !== source) {
    throw new Error("Syntax highlighter changed code text");
  }

  const fragment = document.createDocumentFragment();
  for (let lineIndex = 0; lineIndex < lines.length; lineIndex++) {
    for (const token of lines[lineIndex]) {
      const span = document.createElement("span");
      span.className = "shelley-code-token";
      span.style.setProperty("--shelley-code-light", token.light);
      span.style.setProperty("--shelley-code-dark", token.dark);
      if (token.fontStyle & 1) span.classList.add("shelley-code-italic");
      if (token.fontStyle & 2) span.classList.add("shelley-code-bold");
      if (token.fontStyle & 4) span.classList.add("shelley-code-underlined");
      span.textContent = token.content;
      fragment.append(span);
    }
    if (lineIndex < lines.length - 1) fragment.append("\n");
  }
  code.replaceChildren(fragment);
}
