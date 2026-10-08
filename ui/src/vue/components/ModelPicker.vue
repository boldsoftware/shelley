<!-- Unified model + reasoning-effort picker, built on PrimeVue <Select>.
     One quiet control replaces the previous pair of labeled dropdowns: the
     trigger reads "Claude Opus 4.8 · medium" and opens a single popover of
     sections with like headings: Model (with Manage… and refresh), search,
     recent combinations, the model list, and Reasoning effort; as the
     settings knob, Tools and Profile too.

     Every choice in the panel (a model, a pill, a profile action) keeps it
     open, so one visit can adjust several settings; it closes on a click
     outside, Escape, Enter on an option, or an action that opens a dialog.

     Design notes for the default (non-customized) install:
     - Model ids are humanized via prettyModelLabels (claude-opus-4.8 ->
       "Claude Opus 4.8"); unknown ids pass through verbatim and label
       collisions fall back to raw ids.
     - Source sub-labels only render when the catalog actually spans more
       than one source — a single-gateway install never repeats its hostname
       under every option.
     - Tier-2 models stay behind a "All models (N)" toggle; an active search
       spans all models so nothing is unfindable.
     PrimeVue owns open/close, outside-click, Escape and viewport-aware flip
     placement; styling uses size="small" + the shared statusPickerDt token
     map.

     The overlay is appended to the trigger for the boxed variant and to <body>
     for the inline one. append-to="self" positions with relativePosition(),
     which aligns left edges and never clamps to the viewport — fine for the
     composer's full-width trigger, but the inline trigger sits at the right edge
     of the status bar, from where the panel hung off the left of the screen on a
     narrow viewport. The body portal takes absolutePosition(), which does clamp
     and flip. -->
<template>
  <Select
    ref="selectRef"
    :model-value="selectedPickerValue"
    :options="optionGroups"
    option-label="label"
    option-value="pickerValue"
    option-group-label="label"
    option-group-children="items"
    :option-disabled="(m: PickerOption) => !m.ready || m.id === disabledModel"
    :disabled="disabled"
    fluid
    size="small"
    :dt="statusPickerDt"
    :scroll-height="scrollHeight"
    :class="['model-picker', { 'model-picker-inline': inline }]"
    :aria-label="ariaLabel"
    :append-to="inline || appendToBody ? 'body' : 'self'"
    :pt="{
      overlay: { class: ['model-picker-panel', panelClass] },
      label: disabledReason ? { 'aria-description': disabledReason } : {},
    }"
    filter
    :filter-fields="['label', 'id', 'source']"
    :filter-placeholder="t('searchModels')"
    :empty-filter-message="t('noModelsFound')"
    reset-filter-on-hide
    :auto-filter-focus="autoFilterFocus"
    :focus-on-hover="false"
    @update:model-value="handleSelect"
    @filter="onFilter"
    @before-show="heldRecent = liveRecent"
    @hide="onHide"
  >
    <template #value>
      <span class="model-picker-value">
        <span :class="['model-picker-value-name', { 'status-readout-affordance': inline }]">{{
          triggerLabel || selectedLabel
        }}</span>
        <span v-if="effortText && !inline && !triggerLabel" class="model-picker-value-effort"
          >· {{ effortText }}</span
        >
      </span>
    </template>
    <template v-if="catalogActions || keepsOpen" #header>
      <div class="model-picker-section-head model-picker-header" @click.stop>
        <span>{{ t("model") }}</span>
        <span class="model-picker-head-actions">
          <button
            v-if="catalogActions"
            class="model-picker-link"
            type="button"
            :aria-label="t('manageModels')"
            @click="handleKnob(() => emit('manageModels'))"
          >
            {{ t("manageModelsAction") }}
          </button>
          <button
            v-if="catalogActions"
            class="model-picker-icon"
            type="button"
            :disabled="refreshing"
            v-tooltip.top="refreshing ? t('refreshingModels') : t('refreshModels')"
            :aria-label="refreshing ? t('refreshingModels') : t('refreshModels')"
            @click="emit('refreshModels')"
          >
            <svg
              :class="`model-picker-refresh-icon ${refreshing ? 'spinning' : ''}`"
              width="12"
              height="12"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
            >
              <path d="M21 12a9 9 0 1 1-2.64-6.36M21 3v6h-6" />
            </svg>
          </button>
          <button
            v-if="keepsOpen"
            class="model-picker-icon"
            type="button"
            :aria-label="t('closeAction')"
            @click="close"
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M6 18L18 6M6 6l12 12" />
            </svg>
          </button>
        </span>
      </div>
    </template>
    <template #optiongroup="{ option }">
      <span
        v-if="option.kind === 'recent'"
        class="model-picker-section-head model-picker-group-label"
        >{{ t("recentModels") }}</span
      >
      <span v-else class="model-picker-group-divider" aria-hidden="true" />
    </template>
    <template #option="{ option, index }">
      <!-- Picking with the pointer keeps the panel open, like every other
           choice in it; PrimeVue's own mousedown would close it. -->
      <div class="model-picker-option" @mousedown.stop.prevent="choose(option, index)">
        <div class="model-picker-option-content">
          <span class="model-picker-option-name">{{ option.label }}</span>
          <span
            v-if="option.source && option.source !== dominantSource"
            class="model-picker-option-source"
            >{{ option.source }}</span
          >
        </div>
        <span
          v-if="option.kind === 'recent' && option.recentEffortLabel"
          class="model-picker-option-effort"
        >
          {{ option.recentEffortLabel }}
        </span>
        <span v-if="!option.ready" class="model-picker-option-badge">{{ t("notReadyBadge") }}</span>
        <svg
          v-else-if="option.kind === 'model' && option.id === selectedModel"
          class="model-picker-option-check"
          width="14"
          height="14"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="2.5"
          stroke-linecap="round"
          stroke-linejoin="round"
          aria-hidden="true"
        >
          <path d="M20 6L9 17l-5-5" />
        </svg>
      </div>
    </template>
    <template #footer>
      <!-- Clicks stay in here: a choice re-renders the panel, and PrimeVue,
           finding the clicked element gone from it, would take the click for
           one outside and close the panel. -->
      <div class="model-picker-footer" @click.stop>
        <button
          v-if="tier2Models.length > 0 && !filterValue"
          class="model-picker-more"
          type="button"
          @click="toggleMore"
        >
          <svg
            :class="`model-picker-more-icon ${showMore ? 'expanded' : ''}`"
            width="12"
            height="12"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
          >
            <path d="M6 9l6 6 6-6" />
          </svg>
          <span>{{
            showMore ? t("showFewerModels") : `${t("showAllModels")} (${models.length})`
          }}</span>
        </button>
        <section v-if="showReasoning && reasoningSupported" class="model-picker-section">
          <div class="model-picker-section-head">
            <span :id="effortLabelId">{{ t("effortLabel") }}</span>
          </div>
          <div class="model-picker-effort-pills" role="radiogroup" :aria-labelledby="effortLabelId">
            <button
              v-for="level in effortLevels"
              :key="level.value"
              type="button"
              role="radio"
              :aria-checked="level.value === effectiveEffort"
              :class="`model-picker-effort-pill${level.value === effectiveEffort ? ' active' : ''}`"
              @click="selectEffort(level.value)"
            >
              {{ level.label }}
            </button>
          </div>
        </section>
        <template v-if="knob">
          <section class="model-picker-section" data-testid="model-picker-tools">
            <div class="model-picker-section-head">
              <span>{{ t("toolsLabel") }}</span>
              <button
                class="model-picker-link"
                type="button"
                :aria-label="t('editToolsAction')"
                @click="handleKnob(knob.configureTools)"
              >
                {{ t("editAction") }}
              </button>
            </div>
            <div class="model-picker-section-text">
              {{ knob.toolChanges.length ? describe(knob.toolChanges) : t("toolsAtDefaults") }}
            </div>
          </section>
          <section
            v-if="knob.profiles.length > 0"
            class="model-picker-section"
            data-testid="model-picker-profiles"
          >
            <div class="model-picker-section-head">
              <span :id="profileLabelId">{{ t("profileLabel") }}</span>
              <button
                class="model-picker-link"
                type="button"
                :aria-label="t('editProfilesAction')"
                @click="handleKnob(knob.editProfiles)"
              >
                {{ t("editAction") }}
              </button>
            </div>
            <div
              ref="profilePills"
              class="model-picker-profile-pills"
              role="radiogroup"
              :aria-labelledby="profileLabelId"
            >
              <button
                v-for="p in knob.profiles"
                :key="p.name"
                type="button"
                role="radio"
                :aria-checked="p.name === knob.profile"
                :class="[
                  'model-picker-profile-pill',
                  {
                    active: p.name === knob.profile,
                    modified: p.name === knob.profile && knob.changes.length > 0,
                  },
                ]"
                @click="profileAction(() => knob!.selectProfile(p.name))"
              >
                {{ p.name }}
              </button>
            </div>
            <div
              v-if="knob.changes.length > 0 || !profileSaved"
              class="model-picker-profile-changed"
              data-testid="model-picker-profile-changed"
            >
              <div class="model-picker-section-text">{{ changedText }}</div>
              <form
                v-if="newProfileName !== null"
                class="model-picker-profile-new"
                @submit.prevent="profileAction(() => knob!.saveAsProfile(newProfileName!.trim()))"
              >
                <input
                  ref="newProfileInput"
                  v-model="newProfileName"
                  class="model-picker-profile-name"
                  :placeholder="t('profileNewName')"
                  :aria-label="t('profileNewName')"
                  autocomplete="off"
                  @keydown.escape.stop="cancelSaveAsNew"
                />
                <button class="model-picker-link" type="submit" :disabled="!newProfileName.trim()">
                  {{ t("save") }}
                </button>
                <button class="model-picker-link" type="button" @click="cancelSaveAsNew">
                  {{ t("cancel") }}
                </button>
              </form>
              <div v-else class="model-picker-profile-actions">
                <button
                  v-if="profileSaved"
                  class="model-picker-link"
                  type="button"
                  @click="profileAction(() => knob!.updateProfile())"
                >
                  {{ t("profileUpdate").replace("{name}", knob.profile) }}
                </button>
                <button
                  ref="saveAsNewButton"
                  class="model-picker-link"
                  type="button"
                  @click="startSaveAsNew"
                >
                  {{ t("profileSaveAsNew") }}
                </button>
                <button
                  v-if="profileSaved"
                  class="model-picker-link"
                  type="button"
                  @click="profileAction(() => knob!.selectProfile(knob!.profile))"
                >
                  {{ t("profileRevert") }}
                </button>
              </div>
            </div>
            <div v-if="profileError" class="model-picker-profile-error" role="alert">
              {{ profileError }}
            </div>
          </section>
        </template>
      </div>
    </template>
  </Select>
</template>

<script setup lang="ts">
import { computed, inject, nextTick, onBeforeUnmount, ref, watch } from "vue";
import Select from "primevue/select";
import { statusPickerDt } from "./statusPickerDt";
import { prettyModelLabels } from "../../utils/modelNames";
import { THINKING_LEVELS, type ThinkingLevel } from "./thinkingLevel";
import { useI18n } from "../composables/i18n";
import type { Model } from "../../types";
import { ConversationsListKey } from "../composables/subagentLive";
import { recentModelCombinations, type RecentThinkingLevel } from "./recentModelCombinations";
import type { SettingsDifference, SettingsKnob } from "../composables/profiles";
import { COMPACT_IN_PLACE_TOOL } from "./autoCompaction";

type PickerModel = Model & { label: string };
type PickerOption = PickerModel & {
  kind: "model" | "recent";
  pickerValue: string;
  recentEffortLabel?: string;
  recentThinkingLevel?: RecentThinkingLevel;
};
type PickerGroup = {
  kind: "models" | "recent";
  label: string;
  items: PickerOption[];
};

const props = withDefaults(
  defineProps<{
    models: Model[];
    selectedModel: string;
    thinkingLevel: ThinkingLevel;
    disabled?: boolean;
    disabledModel?: string;
    showRecent?: boolean;
    showReasoning?: boolean;
    panelClass?: string;
    refreshing?: boolean;
    /** Show the "Manage models…" / refresh footer. Off where the picker
     *  configures a one-shot action rather than the catalog. */
    catalogActions?: boolean;
    /** Portal the overlay to <body> so it isn't clipped by a scrollable
     *  ancestor (e.g. the version modal). */
    appendToBody?: boolean;
    /** Render as bare text (no box, no chevron) for the status-bar readout,
     *  where the picker sits among plain dot-separated segments. The overlay
     *  and behavior are unchanged; only the trigger's chrome differs. */
    inline?: boolean;
    /** Prefix for the combobox's accessible name when the picker configures a
     *  specific action, such as "Rebase model". */
    ariaLabelPrefix?: string;
    /** Fixed text for the trigger instead of the selected model's name, for
     *  callers that name the selection elsewhere and want the trigger to read
     *  as a plain control (e.g. "Model" beside a button naming the model). */
    triggerLabel?: string;
    /** Max height of the option list. The default suits a trigger with the
     *  page below it; a trigger near the bottom of a dialog wants less, so the
     *  panel opens downward instead of flipping over the trigger. */
    scrollHeight?: string;
    /** Why the picker is disabled, put on the combobox itself so a screen
     *  reader that lands on it is told. Pointer users get the caller's tooltip;
     *  ARIA has to come from here because the disabled element is the thing
     *  assistive tech actually focuses. */
    disabledReason?: string;
    /** Makes the picker the settings knob: profiles, tools, and the profiles
     *  manager join models and reasoning. */
    knob?: SettingsKnob;
  }>(),
  {
    disabled: false,
    disabledModel: "",
    showRecent: true,
    showReasoning: true,
    refreshing: false,
    catalogActions: true,
    inline: false,
    appendToBody: false,
    // On a short screen the list gives way further, to fit the sections
    // around it (see .model-picker-panel in styles.css).
    scrollHeight: "22rem",
  },
);
const emit = defineEmits<{
  (e: "selectModel", modelId: string): void;
  (e: "selectCombination", modelId: string, level: RecentThinkingLevel): void;
  (e: "thinkingChange", level: ThinkingLevel): void;
  (e: "manageModels"): void;
  (e: "refreshModels"): void;
}>();

const { t } = useI18n();
const selectRef = ref<InstanceType<typeof Select> | null>(null);
// Where the panel is a sheet (see styles.css), focusing the search would bring
// up the keyboard over it.
const mobileMq = window.matchMedia("(max-width: 767px), (max-height: 500px)");
const autoFilterFocus = ref(!mobileMq.matches);
const onMobileChange = (event: MediaQueryListEvent) => {
  autoFilterFocus.value = !event.matches;
};
mobileMq.addEventListener("change", onMobileChange);
onBeforeUnmount(() => mobileMq.removeEventListener("change", onMobileChange));
const effortLabelId = `model-picker-effort-label-${Math.random().toString(36).slice(2, 8)}`;
const profileLabelId = `${effortLabelId}-profile`;
const conversations = inject(ConversationsListKey);
if (!conversations) throw new Error("ModelPicker requires ConversationsListKey");

// ---- model list -------------------------------------------------------

const labels = computed(() => prettyModelLabels(props.models));
const decorated = computed<PickerModel[]>(() =>
  props.models.map((m) => ({ ...m, label: labels.value.get(m.id) || m.id })),
);

// Source sub-labels are pure noise when every model comes from the same
// place — and even in a customized install, the main gateway's name repeated
// under dozens of options drowns out the few models it actually matters for.
// So label only the models whose source differs from the dominant (most
// common) source; in a single-source install that's nothing.
const dominantSource = computed(() => {
  const counts = new Map<string, number>();
  for (const m of props.models) {
    if (!m.source) continue;
    counts.set(m.source, (counts.get(m.source) || 0) + 1);
  }
  let best = "";
  let bestCount = 0;
  for (const [src, n] of counts) {
    if (n > bestCount) {
      best = src;
      bestCount = n;
    }
  }
  return best;
});

// Tier 2 models are overshadowed by a better available sibling; they're kept
// behind an "All models" toggle so the common case stays uncluttered. Absent
// tier is treated as tier 1 (backward compatible with older servers).
const isTier2 = (m: Model) => m.tier === 2;
const tier1Models = computed(() => decorated.value.filter((m) => !isTier2(m)));
const tier2Models = computed(() => decorated.value.filter(isTier2));

const showMore = ref(false);
const filterValue = ref("");

function onFilter(event: { value: string }) {
  filterValue.value = event.value ?? "";
}

// When collapsed, only show tier-1 models — plus the currently selected model
// if it happens to be a tier-2 one, so the selection always renders. While a
// filter is active, search across all models (including tier-2) so hidden
// models remain findable without expanding "All models".
const visibleModels = computed(() => {
  if (showMore.value || filterValue.value) return decorated.value;
  const base = tier1Models.value;
  const selected = decorated.value.find((m) => m.id === props.selectedModel);
  if (selected && isTier2(selected) && !base.includes(selected)) {
    return [...base, selected];
  }
  return base;
});

// Held while the panel is open, so picking one doesn't pull it out from under
// the pointer. Live while it's closed: PrimeVue finds the option to highlight
// as it opens, before a list changed then would render.
const heldRecent = ref<PickerOption[] | null>(null);
const recentOptions = computed(() => heldRecent.value ?? liveRecent.value);
const liveRecent = computed((): PickerOption[] => {
  const byId = new Map(decorated.value.map((model) => [model.id, model]));
  return recentModelCombinations(conversations.value, props.models, Date.now(), 4)
    .filter((combination) => !isActiveRecentCombination(combination))
    .slice(0, 3)
    .flatMap((combination) => {
      const model = byId.get(combination.modelId);
      if (!model) return [];
      return [
        {
          ...model,
          kind: "recent",
          pickerValue: `recent:${model.id}:${combination.thinkingLevel || ""}`,
          recentThinkingLevel: combination.thinkingLevel,
          recentEffortLabel: combination.thinkingLevel || "",
        },
      ];
    });
});

const modelOptions = computed<PickerOption[]>(() =>
  visibleModels.value.map((model) => ({
    ...model,
    kind: "model",
    pickerValue: `model:${model.id}`,
  })),
);

const optionGroups = computed<PickerGroup[]>(() => {
  const groups: PickerGroup[] = [];
  if (props.showRecent && recentOptions.value.length && !filterValue.value) {
    groups.push({ kind: "recent", label: "Recent", items: recentOptions.value });
  }
  groups.push({ kind: "models", label: "", items: modelOptions.value });
  return groups;
});

const pickerOptionsByValue = computed(
  () =>
    new Map(
      [...recentOptions.value, ...modelOptions.value].map((option) => [option.pickerValue, option]),
    ),
);

const selectedPickerValue = computed(() => `model:${props.selectedModel}`);

const selectedModelObj = computed(() => props.models.find((m) => m.id === props.selectedModel));
// selectedModel is "" when the server serves no models (see
// utils/modelSetupHint). Show an explicit placeholder rather than an empty
// control, so the trigger never looks like a model is selected.
const selectedLabel = computed(
  () =>
    labels.value.get(props.selectedModel) || props.selectedModel || t("noModelSelectedPlaceholder"),
);

// ---- reasoning effort --------------------------------------------------

const reasoningSupported = computed(() => selectedModelObj.value?.supports_reasoning !== false);

// The model's default as a real, selectable level (null when the provider's
// default can't be named, e.g. a dynamic default Shelley doesn't know).
const modelDefault = computed<ThinkingLevel | null>(() => {
  const d = selectedModelObj.value?.default_reasoning_level;
  if (!d || d === "default") return null;
  return THINKING_LEVELS.some((l) => l.value === d) ? (d as ThinkingLevel) : null;
});

// What the pill row highlights. A stored "default" sentinel resolves to the
// model's concrete default level when we know it, so the UI shows the real
// level (e.g. medium) rather than a made-up "default" entry.
const effectiveEffort = computed<ThinkingLevel>(() =>
  props.thinkingLevel === "default" && modelDefault.value
    ? modelDefault.value
    : props.thinkingLevel,
);

const effortLevels = computed(() => {
  const real = THINKING_LEVELS.filter((l) => l.value !== "default");
  const advertised = selectedModelObj.value?.reasoning_levels as ThinkingLevel[] | undefined;
  const list = advertised?.length
    ? real.filter((l) => advertised.includes(l.value))
    : real.filter((l) => l.value !== "max");
  // Make sure the model's default is always selectable, even if it falls
  // outside the advertised subset (defensive; normally it's included).
  if (modelDefault.value && !list.some((l) => l.value === modelDefault.value)) {
    const def = real.find((l) => l.value === modelDefault.value);
    if (def) list.push(def);
  }
  // Only keep an "auto" sentinel when the concrete default is unknown, so
  // users can still defer to the model; otherwise the default is just one of
  // the real levels (pre-selected).
  return modelDefault.value === null
    ? [{ value: "default" as ThinkingLevel, label: t("effortAuto") }, ...list]
    : list;
});

// Trigger suffix: the concrete effort in play. Blank when the model doesn't
// reason or when the effective level is an unknowable provider default —
// showing nothing beats showing the word "default".
const effortText = computed(() => {
  if (!reasoningSupported.value) return "";
  if (effectiveEffort.value === "default") return "";
  return effectiveEffort.value;
});

// Names the effort even in the inline variant, whose trigger doesn't show it.
const ariaLabel = computed(() => {
  const prefix = props.ariaLabelPrefix || "Model";
  return effortText.value
    ? `${prefix}: ${selectedLabel.value}, reasoning effort: ${effortText.value}`
    : `${prefix}: ${selectedLabel.value}`;
});

// ---- actions -----------------------------------------------------------

function isActiveRecentCombination(combination: {
  modelId: string;
  thinkingLevel: RecentThinkingLevel;
}): boolean {
  if (combination.modelId !== props.selectedModel) return false;
  if (selectedModelObj.value?.supports_reasoning === false) {
    return combination.thinkingLevel === null;
  }
  return (combination.thinkingLevel ?? "default") === effectiveEffort.value;
}

// Disabling only locks the trigger. A picker already open when it happens
// (the status bar's, as a turn starts) closes, and anything still in flight
// from its panel is dropped: switching model mid-turn cancels the turn.
watch(
  () => props.disabled,
  (disabled) => {
    if (disabled) selectRef.value?.hide();
  },
);

// Enter on an option, which PrimeVue follows by closing the panel.
function handleSelect(pickerValue: string) {
  const option = pickerOptionsByValue.value.get(pickerValue);
  if (!option) throw new Error(`unknown model picker option: ${pickerValue}`);
  choose(option);
}

// What this takes of PrimeVue's Select beyond its typings: hide(true) puts
// focus back on the trigger, and the option keyboard focus is on, which a
// pick with the pointer (taken here, not by PrimeVue) has to move as well, or
// Enter would pick the one focused before.
type SelectInternals = { hide(focusTrigger?: boolean): void; focusedOptionIndex: number };
const select = () => selectRef.value as unknown as SelectInternals | null;

// A panel of settings stays open through its choices, and says so with a
// close button; a bare list of models closes on a pick, as a select does.
const keepsOpen = computed(() => props.showReasoning || !!props.knob);
function close() {
  select()?.hide(true);
}
function choose(option: PickerOption, index?: number) {
  if (props.disabled || !option.ready || option.id === props.disabledModel) return;
  if (option.kind === "recent") {
    emit("selectCombination", option.id, option.recentThinkingLevel ?? null);
  } else {
    emit("selectModel", option.id);
  }
  if (!keepsOpen.value) close();
  else if (index !== undefined) {
    const s = select();
    if (s) s.focusedOptionIndex = index;
  }
}

function selectEffort(level: ThinkingLevel) {
  // The pills are radios: the current one, which may stand for the model's
  // default, changes nothing.
  if (props.disabled || level === effectiveEffort.value) return;
  emit("thinkingChange", level);
}

function toggleMore() {
  showMore.value = !showMore.value;
}

// An action that opens a dialog, which takes the focus.
function handleKnob(action: () => void) {
  select()?.hide();
  action();
}

// Whether the settings' profile is still saved; it may have been deleted, or
// a conversation from before profiles has none.
const profileSaved = computed(
  () => !!props.knob?.profiles.some((p) => p.name === props.knob?.profile),
);
const newProfileName = ref<string | null>(null);
const newProfileInput = ref<HTMLInputElement | null>(null);
const saveAsNewButton = ref<HTMLButtonElement | null>(null);
const profilePills = ref<HTMLElement | null>(null);
let profileBusy = false;
const profileError = ref("");
// Saving the settings into a profile, or switching profiles, from the panel.
// The panel stays open to show the result; a failure says why there.
async function profileAction(action: () => Promise<void>) {
  if (profileBusy) return;
  profileBusy = true;
  profileError.value = "";
  try {
    await action();
    newProfileName.value = null;
  } catch (err) {
    profileError.value = err instanceof Error ? err.message : String(err);
  } finally {
    profileBusy = false;
  }
  // The action may have taken its button away (Revert and Update take the
  // whole row), which leaves focus on <body>, out of reach of the panel's
  // Escape; it goes to the profile in use.
  await nextTick();
  if (document.activeElement && document.activeElement !== document.body) return;
  const pills = profilePills.value;
  (
    pills?.querySelector<HTMLElement>('[aria-checked="true"]') ?? pills?.querySelector("button")
  )?.focus();
}
function onHide() {
  heldRecent.value = null;
  filterValue.value = "";
  newProfileName.value = null;
  profileError.value = "";
}
async function startSaveAsNew() {
  newProfileName.value = "";
  await nextTick();
  newProfileInput.value?.focus();
}
async function cancelSaveAsNew() {
  newProfileName.value = null;
  await nextTick();
  saveAsNewButton.value?.focus();
}

// What differs, in words: "Claude Opus 5.5 → GLM 5.3, browser off".
function describe(changes: SettingsDifference[]): string {
  const level = (l: string) => l || t("effortAuto");
  const swap = (key: Parameters<typeof t>[0], from: string, to: string) =>
    t(key).replace("{from}", from).replace("{to}", to);
  return changes
    .map((c) => {
      switch (c.kind) {
        case "model":
          return `${labels.value.get(c.from) || c.from} → ${labels.value.get(c.to) || c.to}`;
        case "reasoning":
          return swap("changeReasoning", level(c.from), level(c.to));
        case "tool":
          return t(c.on ? "changeToolOn" : "changeToolOff").replace(
            "{name}",
            c.name === COMPACT_IN_PLACE_TOOL ? t("autoCompactionName") : c.name,
          );
        case "nudge":
          return swap("changeNudge", `${c.from / 1000}k`, `${c.to / 1000}k`);
        case "systemPrompt":
          return t("changeSystemPrompt");
      }
    })
    .join(", ");
}

const changedText = computed(() => {
  const knob = props.knob;
  if (!knob) return "";
  if (!profileSaved.value) {
    return t(knob.profile ? "profileDeleted" : "profileUnsaved").replace("{name}", knob.profile);
  }
  return `${t("profileDiffersFrom").replace("{name}", knob.profile)}: ${describe(knob.changes)}`;
});
</script>
