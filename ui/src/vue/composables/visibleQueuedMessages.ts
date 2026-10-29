import { shallowRef, watch } from "vue";
import type { QueuedMessage } from "../../types";
import { messageSource, actionReportSource } from "../../utils/messageSource";

const NOTICE_DELAY_MS = 2000;

export function useVisibleQueuedMessages(queuedMessages: () => QueuedMessage[]) {
  const visible = shallowRef<QueuedMessage[]>([]);

  watch(
    queuedMessages,
    (messages, _previous, onCleanup) => {
      let timer: ReturnType<typeof setTimeout> | undefined;
      function update() {
        const now = Date.now();
        let nextDelay = Infinity;
        visible.value = messages.filter((message) => {
          // The run's card already shows a queued action report.
          if (actionReportSource(message.user_data)) return false;
          if (!messageSource(message.user_data) || message.kind === "transcription") return true;
          const remaining = Date.parse(message.created_at) + NOTICE_DELAY_MS - now;
          if (remaining <= 0) return true;
          nextDelay = Math.min(nextDelay, remaining);
          return false;
        });
        if (nextDelay < Infinity) timer = setTimeout(update, nextDelay);
      }
      update();
      onCleanup(() => clearTimeout(timer));
    },
    { immediate: true },
  );

  return visible;
}
