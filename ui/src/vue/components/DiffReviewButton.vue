<template>
  <button
    type="button"
    class="diff-draft-review-toggle"
    :class="{ 'has-comments': count > 0 }"
    :disabled="disabled"
    :aria-label="`Open draft review, ${count} ${count === 1 ? 'comment' : 'comments'}`"
    aria-haspopup="dialog"
    @click="emit('open', $event)"
  >
    <svg aria-hidden="true" width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round">
      <path d="M3.5 2.75h9a1 1 0 0 1 1 1v6a1 1 0 0 1-1 1H7l-3.5 2.5v-2.5a1 1 0 0 1-1-1v-6a1 1 0 0 1 1-1Z" />
      <path d="M5 5.5h6M5 8h4" />
    </svg>
    <span aria-hidden="true">{{ count || "" }}</span>
    <span class="diff-draft-review-caption" aria-hidden="true">{{ count ? (count === 1 ? "comment" : "comments") : "Review" }}</span>
  </button>
</template>

<script setup lang="ts">
defineProps<{ count: number; disabled?: boolean }>();
const emit = defineEmits<{ (e: "open", event: MouseEvent): void }>();
</script>
