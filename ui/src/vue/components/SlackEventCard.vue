<!-- A Slack event delivered to the Slack Assistant (see utils/slack.ts), as
     the chat it is: who wrote where, and what (mrkdwn rendered, files shown);
     or a reaction or click, on one line. The header expands into the text the
     model read, verbatim. -->
<template>
  <div class="slack-event" data-testid="slack-event" :data-slack-kind="event.kind">
    <button
      type="button"
      class="slack-event-header"
      :aria-expanded="showRaw"
      :title="title"
      @click="showRaw = !showRaw"
    >
      <SlackMark />
      <span class="truncate">
        <template v-if="event.kind === 'message'">
          <strong class="slack-event-who">{{ who }}</strong> · {{ where }}
        </template>
        <template v-else>{{ where }}</template>
      </span>
      <ToolChevron :expanded="showRaw" />
    </button>
    <template v-if="event.kind === 'message'">
      <SlackText v-if="event.text" :text="event.text" />
      <SlackFiles v-if="event.files.length" :paths="event.files" served />
    </template>
    <div v-else-if="event.kind === 'reaction'" class="slack-event-line">
      <strong class="slack-event-who">{{ who }}</strong> reacted
      <span class="slack-event-emoji" :title="`:${event.emoji}:`">{{
        slackEmoji(event.emoji)
      }}</span>
      to <span :title="plainText">“{{ excerpt(plainText, 80) }}”</span>
    </div>
    <div v-else class="slack-event-line" :title="event.text">
      <strong class="slack-event-who">{{ who }}</strong> clicked
      <span class="slack-event-button">{{ emojify(clickedLabel(event.text)) }}</span>
    </div>
    <div v-if="showRaw" class="tool-code slack-event-raw" data-testid="slack-event-raw">
      {{ text }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { clickedLabel, slackKey, slackPlace, type SlackEvent } from "../../utils/slack";
import { emojify, excerpt, mrkdwnText, slackEmoji } from "../../utils/slackMrkdwn";
import { useSlackDirectory } from "./slackDirectory";
import SlackFiles from "./SlackFiles.vue";
import SlackMark from "./SlackMark.vue";
import SlackText from "./SlackText.vue";
import ToolChevron from "./tools/ToolChevron.vue";

const props = defineProps<{ event: SlackEvent; text: string }>();

const showRaw = ref(false);
const directory = useSlackDirectory();

const who = computed(
  () => props.event.userName || directory.value.users.get(props.event.user) || props.event.user,
);
const inThread = computed(() => props.event.threadTS !== props.event.ts);
const where = computed(() => {
  const place = slackPlace(props.event.channel, directory.value);
  return inThread.value ? `${place} · thread` : place;
});

const title = computed(() => {
  const { channel, threadTS, ts } = props.event;
  const root = directory.value.messages.get(slackKey(channel, threadTS));
  const lines = [`${channel}, ts ${ts}`];
  if (inThread.value) {
    lines.push(
      root
        ? `In the thread of ${root.userName || root.user}: ${excerpt(mrkdwnText(root.text, directory.value), 100)}`
        : `In the thread ${threadTS}`,
    );
  }
  return lines.join("\n");
});

const plainText = computed(() => mrkdwnText(props.event.text, directory.value));
</script>

<style scoped>
.slack-event {
  display: flex;
  flex-direction: column;
  gap: 0.375rem;
}
.slack-event-header {
  display: flex;
  align-items: center;
  align-self: flex-end;
  gap: 0.375rem;
  max-width: 100%;
  padding: 0;
  border: 0;
  background: none;
  color: var(--text-secondary);
  font: inherit;
  font-size: var(--font-size-12);
  cursor: pointer;
}
.slack-event-header:hover {
  color: var(--text-primary);
}
.slack-event-who {
  color: var(--text-primary);
  font-weight: 600;
}
.slack-event-line {
  color: var(--text-secondary);
}
.slack-event-emoji {
  font-size: 1.125rem;
  line-height: 1;
}
.slack-event-button {
  display: inline-block;
  padding: 0 0.5rem;
  border: 1px solid var(--border);
  border-radius: 0.375rem;
  color: var(--text-primary);
  font-size: var(--font-size-13);
  font-weight: 600;
}
.slack-event-raw {
  font-size: var(--font-size-12);
}
</style>
