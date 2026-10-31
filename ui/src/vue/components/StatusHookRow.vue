<template>
  <div v-if="visible && stale" class="status-hook-row" data-testid="status-bar-hook">&nbsp;</div>
  <div
    v-else-if="visible"
    :class="['status-hook-row', { 'status-hook-row-error': status?.error }]"
    :style="status?.error ? undefined : { backgroundColor: status?.bgcolor, color: status?.color }"
    data-testid="status-bar-hook"
  >
    <template v-if="status?.error">
      <button
        type="button"
        class="status-hook-item-control status-hook-item-action"
        aria-haspopup="dialog"
        :aria-expanded="errorOpen"
        :aria-controls="errorOpen ? errorId : undefined"
        title="Show error details"
        @click="errorPopover?.toggle($event)"
      >
        Status hook failed
      </button>
      <Popover
        ref="errorPopover"
        :pt="{
          root: {
            id: errorId,
            class: 'status-hook-error-popover',
            'aria-label': 'Status hook error',
          },
        }"
        @show="errorOpen = true"
        @hide="errorOpen = false"
      >
        <pre class="status-hook-error-details">{{ status.error }}</pre>
      </Popover>
    </template>
    <div v-else class="status-hook-items">
      <template v-for="(item, i) in items" :key="i">
        <slot v-if="item.action === 'open_models'" name="open_models" :item="item" />
        <template v-else>
          <component
            :is="item.href ? 'a' : item.action ? 'button' : 'span'"
            :type="item.action && !item.href ? 'button' : undefined"
            :href="item.href"
            :target="item.href ? '_blank' : undefined"
            :rel="item.href ? 'noopener noreferrer' : undefined"
            :class="[
              'status-hook-item',
              { 'status-hook-item-control status-hook-item-action': item.action || item.href },
            ]"
            :style="{ backgroundColor: item.bgcolor, color: item.color }"
            :title="item.title || item.body"
            @click="item.action && item.action !== 'open_url' && emit('action', item)"
          >
            <template v-if="item.action || item.href">{{ item.body }}</template>
            <template v-for="(part, j) in item.parts" v-else :key="j">
              <a
                v-if="part.type === 'link'"
                :href="part.href"
                target="_blank"
                rel="noopener noreferrer"
                class="text-link"
                >{{ part.content }}</a
              >
              <template v-else>{{ part.content }}</template>
            </template>
          </component>
        </template>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, useId, watch } from "vue";
import Popover from "primevue/popover";
import type { StatusBar, StatusBarItem } from "../../services/api";
import {
  localhostLinkOptionsFromInit,
  parseLinks,
  rewriteLocalhostLink,
} from "../../utils/linkify";

const props = defineProps<{
  status: StatusBar | null;
  stale: boolean;
}>();
const emit = defineEmits<{
  action: [item: StatusBarItem];
}>();
const errorPopover = ref<InstanceType<typeof Popover>>();
const errorOpen = ref(false);
const errorId = useId();

watch([() => props.status?.error, () => props.stale], () => {
  errorPopover.value?.hide();
  errorOpen.value = false;
});

const items = computed(() => {
  const links = localhostLinkOptionsFromInit();
  return (props.status?.items || []).map((item) => ({
    ...item,
    href: item.action === "open_url" ? rewriteLocalhostLink(item.args!.url!, links) : undefined,
    parts: item.action ? [] : parseLinks(item.body || "", links),
  }));
});
const visible = computed(() => !!props.status?.error || items.value.length > 0);
</script>
