import { computed, onScopeDispose, ref, watch, type Ref } from "vue";
import { api } from "../../services/api";
import { fileTokenAt, insertFilePath } from "../../utils/fileCompletion";

/** Debounce name-only searches and invalidate them on every token/session change.
 * Aborting saves work; the cleanup flag also rejects responses already in flight. */
export function useFileCompletion(options: {
  message: Ref<string>;
  cwd: () => string;
  session: () => string | null;
  enabled: () => boolean;
  findFiles?: typeof api.findFiles;
}) {
  const selection = ref({ start: 0, end: 0 });
  const focused = ref(false);
  const dismissed = ref<string | null>(null);
  const selected = ref(0);
  const loading = ref(false);
  const error = ref("");
  const result = ref<Awaited<ReturnType<typeof api.findFiles>> | null>(null);
  const token = computed(() =>
    fileTokenAt(options.message.value, selection.value.start, selection.value.end),
  );
  const key = computed(() =>
    focused.value && options.enabled() && options.cwd() && token.value
      ? JSON.stringify([options.session(), options.cwd(), token.value])
      : null,
  );
  // Dismiss the token, not a particular caret position. Refocusing or moving
  // within it must not undo Escape; editing it starts a fresh completion.
  const tokenKey = computed(() =>
    token.value
      ? JSON.stringify([
          options.session(),
          options.cwd(),
          token.value.start,
          options.message.value.slice(token.value.start, token.value.end),
        ])
      : null,
  );
  watch(
    tokenKey,
    () => {
      dismissed.value = null;
    },
    { flush: "sync" },
  );
  const visible = computed(() => key.value !== null && tokenKey.value !== dismissed.value);
  const matches = computed(() => result.value?.matches.slice(0, 20) ?? []);

  watch(
    () => (visible.value ? key.value : null),
    (value, _old, onCleanup) => {
      result.value = null;
      selected.value = 0;
      error.value = "";
      loading.value = value !== null;
      if (value === null) return;
      const controller = new AbortController();
      let stale = false;
      const cwd = options.cwd();
      const query = token.value!.query;
      const timer = setTimeout(async () => {
        try {
          const response = await (options.findFiles ?? api.findFiles.bind(api))(
            cwd,
            query,
            controller.signal,
            { content: "skip" },
          );
          if (!stale) result.value = response;
        } catch (err) {
          if (!stale) error.value = err instanceof Error ? err.message : String(err);
        } finally {
          if (!stale) loading.value = false;
        }
      }, 120);
      onCleanup(() => {
        stale = true;
        clearTimeout(timer);
        controller.abort();
      });
    },
    { flush: "sync" },
  );

  function updateSelection(start: number, end: number) {
    if (selection.value.start !== start || selection.value.end !== end) {
      selection.value = { start, end };
    }
  }

  function dismiss() {
    dismissed.value = tokenKey.value;
  }

  function choose(index: number) {
    const match = matches.value[index];
    if (!visible.value || !match || !result.value || !token.value) return null;
    const replacement = insertFilePath(
      options.message.value,
      token.value,
      result.value.search_dir,
      match.path,
    );
    dismiss();
    return replacement;
  }

  onScopeDispose(() => {
    focused.value = false;
  });
  return { visible, matches, selected, loading, error, focused, updateSelection, dismiss, choose };
}
