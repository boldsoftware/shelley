// Opening an arbitrary file in the standalone editor modal, from anywhere in
// the tree.
//
// App owns the EditableFileModal (one instance, so two cards can't fight over
// the same Monaco model) and provides this opener; components deep in the
// message list — e.g. a patch tool card's "open in editor" button — inject it
// instead of emitting through every intermediate component.
import { inject, provide, type InjectionKey } from "vue";

/** Opens `path` (absolute, or relative to the conversation's cwd) in the
 *  editor modal. A path that can't be resolved is ignored. */
export type OpenFileEditor = (path: string) => void;

export const OpenFileEditorKey: InjectionKey<OpenFileEditor> = Symbol("open-file-editor");

export function provideOpenFileEditor(open: OpenFileEditor): void {
  provide(OpenFileEditorKey, open);
}

/** The editor opener. App is the only host that mounts tool cards, and it
 *  always provides one; a missing provider is a bug (Vue logs the failed
 *  injection and calling this throws) rather than something to fall back for. */
export function useOpenFileEditor(): OpenFileEditor {
  return inject(OpenFileEditorKey) as OpenFileEditor;
}
