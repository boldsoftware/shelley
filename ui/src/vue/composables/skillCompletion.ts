import { computed, nextTick, onScopeDispose, ref, watch, type Ref } from "vue";
import { api, type SkillDescriptor } from "../../services/api";
import { filterSkills, insertSkill, skillTokenAt } from "../../utils/skillCompletion";

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
  watch(
    () =>
      visible.value
        ? JSON.stringify([options.session(), options.cwd(), options.conversationId()])
        : null,
    async (context, _old, onCleanup) => {
      skills.value = [];
      selected.value = 0;
      error.value = "";
      loading.value = context !== null;
      if (context === null) return;
      const controller = new AbortController();
      let stale = false;
      onCleanup(() => {
        stale = true;
        controller.abort();
      });
      // Invalidate synchronously, but start only after Vue finishes patching
      // conversationId/lazyDraftId and the stable composer session. Intermediate
      // prop combinations must not issue duplicate requests.
      await nextTick();
      if (stale) return;
      try {
        const result = await (options.getSkills ?? api.getSkills.bind(api))(
          options.cwd(),
          options.conversationId(),
          controller.signal,
        );
        if (!stale) skills.value = result.skills;
      } catch (err) {
        if (!stale) error.value = err instanceof Error ? err.message : String(err);
      } finally {
        if (!stale) loading.value = false;
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
    const replacement = insertSkill(options.message.value, token.value, skill);
    dismiss();
    return replacement;
  }
  onScopeDispose(() => {
    focused.value = false;
  });
  return {
    active,
    visible,
    matches,
    selected,
    loading,
    error,
    focused,
    updateSelection,
    dismiss,
    reopen,
    choose,
  };
}
