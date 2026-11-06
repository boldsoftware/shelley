// The Slack Assistant's directory (see utils/slack.ts): who and where the
// Slack IDs in its messages and replies are. ChatInterface provides it; it is
// computed only when a Slack message or reply reads it.
import { computed, inject, type ComputedRef, type InjectionKey } from "vue";
import { EMPTY_SLACK_DIRECTORY, type SlackDirectory } from "../../utils/slack";

export const slackDirectoryKey: InjectionKey<ComputedRef<SlackDirectory>> =
  Symbol("slackDirectory");

export function useSlackDirectory(): ComputedRef<SlackDirectory> {
  return inject(
    slackDirectoryKey,
    computed(() => EMPTY_SLACK_DIRECTORY),
    false,
  );
}
