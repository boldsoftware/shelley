<!-- Sub-component of Message.vue (ported from GitInfoMessage in
     components/Message.tsx). Compact git-state notification. Preserves the
     .message.message-gitinfo container, data-testid "message-gitinfo", and the
     msg-* classes for the worktree/branch/hash/subject/diff link. -->
<template>
  <div
    v-if="commitHash"
    class="message message-gitinfo msg-gitinfo-container"
    data-testid="message-gitinfo"
  >
    <div class="msg-gitinfo-content">
      <span v-if="subject" class="msg-subject" :title="subject">{{ subject }}</span>
      <span v-if="branch" class="msg-branch">{{ branch }}</span>
      <button type="button" class="msg-commit-hash" title="Click to copy commit hash" @click="handleCopyHash">{{ shortCommitHash }}</button>
      <span class="msg-copy-status" aria-live="polite">{{ copied ? "Commit hash copied" : "" }}</span>
      <span v-if="worktree" ref="worktreeRef" class="msg-worktree" :title="worktree">{{ worktreeLabel }}</span>
      <a v-if="canShowDiff" :href="diffHref" class="msg-diff-link" @click="onDiffLinkClick">Open Diff</a>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import type { Message as MessageType } from "../../types";
import { pathShortener } from "./gitGraph/shortenPaths";
import { tildifyPath } from "../../utils/tildify";

const props = defineProps<{
  message: MessageType;
  onOpenDiffViewer?: (commit: string, cwd?: string) => void;
}>();

const copied = ref(false);
let disposed = false;

const parsed = computed(() => {
  let commitHash: string | null = null;
  let subject: string | null = null;
  let branch: string | null = null;
  let worktree: string | null = null;

  if (props.message.user_data) {
    try {
      const userData =
        typeof props.message.user_data === "string"
          ? JSON.parse(props.message.user_data)
          : props.message.user_data;
      if (userData.commit) commitHash = userData.commit;
      if (userData.subject) subject = userData.subject;
      if (userData.branch) branch = userData.branch;
      if (userData.worktree) worktree = userData.worktree;
    } catch (err) {
      console.error("Failed to parse gitinfo user_data:", err);
    }
  }
  return { commitHash, subject, branch, worktree };
});

const commitHash = computed(() => parsed.value.commitHash);
const subject = computed(() => parsed.value.subject);
const branch = computed(() => parsed.value.branch);
const worktree = computed(() => parsed.value.worktree);
const shortCommitHash = computed(() => commitHash.value?.slice(0, 8) ?? "");
const worktreeRef = ref<HTMLElement | null>(null);
const displayWorktree = computed(() => tildifyPath(worktree.value) || "");
const worktreeLabel = ref(displayWorktree.value);
let worktreeObserver: ResizeObserver | null = null;

onMounted(() => {
  const element = worktreeRef.value;
  if (!element) return;
  const context = document.createElement("canvas").getContext("2d");
  if (!context) throw new Error("Cannot measure worktree paths");
  const updateLabel = () => {
    if (disposed) return;
    const path = displayWorktree.value;
    const width = element.getBoundingClientRect().width;
    context.font = getComputedStyle(element).font;
    worktreeLabel.value = pathShortener([path])(
      (text) => context.measureText(text).width <= width,
    ).get(path)!;
  };
  worktreeObserver = new ResizeObserver(updateLabel);
  worktreeObserver.observe(element);
  watch(displayWorktree, updateLabel, { flush: "post" });
  void document.fonts.ready.then(updateLabel);
});

const canShowDiff = computed(() => !!commitHash.value && !!props.onOpenDiffViewer);

const diffHref = computed(() => {
  const params = new URLSearchParams();
  params.set("diff", commitHash.value!);
  if (worktree.value) params.set("cwd", worktree.value);
  return `${window.location.pathname}?${params.toString()}`;
});

function onDiffLinkClick(e: MouseEvent) {
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
  e.preventDefault();
  if (commitHash.value && props.onOpenDiffViewer) {
    props.onOpenDiffViewer(commitHash.value, worktree.value || undefined);
  }
}

onBeforeUnmount(() => {
  disposed = true;
  worktreeObserver?.disconnect();
});

function handleCopyHash(e: MouseEvent) {
  e.preventDefault();
  if (commitHash.value) {
    navigator.clipboard.writeText(commitHash.value).then(() => {
      copied.value = true;
      setTimeout(() => (copied.value = false), 1500);
    });
  }
}
</script>
