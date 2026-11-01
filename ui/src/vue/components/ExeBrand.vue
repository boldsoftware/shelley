<!-- exe's logo (the pufferfish glyph of exe's iOS app, then the
     "exe" wordmark, which exe sets as bold monospace text), shown at the top
     left in workspace mode: atop the drawer, or in the app bar while the
     drawer is collapsed. -->
<template>
  <div class="exe-brand" data-testid="exe-brand">
    <img class="exe-brand-fish" :src="fishUrl" alt="" aria-hidden="true" />
    <span class="exe-brand-wordmark">exe</span>
  </div>
</template>

<script setup lang="ts">
import fish from "../assets/exe-fish.svg";

const fishUrl = `data:image/svg+xml,${encodeURIComponent(fish)}`;
</script>

<style scoped>
.exe-brand {
  display: flex;
  align-items: center;
  gap: 0.375rem;
  flex-shrink: 0;
  margin-right: 0.25rem;
  min-height: 2rem; /* the header's row height (.app-bar-title) */
  user-select: none;
}
/* In the app bar, a rule sets the logo apart from the title after it. */
.header-left .exe-brand {
  margin-right: 0;
  padding-right: 0.75rem;
  border-right: 1px solid var(--border);
  min-height: 1.25rem;
}
.exe-brand-fish {
  display: block;
  width: 1.5rem;
  height: auto;
}
.exe-brand-wordmark {
  font-family: var(--font-mono);
  font-size: 1.0625rem;
  font-weight: 700;
  line-height: 1;
  letter-spacing: -0.02em;
  color: var(--text-primary);
}
</style>
