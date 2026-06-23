<!-- Vue port of components/FeatureFlagsModal.tsx. Toggle/override feature flags.
     Preserves the feature-flag-* class contract (feature-flag-row / -head /
     -name / -badge / -desc / -bool / -json / -actions / -meta / -clear /
     -error / -list / -empty). Uses Modal.vue and the featureFlags composable
     (refreshFeatureFlags) so the module-level cache updates after edits.
     The React FlagRow's per-row draft/error/busy state is kept in maps keyed
     by flag name so this stays a single component file. -->
<template>
  <Modal :is-open="isOpen" title="Feature flags" @close="emit('close')">
    <div v-if="loading">Loading…</div>
    <div v-if="error" class="feature-flag-error">{{ error }}</div>
    <div v-if="!loading && !error && flags.length === 0" class="feature-flag-empty">
      No feature flags are defined. Add some by calling <code>featureflags.Register</code> in the Go
      code.
    </div>
    <div class="feature-flag-list">
      <div v-for="flag in flags" :key="flag.name" class="feature-flag-row">
        <div class="feature-flag-head">
          <code class="feature-flag-name">{{ flag.name }}</code>
          <span v-if="isOverridden(flag)" class="feature-flag-badge">overridden</span>
        </div>
        <div v-if="flag.description" class="feature-flag-desc">{{ flag.description }}</div>

        <label v-if="isBool(flag)" class="feature-flag-bool">
          <input
            type="checkbox"
            :checked="effective(flag) === true"
            :disabled="busy[flag.name]"
            @change="toggleBool(flag, ($event.target as HTMLInputElement).checked)"
          />
          <span>{{ effective(flag) === true ? "true" : "false" }}</span>
        </label>
        <template v-else>
          <textarea
            class="form-input feature-flag-json"
            :value="draft[flag.name]"
            spellcheck="false"
            :rows="Math.min(8, (draft[flag.name] ?? '').split('\n').length)"
            :disabled="busy[flag.name]"
            @input="draft[flag.name] = ($event.target as HTMLTextAreaElement).value"
          />
          <div class="feature-flag-actions">
            <button
              class="btn-primary"
              :disabled="busy[flag.name] || draft[flag.name] === effectiveJSON(flag)"
              @click="commitJSON(flag)"
            >
              Save
            </button>
          </div>
        </template>

        <div class="feature-flag-meta">
          <span>
            default: <code>{{ JSON.stringify(flag.default) }}</code>
          </span>
          <button
            v-if="isOverridden(flag)"
            class="btn-secondary feature-flag-clear"
            :disabled="busy[flag.name]"
            @click="clear(flag)"
          >
            Reset to default
          </button>
        </div>

        <div v-if="rowError[flag.name]" class="feature-flag-error">{{ rowError[flag.name] }}</div>
      </div>
    </div>
  </Modal>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from "vue";
import Modal from "./Modal.vue";
import { featureFlagsApi, type FeatureFlag } from "../../services/api";
import { refreshFeatureFlags } from "../composables/featureFlags";

const props = defineProps<{ isOpen: boolean }>();
const emit = defineEmits<{ (e: "close"): void }>();

const flags = ref<FeatureFlag[]>([]);
const loading = ref(false);
const error = ref<string | null>(null);

// Per-row draft / error / busy state, keyed by flag name.
const draft = reactive<Record<string, string>>({});
const rowError = reactive<Record<string, string | null>>({});
const busy = reactive<Record<string, boolean>>({});

function effective(flag: FeatureFlag): unknown {
  return flag.override !== undefined ? flag.override : flag.default;
}
function effectiveJSON(flag: FeatureFlag): string {
  return JSON.stringify(effective(flag), null, 2);
}
function isOverridden(flag: FeatureFlag): boolean {
  return flag.override !== undefined;
}
function isBool(flag: FeatureFlag): boolean {
  return typeof effective(flag) === "boolean" || typeof flag.default === "boolean";
}

function syncDrafts() {
  for (const f of flags.value) {
    draft[f.name] = effectiveJSON(f);
    rowError[f.name] = null;
  }
}

async function load() {
  loading.value = true;
  error.value = null;
  try {
    flags.value = await featureFlagsApi.list();
    syncDrafts();
  } catch (e) {
    error.value = e instanceof Error ? e.message : "Failed to load feature flags";
  } finally {
    loading.value = false;
  }
}

async function handleSave(name: string, value: unknown) {
  await featureFlagsApi.set(name, value);
  await load();
  await refreshFeatureFlags();
}

async function handleClear(name: string) {
  await featureFlagsApi.clear(name);
  await load();
  await refreshFeatureFlags();
}

async function commitJSON(flag: FeatureFlag) {
  rowError[flag.name] = null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(draft[flag.name]);
  } catch (e) {
    rowError[flag.name] = e instanceof Error ? e.message : "Invalid JSON";
    return;
  }
  busy[flag.name] = true;
  try {
    await handleSave(flag.name, parsed);
  } catch (e) {
    rowError[flag.name] = e instanceof Error ? e.message : "Save failed";
  } finally {
    busy[flag.name] = false;
  }
}

async function toggleBool(flag: FeatureFlag, next: boolean) {
  rowError[flag.name] = null;
  busy[flag.name] = true;
  try {
    await handleSave(flag.name, next);
  } catch (e) {
    rowError[flag.name] = e instanceof Error ? e.message : "Save failed";
  } finally {
    busy[flag.name] = false;
  }
}

async function clear(flag: FeatureFlag) {
  rowError[flag.name] = null;
  busy[flag.name] = true;
  try {
    await handleClear(flag.name);
  } catch (e) {
    rowError[flag.name] = e instanceof Error ? e.message : "Clear failed";
  } finally {
    busy[flag.name] = false;
  }
}

watch(
  () => props.isOpen,
  (open) => {
    if (open) load();
  },
  { immediate: true },
);
</script>
