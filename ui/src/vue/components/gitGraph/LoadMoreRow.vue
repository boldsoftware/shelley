<!-- Vue port of the LoadMoreRow subcomponent in components/GitGraphViewer.tsx.
     Load-more footer. Shows options when the server returned at least as many
     commits as requested (i.e. there's likely more). Emits "load" with the new
     limit (React onLoad). -->
<template>
  <div v-if="commitsLoaded < limit" class="git-graph-loadmore git-graph-loadmore-end">
    — end of history ({{ commitsLoaded }} commits) —
  </div>
  <div v-else class="git-graph-loadmore">
    <span v-if="loading" class="git-graph-loadmore-loading">Loading…</span>
    <template v-else>
      Load{{ " " }}
      <template v-for="(step, i) in LOAD_STEPS" :key="step">
        <template v-if="i > 0"> / </template>
        <a href="#" class="git-graph-loadmore-link" @click.prevent="emit('load', limit + step)">
          {{ step }} more
        </a>
      </template>
      {{ " / " }}
      <a href="#" class="git-graph-loadmore-link" @click.prevent="emit('load', ALL_LIMIT)"> all </a>
    </template>
  </div>
</template>

<script setup lang="ts">
import { LOAD_STEPS, ALL_LIMIT } from "../gitGraphLayout";

defineProps<{
  limit: number;
  commitsLoaded: number;
  loading: boolean;
}>();
const emit = defineEmits<{ (e: "load", n: number): void }>();
</script>
