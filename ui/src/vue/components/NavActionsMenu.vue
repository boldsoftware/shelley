<!-- The sidebar's ⋯: one trigger and menu, shared by the drawer's rows. -->
<template>
  <Button
    class="btn-icon-sm drawer-actions-trigger"
    text
    severity="secondary"
    size="small"
    v-tooltip.top="{ value: label, showDelay: 400, hideDelay: 150 }"
    :aria-label="label"
    aria-haspopup="menu"
    :aria-expanded="isOpen"
    @click.stop="menu?.toggle($event)"
  >
    <OverflowDotsIcon class="drawer-actions-trigger-icon" />
  </Button>
  <TieredMenu
    ref="menu"
    :model="items"
    popup
    :aria-label="label"
    :pt="navMenuPt(testid)"
    @show="isOpen = true"
    @hide="isOpen = false"
  />
</template>

<script setup lang="ts">
import { ref } from "vue";
import Button from "primevue/button";
import TieredMenu from "primevue/tieredmenu";
import type { MenuItem } from "primevue/menuitem";
import OverflowDotsIcon from "./OverflowDotsIcon.vue";
import { navMenuPt } from "./navMenu";

defineProps<{ items: MenuItem[]; label: string; testid: string }>();

const menu = ref<InstanceType<typeof TieredMenu> | null>(null);
const isOpen = ref(false);

defineExpose({ hide: () => menu.value?.hide() });
</script>
