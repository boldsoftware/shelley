<template>
  <p v-if="error" class="conversation-themes-error" role="alert">{{ error }}</p>
  <ol v-else-if="expanded && chapters.length" class="conversation-themes-list" @click.stop @auxclick.stop @keydown.stop>
    <li v-for="chapter in chapters" :key="chapter.message_id">
      <a :href="chapterHref(chapter)" @click="onLinkClick($event, chapter)">{{ chapter.title }}</a>
    </li>
  </ol>
</template>

<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref } from "vue";
import { apiFetch } from "../../services/api";
import { fragmentForMessage } from "../../utils/locationFragment";

interface Chapter {
  title: string;
  message_id: string;
}
const props = defineProps<{ conversationId: string; conversationSlug?: string | null }>();
const chapters = ref<Chapter[]>([]);
const expanded = ref(false);
const error = ref("");
const hasChapters = computed(() => chapters.value.length > 0);

async function load() {
  try {
    const res = await apiFetch(
      `/api/conversation/${encodeURIComponent(props.conversationId)}/themes`,
      "Failed to load chapters",
    );
    chapters.value = ((await res.json()) as { chapters: Chapter[] }).chapters;
    error.value = "";
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function onChaptersUpdated(event: Event) {
  if ((event as CustomEvent<string>).detail === props.conversationId) void load();
}
onMounted(() => {
  void load();
  window.addEventListener("shelley-chapters-updated", onChaptersUpdated);
});
onBeforeUnmount(() => window.removeEventListener("shelley-chapters-updated", onChaptersUpdated));

function chapterHref(chapter: Chapter): string {
  return `/c/${encodeURIComponent(props.conversationSlug || props.conversationId)}#${fragmentForMessage(chapter.message_id)}`;
}

function onLinkClick(event: MouseEvent, chapter: Chapter) {
  if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
  event.preventDefault();
  window.history.pushState({}, "", chapterHref(chapter));
  window.dispatchEvent(new PopStateEvent("popstate"));
}

defineExpose({ hasChapters, expanded, toggle: () => { expanded.value = !expanded.value; } });
</script>

<style>
.conversation-themes-list {
  list-style: none;
  margin: 0.625rem 0 0.5rem 0.5rem;
  padding: 0 0 0 0.75rem;
  border-left: 1px solid color-mix(in srgb, currentColor 25%, transparent);
  font-size: var(--font-size-12);
  line-height: 1.5;
  color: inherit;
  cursor: auto;
}
.conversation-themes-list li + li { margin-top: 0.5rem; }
.conversation-themes-list a {
  display: block;
  color: inherit;
  opacity: 0.85;
  text-decoration: none;
  cursor: pointer;
  padding: 0.25rem 0.375rem;
  margin: -0.25rem -0.375rem;
  border-radius: 0.25rem;
}
.conversation-themes-list a:hover,
.conversation-themes-list a:focus-visible {
  opacity: 1;
  background: color-mix(in srgb, currentColor 12%, transparent);
  text-decoration: underline;
}
.conversation-themes-toggle {
  border: 0;
  background: transparent;
  color: inherit;
  opacity: 0.65;
  padding: 0;
  width: 1rem;
  height: 1.25rem;
  flex: 0 0 1rem;
  cursor: pointer;
}
.conversation-themes-toggle:hover { opacity: 1; }
</style>
