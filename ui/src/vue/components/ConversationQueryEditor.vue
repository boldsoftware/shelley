<template>
  <EditorContent :editor="editor" class="conversation-query-editor-host" />
</template>

<script setup lang="ts">
import { mergeAttributes, Node } from "@tiptap/core";
import type { FocusPosition, JSONContent } from "@tiptap/core";
import Document from "@tiptap/extension-document";
import Paragraph from "@tiptap/extension-paragraph";
import Text from "@tiptap/extension-text";
import { UndoRedo } from "@tiptap/extensions";
import type { Node as ProseMirrorNode } from "@tiptap/pm/model";
import { TextSelection } from "@tiptap/pm/state";
import { Editor, EditorContent } from "@tiptap/vue-3";
import { nextTick, onBeforeUnmount, watch } from "vue";
import {
  completeStructuredQueryEdit,
  rebaseConversationQuerySelection,
  removeConversationQueryTerm,
  structuredQueryEditAtOffset,
  tokenizeConversationQuery,
  type ActiveStructuredQueryEdit,
  type ConversationQuerySelection,
} from "../../utils/conversationQuery";
import { isImeComposing } from "../../utils/imeComposing";

const props = defineProps<{
  modelValue: string;
  placeholder: string;
  ariaLabelText: string;
}>();

const emit = defineEmits<{
  (event: "update:modelValue", value: string): void;
  (event: "keydown", value: KeyboardEvent): void;
  (event: "structured-edit-change", value: ActiveStructuredQueryEdit | null): void;
}>();

const QueryToken = Node.create({
  name: "queryToken",
  group: "inline",
  inline: true,
  atom: true,
  selectable: false,

  addAttributes() {
    return {
      kind: { default: "tag" },
      raw: { default: "" },
    };
  },

  parseHTML() {
    return [{ tag: "span[data-conversation-query-token]" }];
  },

  renderHTML({ node, HTMLAttributes }) {
    const raw = String(node.attrs.raw);
    const colon = raw.indexOf(":") + 1;
    const kind = String(node.attrs.kind);
    return [
      "span",
      mergeAttributes(HTMLAttributes, {
        "data-conversation-query-token": kind,
        "data-query-raw": raw,
        class: `conversation-query-token conversation-query-token-${kind}`,
      }),
      ["span", { class: "conversation-query-token-prefix" }, colon > 0 ? raw.slice(0, colon) : raw],
      ["span", { class: "conversation-query-token-value" }, colon > 0 ? raw.slice(colon) : ""],
      [
        "button",
        {
          type: "button",
          tabindex: "-1",
          class: "conversation-query-token-delete",
          "aria-label": `Remove ${raw}`,
        },
      ],
    ];
  },

  renderText({ node }) {
    return String(node.attrs.raw);
  },
});

const QueryDocument = Document.extend({ content: "paragraph" });

let localRaw = props.modelValue;
let activeEdit: ActiveStructuredQueryEdit | null = null;
let applyingExternalValue = false;

function queryDocument(raw: string): JSONContent {
  const content: JSONContent[] = [];
  for (const token of tokenizeConversationQuery(raw)) {
    if (!token.raw) continue;
    if (token.kind === "text") {
      content.push({ type: "text", text: token.raw });
    } else {
      content.push({
        type: "queryToken",
        attrs: { kind: token.kind, raw: token.raw },
      });
    }
  }
  return {
    type: "doc",
    content: [{ type: "paragraph", ...(content.length > 0 ? { content } : {}) }],
  };
}

function nodeRaw(node: ProseMirrorNode): string {
  if (node.type.name === "queryToken") return String(node.attrs.raw);
  return node.isText ? (node.text ?? "") : "";
}

// Raw text of the document up to one position, resolving a position inside
// an atomic token to that token's end. Serves as both serializer and
// document -> raw caret mapping.
function rawBefore(doc: ProseMirrorNode, position: number): string {
  return doc.textBetween(0, Math.min(doc.content.size, position), "", nodeRaw);
}

// Document position after the token at `position` and one following space.
function afterTokenSeparator(doc: ProseMirrorNode, position: number): number {
  let end = position + (doc.nodeAt(position)?.nodeSize ?? 0);
  const following = doc.resolve(end).nodeAfter;
  if (following?.isText && following.text?.startsWith(" ")) end += 1;
  return end;
}

function rawPositionToDocument(doc: ProseMirrorNode, position: number): number {
  const paragraph = doc.firstChild;
  if (!paragraph) return 1;
  const target = Math.max(0, position);
  let documentOffset = 1;
  let rawOffset = 0;
  for (let index = 0; index < paragraph.childCount; index += 1) {
    const node = paragraph.child(index);
    const raw = nodeRaw(node);
    if (target <= rawOffset) return documentOffset;
    if (target <= rawOffset + raw.length) {
      if (node.isText) return documentOffset + target - rawOffset;
      return documentOffset + node.nodeSize;
    }
    documentOffset += node.nodeSize;
    rawOffset += raw.length;
  }
  return documentOffset;
}

function currentSelection(): ConversationQuerySelection {
  const { anchor, head } = editor.state.selection;
  return {
    anchor: rawBefore(editor.state.doc, anchor).length,
    focus: rawBefore(editor.state.doc, head).length,
  };
}

function tokenRanges(doc: ProseMirrorNode): Array<{ start: number; end: number }> {
  const paragraph = doc.firstChild;
  if (!paragraph) return [];
  const ranges: Array<{ start: number; end: number }> = [];
  let rawOffset = 0;
  paragraph.forEach((node) => {
    const raw = nodeRaw(node);
    if (node.type.name === "queryToken") {
      ranges.push({ start: rawOffset, end: rawOffset + raw.length });
    }
    rawOffset += raw.length;
  });
  return ranges;
}

function sameActiveEdit(
  left: ActiveStructuredQueryEdit | null,
  right: ActiveStructuredQueryEdit | null,
): boolean {
  return (
    left?.kind === right?.kind &&
    left?.start === right?.start &&
    left?.end === right?.end &&
    left?.prefix === right?.prefix
  );
}

function announceActiveEdit(edit: ActiveStructuredQueryEdit | null) {
  if (sameActiveEdit(activeEdit, edit)) return;
  activeEdit = edit;
  emit("structured-edit-change", edit);
}

function updateActiveEdit() {
  if (applyingExternalValue) return;
  if (!editor.state.selection.empty) {
    announceActiveEdit(null);
    return;
  }
  const selection = currentSelection();
  const edit = structuredQueryEditAtOffset(localRaw, selection.focus);
  if (
    edit &&
    tokenRanges(editor.state.doc).some((range) => edit.start < range.end && edit.end > range.start)
  ) {
    announceActiveEdit(null);
    return;
  }
  announceActiveEdit(edit);
}

function updateEditorData() {
  const dom = editor.view.dom;
  dom.dataset.queryValue = localRaw;
  dom.dataset.empty = localRaw.length === 0 ? "true" : "false";
}

function setRaw(raw: string, selection?: ConversationQuerySelection | null) {
  applyingExternalValue = true;
  try {
    localRaw = raw;
    editor.commands.setContent(queryDocument(raw), { emitUpdate: false });
    if (selection) {
      const anchor = rawPositionToDocument(editor.state.doc, selection.anchor);
      const focus = rawPositionToDocument(editor.state.doc, selection.focus);
      editor.view.dispatch(
        editor.state.tr.setSelection(TextSelection.create(editor.state.doc, anchor, focus)),
      );
    }
    updateEditorData();
  } finally {
    applyingExternalValue = false;
  }
}

const editor = new Editor({
  extensions: [QueryDocument, Paragraph, Text, UndoRedo, QueryToken],
  content: queryDocument(props.modelValue),
  editorProps: {
    attributes: {
      class: "conversation-query-editor drawer-search-input",
      "data-testid": "conversation-query-editor",
      role: "textbox",
      "aria-multiline": "false",
      "aria-label": props.ariaLabelText,
      "aria-placeholder": props.placeholder,
      "data-placeholder": props.placeholder,
      autocomplete: "off",
      autocapitalize: "none",
      spellcheck: "false",
    },
    transformPastedText: (text) => text.replace(/\r\n?|\n/g, " "),
    handleDOMEvents: {
      mousedown(view, event) {
        const target = event.target as Element;
        if (!target.closest("[data-conversation-query-token]")) return false;
        event.preventDefault();
        if (target.closest(".conversation-query-token-delete")) return true;
        const position = view.posAtCoords({ left: event.clientX, top: event.clientY })?.inside;
        if (position !== undefined && view.state.doc.nodeAt(position)?.type.name === "queryToken") {
          focusAt(afterTokenSeparator(view.state.doc, position));
        }
        return true;
      },
      click(view, event) {
        if (!(event.target as Element).closest(".conversation-query-token-delete")) return false;
        event.preventDefault();
        const position = view.posAtCoords({ left: event.clientX, top: event.clientY })?.inside;
        if (position === undefined || view.state.doc.nodeAt(position)?.type.name !== "queryToken") {
          return true;
        }
        view.dispatch(
          view.state.tr.delete(position, afterTokenSeparator(view.state.doc, position)),
        );
        focusAt(position);
        return true;
      },
    },
    handleKeyDown(_view, event) {
      emit("keydown", event);
      if (event.key === "Backspace" && editor.state.selection.empty) {
        const caret = rawBefore(editor.state.doc, editor.state.selection.from).length;
        const token = tokenRanges(editor.state.doc)
          .filter((candidate) => candidate.end <= caret)
          .reverse()
          .find((candidate) => {
            const separator = localRaw.slice(candidate.end, caret);
            return separator.length <= 1 && /^\s*$/.test(separator);
          });
        if (token) {
          event.preventDefault();
          const hasFollowingContent = /^\s+\S/.test(localRaw.slice(token.end));
          const removed = hasFollowingContent
            ? {
                query: localRaw.slice(0, token.start) + localRaw.slice(token.end),
                caret: token.start,
              }
            : removeConversationQueryTerm(localRaw, token.start, token.end);
          setRaw(removed.query, { anchor: removed.caret, focus: removed.caret });
          emit("update:modelValue", removed.query);
          // A trailing partial term shifted left with the removal; re-derive
          // the active edit now so the drawer never parses stale offsets.
          updateActiveEdit();
          void nextTick(() => focusAt(rawPositionToDocument(editor.state.doc, removed.caret)));
          return true;
        }
      }
      if (event.key === "Enter" && !event.defaultPrevented && !isImeComposing(event)) {
        event.preventDefault();
      }
      return event.defaultPrevented;
    },
  },
  onCreate() {
    updateEditorData();
  },
  onTransaction({ editor: updatedEditor, transaction }) {
    if (applyingExternalValue || !transaction.docChanged) return;
    localRaw = rawBefore(updatedEditor.state.doc, updatedEditor.state.doc.content.size);
    updateEditorData();
    emit("update:modelValue", localRaw);
    updateActiveEdit();
  },
  onSelectionUpdate() {
    updateActiveEdit();
  },
  onFocus() {
    updateActiveEdit();
  },
  onBlur() {
    announceActiveEdit(null);
  },
});

// focusAt puts the caret at position and focuses the editor now. TipTap's
// focus command focuses it only on the next animation frame; until then, keys
// go to whatever had focus (the button just clicked), and the DOM caret is
// not yet where the editor's is. Its frame still comes, and focuses a view
// that was not yet in the page.
function focusAt(position: FocusPosition) {
  editor.commands.focus(position);
  editor.view.focus();
}

function completeStructuredTerm(term: string): boolean {
  updateActiveEdit();
  if (!activeEdit) return false;
  const completed = completeStructuredQueryEdit(localRaw, activeEdit, term);
  announceActiveEdit(null);
  setRaw(completed.query, {
    anchor: completed.caret,
    focus: completed.caret,
  });
  emit("update:modelValue", completed.query);
  void nextTick(() => focusAt(rawPositionToDocument(editor.state.doc, completed.caret)));
  return true;
}

function finishStructuredEdit() {
  announceActiveEdit(null);
}

function focusEnd() {
  focusAt("end");
  updateActiveEdit();
}

watch(
  () => props.modelValue,
  (value) => {
    if (value === localRaw) return;
    const selection = currentSelection();
    const rebased = rebaseConversationQuerySelection(localRaw, value, selection);
    announceActiveEdit(null);
    setRaw(value, rebased);
  },
);

onBeforeUnmount(() => editor.destroy());

defineExpose({ completeStructuredTerm, finishStructuredEdit, focusEnd });
</script>
