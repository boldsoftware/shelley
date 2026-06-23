<!-- Vue port of the CarriedBand inner component from ChatInterface.tsx.
     Collapses a run of compaction-carried items behind a single toggle. -->
<template>
  <div class="carried-band" data-testid="carried-band">
    <button
      type="button"
      class="carried-band-toggle"
      :aria-expanded="expanded"
      @click="expanded = !expanded"
    >
      <span class="carried-band-chevron" aria-hidden="true">{{
        expanded ? "\u25be" : "\u25b8"
      }}</span>
      <span class="carried-band-label">
        {{ count }} message{{ count === 1 ? "" : "s" }} carried forward from before compaction
      </span>
    </button>
    <div v-if="expanded" class="carried-band-body">
      <slot />
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref } from "vue";

defineProps<{ count: number }>();
const expanded = ref(false);
</script>
