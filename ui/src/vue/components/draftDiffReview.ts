import type { editor } from "monaco-editor";

export interface NewDraftDiffComment {
  cwd: string;
  root: string;
  machine: string;
  diff: string;
  to: string;
  path: string;
  side: "left" | "right";
  line: number | null;
  endLine: number | null;
  quote: string;
  text: string;
  snapshot: string | null;
}

export interface DraftDiffComment extends NewDraftDiffComment {
  id: number;
}

export function draftCommentLocation(note: NewDraftDiffComment): string {
  const path = note.path.startsWith("commit-message:")
    ? `Commit ${note.path.slice("commit-message:".length, "commit-message:".length + 8)}`
    : note.path;
  if (note.line === null) return path;
  const lines = note.endLine === note.line ? note.line : `${note.line}–${note.endLine}`;
  return `${path}:${lines} (${note.side === "left" ? "old" : "new"})`;
}

export function formatDraftDiffReview(notes: readonly DraftDiffComment[]): string {
  return notes
    .map((note) => {
      const repo = note.machine ? `VM ${note.machine}:${note.root}` : note.root;
      const range = note.diff === "working" ? "working changes" : `${note.diff} → ${note.to}`;
      const lines = note.quote
        ? "\n" +
          note.quote
            .split("\n")
            .map((line) => `> ${line}`)
            .join("\n")
        : "";
      return `> ${repo} · ${range}\n> ${draftCommentLocation(note)}${lines}\n\n${note.text}`;
    })
    .join("\n\n");
}

export function mountDraftDiffComments(
  codeEditor: editor.ICodeEditor,
  notes: readonly DraftDiffComment[],
): () => void {
  const model = codeEditor.getModel();
  if (!model || !notes.length) return () => {};
  const zones = new Map<Element, { id: string; zone: editor.IViewZone }>();
  const observer = new ResizeObserver((entries) => {
    if (codeEditor.getModel() !== model) return;
    codeEditor.changeViewZones((accessor) => {
      for (const { target } of entries) {
        const item = zones.get(target);
        if (!item) continue;
        const height = Math.ceil(target.getBoundingClientRect().height);
        if (height === item.zone.heightInPx) continue;
        item.zone.heightInPx = height;
        accessor.layoutZone(item.id);
      }
    });
  });
  codeEditor.changeViewZones((accessor) => {
    for (const note of notes) {
      if (note.endLine === null || note.endLine > model.getLineCount()) continue;
      const host = document.createElement("div");
      const stack = document.createElement("div");
      stack.className = "diff-draft-inline-stack";
      const card = document.createElement("div");
      card.className = "diff-draft-inline";
      const label = document.createElement("strong");
      label.textContent = "Draft comment";
      const body = document.createElement("div");
      body.textContent = note.text;
      card.append(label, body);
      stack.append(card);
      host.append(stack);
      const zone = { afterLineNumber: note.endLine, domNode: host, heightInPx: 64 };
      zones.set(stack, { id: accessor.addZone(zone), zone });
      observer.observe(stack);
    }
  });
  return () => {
    observer.disconnect();
    if (codeEditor.getModel() !== model) return;
    codeEditor.changeViewZones((accessor) => {
      for (const { id } of zones.values()) accessor.removeZone(id);
    });
  };
}
