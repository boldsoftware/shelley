<!-- A screenshot or recording embedded in a commit tour. Images open the
     shared image annotation view (ImageCommentModal), exactly like images in
     the conversation; recordings get a comment pinned to the current playback
     time. Either way the comment names the git blob, so the agent can
     retrieve the file with `git cat-file blob`. -->
<template>
  <article v-show="visible" class="commit-tour-media">
    <div class="commit-tour-media-header">
      <span class="commit-tour-media-kind">{{ video ? "Video" : "Image" }}</span>
      <code :title="entry.name">{{ entry.name }}</code>
      <button
        v-tooltip.top="video ? 'Comment at the current time' : 'Comment on this image'"
        type="button"
        class="commit-tour-media-comment-btn"
        :aria-label="`Comment on ${entry.name}`"
        @click="openComment"
      >
        💬
      </button>
    </div>
    <MarkdownContent v-if="entry.comment" class="commit-tour-media-caption" :text="entry.comment" />
    <div class="commit-tour-media-stage" @error.capture="failed = true">
      <p v-if="failed" class="commit-tour-media-error" role="alert">
        Couldn't show {{ entry.name }} (git blob {{ shortBlob }}).
        <a :href="src" target="_blank" rel="noopener noreferrer">Open the file</a>
      </p>
      <video
        v-else-if="video"
        ref="videoRef"
        class="commit-tour-media-video"
        :src="src"
        controls
        preload="metadata"
        playsinline
      />
      <CommentableImage v-else :src="src" :alt="entry.name" :path="commentRef" />
    </div>
  </article>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import type { GitTourMediaEntry } from "../../services/api";
import { openImageComment } from "../composables/imageComment";
import type { TourCommentTarget } from "../composables/tourComments";
import CommentableImage from "./CommentableImage.vue";
import MarkdownContent from "./MarkdownContent.vue";
import { isVideoMedia } from "./commitTourContents";

const props = defineProps<{
  entry: GitTourMediaEntry;
  src: string;
  visible: boolean;
}>();
const emit = defineEmits<{
  (e: "comment", target: TourCommentTarget): void;
}>();

const videoRef = ref<HTMLVideoElement | null>(null);
watch(
  () => props.visible,
  (visible) => {
    if (!visible) videoRef.value?.pause();
  },
);
const failed = ref(false);
// The view reuses this component by position, so a new tour can hand it
// different media.
watch(
  () => props.src,
  () => (failed.value = false),
);
const video = computed(() => isVideoMedia(props.entry));
const shortBlob = computed(() => props.entry.blob.slice(0, 12));
// How comments refer to the file: its name for the reader, the blob for the
// agent, which may no longer have the original path.
const commentRef = computed(() => `${props.entry.name} (git blob ${shortBlob.value})`);

// m:ss.s, which ffmpeg's -ss accepts for pulling out the frame.
function formatTime(seconds: number): string {
  const tenths = Math.round(seconds * 10);
  const rest = ((tenths % 600) / 10).toFixed(1).padStart(4, "0");
  return `${Math.floor(tenths / 600)}:${rest}`;
}

function openComment() {
  if (!video.value) {
    openImageComment({ src: props.src, path: commentRef.value });
    return;
  }
  const player = videoRef.value;
  player?.pause();
  const at = formatTime(player?.currentTime ?? 0);
  emit("comment", {
    where: `${props.entry.name} at ${at}`,
    reference: `video ${commentRef.value} at ${at}`,
  });
}
</script>

<style scoped>
.commit-tour-media {
  overflow: hidden;
  border: 1px solid var(--border-color);
  border-radius: 0.5rem;
  background: var(--bg-base);
  scroll-margin-top: 1rem;
}

.commit-tour-media-header {
  min-height: 2.5rem;
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.5rem 0.75rem;
  background: var(--bg-secondary);
  color: var(--text-primary);
}

.commit-tour-media-kind {
  flex-shrink: 0;
  padding: 0.0625rem 0.3rem;
  border: 1px solid var(--border-color);
  border-radius: 0.25rem;
  color: var(--text-secondary);
  font-size: 0.6875rem;
  line-height: 1.2;
}

.commit-tour-media-header code {
  min-width: 0;
  flex: 1 1 auto;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--font-mono, monospace);
  font-size: 0.8125rem;
}

.commit-tour-media-comment-btn {
  flex: 0 0 auto;
  width: 1.75rem;
  height: 1.75rem;
  padding: 0;
  border: 0;
  border-radius: 0.25rem;
  background: transparent;
  cursor: pointer;
}

.commit-tour-media-comment-btn:hover {
  background: var(--bg-tertiary);
}

.commit-tour-media-caption {
  padding: 0.875rem 1rem;
  border-top: 1px solid var(--border-color);
  border-bottom: 1px solid var(--border-color);
  background: var(--bg-secondary);
}

.commit-tour-media-caption :deep(> :first-child) {
  margin-top: 0;
}

.commit-tour-media-caption :deep(> :last-child) {
  margin-bottom: 0;
}

.commit-tour-media-stage {
  display: flex;
  justify-content: center;
  padding: 0.75rem;
  background: var(--bg-tertiary);
}

.commit-tour-media-stage :deep(img),
.commit-tour-media-video {
  display: block;
  max-width: 100%;
  max-height: 75vh;
  height: auto;
  box-shadow: 0 0 0 1px var(--border-color);
}

.commit-tour-media-video {
  background: #000;
}

.commit-tour-media-error {
  margin: 0;
  color: var(--red-text, #dc2626);
  font-size: 0.8125rem;
}

@media (max-width: 767px) {
  .commit-tour-media {
    border-right: 0;
    border-left: 0;
    border-radius: 0;
  }

  .commit-tour-media-stage {
    padding: 0;
  }
}
</style>
