<template>
  <iframe
    class="google-search-suggestions"
    :srcdoc="'<base target=&quot;_blank&quot;>' + html"
    sandbox="allow-popups allow-popups-to-escape-sandbox"
    referrerpolicy="no-referrer"
    title="Google Search suggestions"
  />
</template>

<script setup lang="ts">
defineProps<{ html: string }>();
</script>

<style scoped>
.google-search-suggestions {
  display: block;
  width: 100%;
  height: 5rem;
  border: 0;
  margin-top: 0.5rem;
}
</style>
