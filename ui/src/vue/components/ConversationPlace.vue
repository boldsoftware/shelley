<!-- Where a task runs (workspace mode), as one icon in its title row: a VM
     with its connection state as a colored dot, or this host. A click or tap
     spells it out, with the VM's link. -->
<template>
  <button
    type="button"
    class="conversation-place"
    :title="summary"
    :aria-label="summary"
    aria-haspopup="dialog"
    :aria-expanded="open"
    data-testid="conversation-place"
    @click="popover?.toggle($event)"
  >
    <i :class="vm ? 'pi pi-server' : 'pi pi-desktop'" aria-hidden="true" />
    <span v-if="status" class="conversation-place-dot" :class="`place-${status.tone}`" />
  </button>
  <Popover
    ref="popover"
    :pt="{ root: { class: 'conversation-place-popover', 'aria-label': summary } }"
    @show="open = true"
    @hide="open = false"
  >
    <template v-if="vm && status">
      <p>
        {{ vmPlace(vm, t) }}
        <!-- An SSH host (no integration) has no exe.dev page. -->
        <span v-if="vm.status === 'deleted' || !vm.integration">{{ vm.vm_name }}</span>
        <a
          v-else
          :href="vm.https_url || `https://${vm.vm_name}.exe.xyz/`"
          target="_blank"
          rel="noopener noreferrer"
        >
          {{ vm.vm_name }}
          <i class="pi pi-external-link" aria-hidden="true" />
          <span class="sr-only">{{ t("opensInNewTab") }}</span>
        </a>
      </p>
      <p :class="`project-status-${status.tone}`" role="status">
        <span class="conversation-place-dot" :class="`place-${status.tone}`" />
        {{ status.label }}
      </p>
      <p v-if="vm.dir" class="conversation-place-path">{{ vm.dir }}</p>
      <p v-if="archiveNote" class="project-muted">{{ archiveNote }}</p>
      <p v-if="vm.error" class="error-line" role="alert">{{ vm.error }}</p>
    </template>
    <template v-else>
      <p>
        <template v-for="part in worksOnHost" :key="part">
          <strong v-if="part === '{hostname}'">{{ hostname }}</strong>
          <template v-else>{{ part }}</template>
        </template>
      </p>
      <p v-if="cwd" class="conversation-place-path">{{ cwd }}</p>
    </template>
  </Popover>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import Popover from "primevue/popover";
import { useWorkspace } from "../composables/workspace";
import { useI18n } from "../composables/i18n";
import { vmPlace, vmStatus } from "../../utils/vmStatus";

const props = defineProps<{ conversationId: string; cwd: string; hostname: string }>();
const { t } = useI18n();
const workspace = useWorkspace();
const vm = computed(() => workspace.vmFor(props.conversationId));
const status = computed(() => vm.value && vmStatus(vm.value, t));
const worksOnHost = computed(() => t("placeWorksOnHost").split(/(\{hostname\})/));
const archiveNote = computed(() => workspace.archiveNote(props.conversationId));
const summary = computed(() =>
  vm.value && status.value
    ? t(vm.value.shared ? "vmSummaryShared" : "vmSummary")
        .replace("{vm}", vm.value.vm_name)
        .replace("{status}", status.value.label)
    : t("placeThisHost").replace("{hostname}", props.hostname) +
      (props.cwd ? ` · ${props.cwd}` : ""),
);
const popover = ref<InstanceType<typeof Popover>>();
const open = ref(false);
</script>
