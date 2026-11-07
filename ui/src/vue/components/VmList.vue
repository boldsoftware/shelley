<!-- The sidebar's prod tab: the user's exe.dev VMs. Project VMs (project base
     VMs and conversations' copies of them) are tucked into a collapsed group,
     since they are project infrastructure rather than something to look at.
     A VM's view changes it, in its settings. -->
<template>
  <div class="vm-list-toolbar">
    <span class="text-secondary">{{
      loading ? "Loading VMs…" : setupUrl ? "" : `${vms.length} VMs`
    }}</span>
    <Button
      class="btn-icon"
      text
      severity="secondary"
      aria-label="Refresh VMs"
      v-tooltip.top="'Refresh VMs'"
      :disabled="loading"
      data-testid="vm-refresh"
      @click="load(true)"
    >
      <i class="pi pi-refresh" aria-hidden="true" />
    </Button>
  </div>
  <div class="drawer-body scrollable">
    <p v-if="error" class="error-line drawer-error vm-list-error" role="alert" :title="error">
      {{ error }}
    </p>
    <div v-if="setupUrl" class="vm-setup" data-testid="vm-setup">
      <p>
        Listing your VMs needs an exe.dev integration attached to this VM, so Shelley can use
        exe.dev as you.
      </p>
      <a :href="setupUrl" target="_blank" rel="noopener" class="vm-setup-link">
        Set up an exe.dev integration <i class="pi pi-external-link" aria-hidden="true" />
      </a>
      <p class="text-secondary">Then refresh this list.</p>
    </div>
    <div class="conversation-list">
      <VmRow
        v-for="vm in apps"
        :key="vm.name"
        :vm="vm"
        :active="vm.name === currentVm"
        @select="emit('select', vm.name)"
      />
      <p
        v-if="!loading && !error && !setupUrl && !apps.length"
        class="text-secondary drawer-empty-state"
      >
        No VMs.
      </p>
      <template v-if="projectVms.length">
        <button
          type="button"
          class="vm-group-toggle"
          :aria-expanded="projectVmsOpen"
          data-testid="vm-project-toggle"
          @click="projectVmsOpen = !projectVmsOpen"
        >
          <i
            :class="`pi ${projectVmsOpen ? 'pi-chevron-down' : 'pi-chevron-right'}`"
            aria-hidden="true"
          />
          Project VMs ({{ projectVms.length }})
        </button>
        <template v-if="projectVmsOpen">
          <VmRow
            v-for="vm in projectVms"
            :key="vm.name"
            :vm="vm"
            :active="vm.name === currentVm"
            @select="emit('select', vm.name)"
          />
        </template>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import Button from "primevue/button";
import VmRow from "./VmRow.vue";
import { exeVmsApi, exeVmsChanged, type ExeVM } from "../../services/exeVms";
import { absorbUnreachable, unreachableSince } from "../../services/serverReachability";
import { errorMessage } from "../../utils/errorMessage";

const props = defineProps<{ currentVm: string | null }>();
const emit = defineEmits<{ select: [name: string] }>();

const vms = ref<ExeVM[]>([]);
const loading = ref(false);
const error = ref("");
/** The last load found Shelley unreachable, which the drawer shows: load
 * again once it answers. */
let unreachable = false;
const setupUrl = ref("");
const apps = computed(() => vms.value.filter((vm) => !vm.role));
const projectVms = computed(() => vms.value.filter((vm) => vm.role));
// Open when the VM on screen is in it, so the active row is visible.
const projectVmsOpen = ref(false);

async function load(refresh: boolean) {
  loading.value = true;
  error.value = "";
  unreachable = false;
  try {
    const list = await exeVmsApi.list(refresh);
    vms.value = list.vms;
    setupUrl.value = list.setup_url ?? "";
    if (projectVms.value.some((vm) => vm.name === props.currentVm)) projectVmsOpen.value = true;
  } catch (err) {
    if (absorbUnreachable(err)) unreachable = true;
    else error.value = errorMessage(err);
  } finally {
    loading.value = false;
  }
}
watch(unreachableSince, (since) => {
  if (since === null && unreachable) void load(false);
});

// A VM's settings changed it: the server's list is fresh.
watch(exeVmsChanged, () => void load(false));

// Mounted whenever the prod tab opens: the server's brief cache keeps this cheap.
onMounted(() => void load(false));
</script>

<style scoped>
.vm-list-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0.125rem 0.375rem 0 0.875rem;
  font-size: var(--font-size-12);
}
.vm-list-error {
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.vm-setup {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  padding: 0.75rem 0.875rem;
  font-size: var(--font-size-13);
}
.vm-setup p {
  margin: 0;
}
.vm-setup-link {
  display: inline-flex;
  align-items: center;
  gap: 0.375rem;
  color: var(--link-color);
  font-weight: 500;
}
.vm-group-toggle {
  display: flex;
  align-items: center;
  gap: 0.375rem;
  width: 100%;
  padding: 0.375rem 0.5rem;
  font-size: var(--font-size-12);
  color: var(--text-secondary);
  background: none;
  border: none;
  cursor: pointer;
}
</style>
