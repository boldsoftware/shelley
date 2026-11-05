<!-- The exe.dev account's other integrations (HTTP proxies, LLM, databases,
     …), which the user adds to the project's VMs: adding attaches one to
     the project's tag and gives the tag to the VMs its tasks run on;
     removing detaches it. Each is one click, applied at once (not on Save).
     GitHub integrations belong to the repository picker. Workspace mode's,
     shown on exe.dev only; an integration that may not list the account gets exe.dev's
     refusal and a link to manage integrations there. -->
<template>
  <template v-if="account.available.value">
    <div class="project-section-heading">
      <h3>{{ t("projectExeIntegrations") }}</h3>
      <a
        v-if="view?.manage_url"
        class="project-section-link"
        :href="view.manage_url"
        target="_blank"
        rel="noopener noreferrer"
        @click="awaitReturn"
      >
        {{ t("projectExeManage") }} <i class="pi pi-external-link" aria-hidden="true" />
      </a>
    </div>
    <div
      class="project-repos"
      :aria-busy="account.loading.value"
      data-testid="project-integrations"
    >
      <p v-if="view?.error" class="project-notice">
        {{
          t("projectExeRestricted")
            .replace("{integration}", view.integration)
            .replace("{error}", view.error)
        }}
      </p>
      <template v-else-if="view">
        <p v-if="view.tag" class="project-muted">
          {{ explain[0] }}<code>{{ view.tag }}</code
          >{{ explain[1] }}
        </p>
        <p v-else class="project-muted">
          {{ t("projectExeExplainNoTag").replace("{where}", where) }}
        </p>
        <p v-for="vm in view.vms.filter((v) => !v.found)" :key="vm.vm_name" class="error-line">
          {{ t("projectExeVMNotFound").replace("{vm}", vm.vm_name) }}
        </p>
        <p v-if="untagged.length && added.length" class="project-notice">
          {{
            t(untagged.length > 1 ? "projectExeUntaggedMany" : "projectExeUntaggedOne").replace(
              "{vms}",
              untagged.join(", "),
            )
          }}
          <Button
            :label="t('projectExeTagNow')"
            text
            size="small"
            :loading="busy === 'tag'"
            :disabled="disabled || !!busy"
            @click="run('tag', { action: 'tag' })"
          />
        </p>
        <input
          v-if="others.length > 8"
          v-model="query"
          type="search"
          :placeholder="t('projectExeSearchPlaceholder').replace('{count}', String(others.length))"
          :aria-label="t('projectExeSearch')"
          autocomplete="off"
        />
        <p v-if="!others.length" class="project-empty-repos">
          {{ t("projectExeNone") }}
        </p>
        <ul class="project-repo-list">
          <li
            v-for="ig in shownIntegrations"
            :key="`${ig.team}/${ig.name}`"
            class="project-repo-row"
            :class="{ selected: ig.added }"
          >
            <i :class="['pi', icon(ig.type)]" aria-hidden="true" />
            <span class="project-repo-name">
              <strong>{{ ig.name }}</strong>
              <span class="project-badge project-badge-plain">{{ ig.title || ig.type }}</span>
              <span v-if="ig.team" class="project-badge project-badge-plain">{{
                t("projectExeTeam")
              }}</span>
              <span class="project-muted">{{ status(ig) }}</span>
            </span>
            <Button
              v-if="ig.added"
              :label="t('projectExeRemove')"
              text
              severity="secondary"
              size="small"
              :loading="busy === ig.name"
              :disabled="disabled || !!busy"
              @click="run(ig.name, { action: 'remove', name: ig.name, team: ig.team })"
            />
            <Button
              v-else-if="!ig.reaches"
              :label="t('projectExeAdd')"
              outlined
              size="small"
              :loading="busy === ig.name"
              :disabled="disabled || !!busy"
              @click="run(ig.name, { action: 'add', name: ig.name, team: ig.team })"
            />
          </li>
        </ul>
      </template>
      <p v-if="error || account.error.value" class="error-line" role="alert">
        {{ error || account.error.value }}
      </p>
    </div>
  </template>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref } from "vue";
import Button from "primevue/button";
import type {
  ExeAccountChange,
  ExeAccountIntegration,
  ProjectPlacement,
} from "../../services/workspace";
import { reloadOnReturn, type ExeAccount } from "../composables/exeAccount";
import { errorMessage } from "../../utils/errorMessage";
import { useI18n } from "../composables/i18n";

const props = defineProps<{
  account: ExeAccount;
  placement: ProjectPlacement;
  disabled?: boolean;
}>();
const { t } = useI18n();
const view = computed(() => props.account.view.value);
const query = ref("");
const busy = ref("");
const error = ref("");
const others = computed(() =>
  (view.value?.integrations ?? [])
    .filter((ig) => ig.type !== "github")
    .sort((a, b) => Number(b.added) - Number(a.added) || a.name.localeCompare(b.name)),
);
const shownIntegrations = computed(() => {
  const q = query.value.toLowerCase();
  return others.value.filter((ig) =>
    [ig.name, ig.type, ig.title, ig.comment].some((s) => s.toLowerCase().includes(q)),
  );
});
const added = computed(() => (view.value?.integrations ?? []).filter((ig) => ig.added));
const untagged = computed(() =>
  (view.value?.vms ?? []).filter((vm) => vm.found && !vm.tagged).map((vm) => vm.vm_name),
);
const where = computed(() => {
  const vm = view.value?.vms[0]?.vm_name ?? "";
  const key =
    props.placement.mode === "host"
      ? "projectExeWhereHost"
      : props.placement.mode === "shared"
        ? "projectExeWhereShared"
        : vm
          ? "projectExeWhereBase"
          : "projectExeWhereNewBase";
  return t(key).replace("{vm}", vm);
});
// The explanation around the tag, which is shown as code.
const explain = computed(() =>
  t("projectExeExplain").replace("{where}", where.value).split("{tag}"),
);
const icon = (type: string) =>
  ({
    "http-proxy": "pi-globe",
    llm: "pi-sparkles",
    "exe-dev": "pi-server",
    postgres: "pi-database",
    wire: "pi-database",
    notify: "pi-bell",
    reflection: "pi-info-circle",
    "ssh-key": "pi-key",
  })[type] ?? "pi-link";
function status(ig: ExeAccountIntegration) {
  if (ig.added) return t(ig.reaches ? "projectExeStatusAdded" : "projectExeStatusAddedTagged");
  const other = ig.attachments.filter((a) => a !== `tag:${view.value?.tag}`).join(", ");
  if (ig.reaches)
    return t("projectExeStatusReaches").replace(
      "{where}",
      other || t("projectExeStatusAttachedElsewhere"),
    );
  if (ig.comment) return ig.comment;
  return other
    ? t("projectExeStatusAttached").replace("{where}", other)
    : t("projectExeStatusNotAttached");
}
async function run(key: string, change: ExeAccountChange) {
  busy.value = key;
  error.value = "";
  try {
    await props.account.change(change);
  } catch (err) {
    error.value = errorMessage(err);
  } finally {
    busy.value = "";
  }
}
let stopWaiting: (() => void) | undefined;
function awaitReturn() {
  stopWaiting?.();
  stopWaiting = reloadOnReturn(() => {
    stopWaiting?.();
    stopWaiting = undefined;
    void props.account.load();
  });
}
onUnmounted(() => stopWaiting?.());
</script>
