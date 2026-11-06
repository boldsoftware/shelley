<!-- Files shared in Slack: a user's, saved to the uploads directory, which
     /api/read serves; or the Slack Assistant's, from anywhere, of which images
     preview if /api/read serves them (screenshots do) and the rest just show. -->
<template>
  <div class="message-user-attachments" data-testid="slack-files">
    <template v-for="path in paths" :key="path">
      <a
        v-if="isImagePath(path) && path.startsWith('/') && !broken.has(path)"
        class="message-user-image"
        :href="readURL(path)"
        target="_blank"
        rel="noopener"
        :title="path"
      >
        <img :src="readURL(path)" :alt="fileName(path)" loading="lazy" @error="broken.add(path)" />
      </a>
      <a
        v-else-if="served"
        class="message-user-file"
        :href="readURL(path)"
        :download="fileName(path)"
        :title="path"
      >
        <i class="pi pi-file" aria-hidden="true" />
        <span class="truncate">{{ fileName(path) }}</span>
      </a>
      <span v-else class="message-user-file" :title="path">
        <i class="pi pi-file" aria-hidden="true" />
        <span class="truncate">{{ fileName(path) }}</span>
      </span>
    </template>
  </div>
</template>

<script setup lang="ts">
import { reactive } from "vue";
import { fileName, isImagePath } from "../../utils/slack";

defineProps<{ paths: string[]; served?: boolean }>();

const broken = reactive(new Set<string>());

function readURL(path: string): string {
  return `/api/read?${new URLSearchParams({ path })}`;
}
</script>
