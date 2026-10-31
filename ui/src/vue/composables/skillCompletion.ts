import { computed, nextTick, onScopeDispose, ref, watch, type Ref } from "vue";
import { api, type SkillDescriptor } from "../../services/api";
import { filterSkills, removeSkillToken, skillTokenAt } from "../../utils/skillCompletion";

/** Fetch once per open/context; query edits filter the in-memory catalog. */
export function useSkillCompletion(options: {
  message: Ref<string>;
  cwd: () => string;
  session: () => string | null;
  conversationId: () => string | null;
  enabled: () => boolean;
  getSkills?: typeof api.getSkills;
}) {
  const selection = ref({ start: 0, end: 0 });
  const focused = ref(false);
  const dismissed = ref<string | null>(null);
  const selected = ref(0);
  const loading = ref(false);
  const error = ref("");
  const warning = ref("");
  const skills = ref<SkillDescriptor[]>([]);
  const token = computed(() =>
    skillTokenAt(
      options.message.value,
      Math.min(selection.value.start, options.message.value.length),
      Math.min(selection.value.end, options.message.value.length),
    ),
  );
  const active = computed(() => options.enabled() && token.value !== null);
  const tokenKey = computed(() =>
    token.value
      ? JSON.stringify([
          options.session(),
          options.cwd(),
          token.value.start,
          // Escape survives refocusing and caret motion, but editing reopens.
          options.message.value,
        ])
      : null,
  );
  watch(
    tokenKey,
    (key) => {
      if (key !== dismissed.value) dismissed.value = null;
    },
    { flush: "sync" },
  );
  const visible = computed(
    () => active.value && focused.value && tokenKey.value !== dismissed.value,
  );
  const matches = computed(() => filterSkills(skills.value, token.value?.query ?? ""));

  watch(
    () => token.value?.query,
    () => {
      selected.value = 0;
    },
    { flush: "sync" },
  );
  // One catalog per composer context: query edits and close/reopen reuse it.
  // Changing session, cwd or conversation (or invalidate(), on send) drops it.
  const context = computed(() =>
    JSON.stringify([options.session(), options.cwd(), options.conversationId()]),
  );
  let loadedFor: string | null = null;
  let controller: AbortController | null = null;
  function reset() {
    controller?.abort();
    controller = null;
    loadedFor = null;
    skills.value = [];
    selected.value = 0;
    error.value = "";
    warning.value = "";
    loading.value = false;
  }
  watch(context, reset, { flush: "sync" });
  watch(
    () => (visible.value ? context.value : null),
    async (ctx) => {
      if (ctx === null) {
        if (loading.value) reset();
        return;
      }
      if (ctx === loadedFor) return;
      reset();
      loadedFor = ctx;
      loading.value = true;
      const mine = new AbortController();
      controller = mine;
      // Start after Vue settles the conversation id and composer session, so
      // intermediate combinations don't fetch.
      await nextTick();
      if (controller !== mine) return;
      try {
        const result = await (options.getSkills ?? api.getSkills.bind(api))(
          options.cwd(),
          options.conversationId(),
          mine.signal,
        );
        if (controller === mine) {
          skills.value = result.skills;
          warning.value = result.warnings?.join("\n") ?? "";
        }
      } catch (err) {
        if (controller !== mine) return;
        error.value = err instanceof Error ? err.message : String(err);
        loadedFor = null; // the next open retries
      } finally {
        if (controller === mine) loading.value = false;
      }
    },
    { flush: "sync" },
  );

  function updateSelection(start: number, end: number) {
    if (selection.value.start !== start || selection.value.end !== end)
      selection.value = { start, end };
  }
  function dismiss() {
    dismissed.value = tokenKey.value;
  }
  function reopen() {
    dismissed.value = null;
    focused.value = true;
  }
  function choose(index: number) {
    const skill = matches.value[index];
    if (!visible.value || !token.value || !skill || loading.value || error.value) return null;
    const replacement = removeSkillToken(options.message.value, token.value);
    dismiss();
    return { ...replacement, skill };
  }
  onScopeDispose(() => {
    focused.value = false;
    reset();
  });
  return {
    active,
    visible,
    matches,
    selected,
    loading,
    error,
    warning,
    focused,
    updateSelection,
    dismiss,
    reopen,
    choose,
    invalidate: reset,
  };
}
