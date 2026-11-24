// What the chat API will take out of the composer's message as secrets
// (docs/secrets.md): the server is asked, debounced, which spans it would
// extract.
import { ref, watch, type Ref } from "vue";
import { secretsApi, type SecretSpan } from "../../services/api";

const DEBOUNCE_MS = 250;

export interface SecretPreview {
  /** The text the spans are for. */
  text: string;
  spans: SecretSpan[];
}

export function useSecretPreview(text: () => string, enabled: Ref<boolean>) {
  // The last answer, kept while a newer one is on its way.
  const preview = ref<SecretPreview>({ text: "", spans: [] });
  let timer: ReturnType<typeof setTimeout> | undefined;
  let controller: AbortController | undefined;

  /** Drops the check on its way, if any. */
  function cancel() {
    clearTimeout(timer);
    controller?.abort();
  }

  function check(value: string) {
    cancel();
    if (!enabled.value || !value.trim()) {
      preview.value = { text: value, spans: [] };
      return;
    }
    timer = setTimeout(async () => {
      const c = (controller = new AbortController());
      try {
        const spans = await secretsApi.detect(value, c.signal);
        if (!c.signal.aborted) preview.value = { text: value, spans };
      } catch {
        // Unchecked: no spans to show.
      }
    }, DEBOUNCE_MS);
  }
  watch([text, enabled], ([value]) => check(value), { immediate: true });

  return { preview, cancel };
}
