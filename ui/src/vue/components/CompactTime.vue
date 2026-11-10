<!-- A sidebar row's age ("5m", "2d"), kept current; its title is the full time. -->
<template>
  <span class="conversation-time" :title="formatAbsolute(date)">{{
    formatCompactTime(now - date.getTime(), t)
  }}</span>
</template>

<script lang="ts">
import { ref } from "vue";

// One clock for every row, ticking while any is mounted.
const now = ref(Date.now());
let users = 0;
let timer: ReturnType<typeof setInterval> | null = null;
</script>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted } from "vue";
import { useI18n } from "../composables/i18n";
import { formatCompactTime } from "../../utils/compactTime";
import { formatAbsolute } from "../../utils/messageTime";

const props = defineProps<{ timestamp: string }>();
const { t } = useI18n();
const date = computed(() => new Date(props.timestamp));

onMounted(() => {
  if (users++ === 0) {
    now.value = Date.now();
    timer = setInterval(() => (now.value = Date.now()), 30_000);
  }
});
onBeforeUnmount(() => {
  if (--users === 0) {
    clearInterval(timer!);
    timer = null;
  }
});
</script>
