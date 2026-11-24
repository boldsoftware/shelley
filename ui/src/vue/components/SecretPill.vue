<!-- A secret's marker in a message (docs/secrets.md): the ID and blurred
     filler derived from it, never the value. Clicking it opens the secrets
     modal (App.vue). markdownRender.ts builds the same markup. -->
<template>
  <button
    type="button"
    class="secret-pill"
    :data-secret-id="id"
    :aria-label="`Secret ${id}`"
    aria-haspopup="dialog"
    ><i class="pi pi-lock" aria-hidden="true" /><span class="secret-pill-id">{{ id }}</span
    ><span class="secret-pill-filler" aria-hidden="true">{{ secretFiller(id) }}</span></button
  >
</template>

<script setup lang="ts">
import { secretFiller } from "../../utils/secretMarkers";

defineProps<{ id: string }>();
</script>
