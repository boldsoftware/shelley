<!-- A {{secret:ID}} placeholder in a message (docs/secrets.md): the ID and
     blurred filler derived from it, never the value, linking to the
     secrets page. markdownRender.ts builds the same markup. -->
<template>
  <a class="secret-pill" :href="pill.href" target="_blank" rel="noopener" :title="pill.title"
    ><i class="pi pi-lock" aria-hidden="true" /><span class="secret-pill-id">{{ id }}</span
    ><span class="secret-pill-filler" aria-hidden="true">{{ pill.filler }}</span></a
  >
</template>

<script setup lang="ts">
import { computed } from "vue";
import { secretPill } from "../../utils/secretDetect";

const props = defineProps<{ id: string }>();
const pill = computed(() => secretPill(props.id));
</script>
