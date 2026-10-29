<!-- The sidebar's ⋯: one trigger and menu for conversation rows and project
     headings. Items may nest (a submenu); a disabled item's `title` says why.
     open() shows it anchored to the trigger, for right-clicks. -->
<template>
  <Button
    ref="trigger"
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
import { ref, type ComponentPublicInstance } from "vue";
import Button from "primevue/button";
import TieredMenu from "primevue/tieredmenu";
import type { MenuItem } from "primevue/menuitem";
import OverflowDotsIcon from "./OverflowDotsIcon.vue";
import { navMenuPt } from "./navMenu";

defineProps<{ items: MenuItem[]; label: string; testid: string }>();

const trigger = ref<ComponentPublicInstance | null>(null);
const menu = ref<InstanceType<typeof TieredMenu> | null>(null);
const isOpen = ref(false);

defineExpose({
  open(e: MouseEvent) {
    e.preventDefault();
    if (!isOpen.value) (trigger.value?.$el as HTMLElement).click();
  },
  hide: () => menu.value?.hide(),
});
</script>
