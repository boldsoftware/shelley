<!-- Vue port of components/AgentsMdEditorModal.tsx. Thin wrapper that reads
     the user AGENTS.md path from window.__SHELLEY_INIT__ and renders
     EditableFileModal with loadUrl="/api/user-agents-md". Emits "close"
     (React onClose). -->
<template>
  <EditableFileModal
    :is-open="isOpen"
    :path="filePath"
    title="Edit AGENTS.md"
    load-url="/api/user-agents-md"
    @close="emit('close')"
  />
</template>

<script setup lang="ts">
import EditableFileModal from "./EditableFileModal.vue";

defineProps<{
  isOpen: boolean;
}>();
const emit = defineEmits<{ (e: "close"): void }>();

const filePath = window.__SHELLEY_INIT__?.user_agents_md_path || "";
</script>
