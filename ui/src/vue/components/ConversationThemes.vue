<template>
  <ol v-if="chapters.length" class="conversation-themes-list" @click.stop @auxclick.stop @keydown.stop>
    <li v-for="chapter in chapters" :key="chapter.message_id">
      <a
        :href="chapterHref(chapter)"
        :class="{ 'conversation-chapter-match': chapterMatchScore(chapter.title, searchText) > 0 }"
        @click="onLinkClick($event, chapter)"
      >
        <template v-for="(segment, i) in chapterSearchSegments(chapter.title, searchText)" :key="i">
          <mark v-if="segment.mark" class="conversation-snippet-mark">{{ segment.text }}</mark>
          <template v-else>{{ segment.text }}</template>
        </template>
      </a>
    </li>
  </ol>
</template>

<script setup lang="ts">
import { fragmentForMessage } from "../../utils/locationFragment";
import { chapterMatchScore, chapterSearchSegments, type Chapter } from "../../utils/chapterSearch";

const props = defineProps<{
  conversationId: string;
  conversationSlug?: string | null;
  chapters: Chapter[];
  searchText: string;
}>();

function chapterHref(chapter: Chapter): string {
  return `/c/${encodeURIComponent(props.conversationSlug || props.conversationId)}#${fragmentForMessage(chapter.message_id)}`;
}

function onLinkClick(event: MouseEvent, chapter: Chapter) {
  if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
  event.preventDefault();
  window.history.pushState({}, "", chapterHref(chapter));
  window.dispatchEvent(new PopStateEvent("popstate"));
}

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
.conversation-themes-list a.conversation-chapter-match {
  opacity: 1;
  font-weight: 600;
}
</style>
