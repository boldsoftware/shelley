import katex from "katex";
import type * as marked from "marked";

interface MathNode {
  element: HTMLElement;
  text: string;
  displayMode: boolean;
}

export function createMathExtension(): marked.MarkedExtension {
  return {
    extensions: [
      {
        name: "math",
        level: "block",
        start(source) {
          return source.search(/^ {0,3}\$\$/m);
        },
        tokenizer(source) {
          const match = /^ {0,3}\$\$((?:\\[\s\S]|[^\\$]|\$(?!\$))*)\$\$[ \t]*(?:\n|$)/.exec(source);
          if (match) {
            return { type: "math", raw: match[0], text: match[1], displayMode: true };
          }
        },
        renderer(token) {
          const element = document.createElement("span");
          element.dataset.shelleyMath = token.displayMode ? "display" : "inline";
          element.textContent = token.text;
          return element.outerHTML;
        },
      },
      {
        name: "math",
        level: "inline",
        start(source) {
          return source.indexOf("$");
        },
        tokenizer(source) {
          if (this.lexer.state.inRawBlock || this.lexer.state.inLink) return;
          const displayMode = source.startsWith("$$");
          const match = (
            displayMode
              ? /^\$\$((?:\\[\s\S]|[^\\$]|\$(?!\$))*)\$\$/
              : /^\$(?!\s)((?:\\[^\n]|[^\\$`\n])+?)(?<!\s)\$(?![\d$])/
          ).exec(source);
          if (match) return { type: "math", raw: match[0], text: match[1], displayMode };
          if (displayMode) return { type: "text", raw: source, text: source };
        },
      },
    ],
  };
}

export function collectMath(root: ParentNode): MathNode[] {
  const nodes: MathNode[] = [];
  for (const element of root.querySelectorAll<HTMLElement>("span[data-shelley-math]")) {
    const mode = element.getAttribute("data-shelley-math");
    element.removeAttribute("data-shelley-math");
    if ((mode === "inline" || mode === "display") && !element.closest("pre, code, a")) {
      nodes.push({ element, text: element.textContent ?? "", displayMode: mode === "display" });
    }
  }
  return nodes;
}

export function renderMath(nodes: MathNode[]) {
  for (const { element, text, displayMode } of nodes) {
    katex.render(text, element, {
      displayMode,
      trust: false,
      throwOnError: false,
      maxSize: 20,
    });
  }
}
