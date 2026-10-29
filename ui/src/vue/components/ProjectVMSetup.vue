<template>
  <p v-if="vm && !vm.base && !vm.shared_vm" class="project-muted project-vm-setup">
    <template v-if="vm.setup_conversation_id">
      <i class="pi pi-spin pi-spinner" aria-hidden="true" />
      <a :href="`/c/${vm.setup_conversation_id}`" @click="open($event, vm.setup_conversation_id)"
        >VM setup in progress</a
      >
    </template>
    <template v-else>
      <i class="pi pi-exclamation-circle" aria-hidden="true" />
      No base VM: save the project to start VM setup
    </template>
  </p>
</template>

<script setup lang="ts">
import type { ProjectVM } from "../../services/workspace";
import { navigateToConversationSlug } from "../composables/subagentLive";

// A VM project's base VM being prepared by its setup conversation. New conversations in the
// project are refused until the base is set.
defineProps<{ vm: ProjectVM | null }>();
const emit = defineEmits<{ (e: "navigate"): void }>();
function open(event: MouseEvent, id: string) {
  if (event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) return;
  event.preventDefault();
  navigateToConversationSlug(id);
  emit("navigate");
}
</script>
