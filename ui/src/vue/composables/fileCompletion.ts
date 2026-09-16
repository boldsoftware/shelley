import { computed, onScopeDispose, ref, watch, type Ref } from "vue";
import { api } from "../../services/api";
import { mergeContentMatches } from "../../utils/fileSearch";
import {
  fileTokenAt,
  insertFilePath,
  relativeFileMatch,
  type FileToken,
} from "../../utils/fileCompletion";

const MAX_ROWS = 20;

function tokenKeyFor(text: string, token: FileToken, session: string | null, cwd: string) {
  return JSON.stringify([session, cwd, token.start, text.slice(token.start, token.end)]);
}

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
  const dismissed = ref<string[]>([]);
  const selected = ref(0);
  const loading = ref(false);
  const grepPending = ref(false);
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
      ? tokenKeyFor(options.message.value, token.value, options.session(), options.cwd())
      : null,
  );
  watch(
    tokenKey,
    (value) => {
      dismissed.value = value && dismissed.value.includes(value) ? [value] : [];
    },
    { flush: "sync" },
  );
  const visible = computed(
    () => key.value !== null && !dismissed.value.includes(tokenKey.value ?? ""),
  );
  const matches = computed(() => {
    const response = result.value;
    if (!response) return [];
    return response.matches
      .slice(0, MAX_ROWS)
      .map((match) => relativeFileMatch(options.cwd(), response.search_dir, match));
  });

  watch(
    () => (visible.value ? key.value : null),
    (value, _old, onCleanup) => {
      result.value = null;
      selected.value = 0;
      error.value = "";
      loading.value = value !== null;
      grepPending.value = false;
      if (value === null) return;
      const controller = new AbortController();
      let stale = false;
      const cwd = options.cwd();
      const query = token.value!.query;
      const timer = setTimeout(async () => {
        const findFiles = options.findFiles ?? api.findFiles.bind(api);
        const namePromise = findFiles(cwd, query, controller.signal, {
          content: "skip",
          includeDirs: true,
        });
        const contentPromise = query
          ? findFiles(cwd, query, controller.signal, { content: "only" })
          : null;
        grepPending.value = contentPromise !== null;
        contentPromise?.catch(() => {});
        let nameApplied = false;
        try {
          const response = await namePromise;
          if (!stale) {
            result.value = response;
            nameApplied = true;
          }
        } catch (err) {
          if (!stale) {
            error.value = err instanceof Error ? err.message : String(err);
            controller.abort();
          }
        } finally {
          if (!stale) loading.value = false;
        }
        if (contentPromise) {
          try {
            const content = await contentPromise;
            if (!stale && nameApplied && result.value?.search_dir === content.search_dir) {
              result.value = {
                ...result.value,
                matches: mergeContentMatches(result.value.matches, content.matches, MAX_ROWS)
                  .matches,
              };
            }
          } catch {
            // Content search is best-effort; keep the faster name results.
          } finally {
            if (!stale) grepPending.value = false;
          }
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
    if (tokenKey.value) dismissed.value = [tokenKey.value];
  }

  function choose(index: number) {
    const match = matches.value[index];
    if (!visible.value || !match || !result.value || !token.value) return null;
    const replacement = insertFilePath(options.message.value, token.value, match.path);
    const nextToken = fileTokenAt(replacement.text, replacement.cursor);
    dismissed.value = [
      tokenKey.value,
      nextToken ? tokenKeyFor(replacement.text, nextToken, options.session(), options.cwd()) : null,
    ].filter((value): value is string => value !== null);
    return replacement;
  }

  onScopeDispose(() => {
    focused.value = false;
  });
  return {
    visible,
    matches,
    selected,
    loading,
    grepPending,
    error,
    focused,
    updateSelection,
    dismiss,
    choose,
  };
}
