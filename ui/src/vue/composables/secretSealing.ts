// The composer's secret sealing (docs/secrets.md): replaces detected
// secrets with {{secret:ID}} placeholders, uploads their values to the
// secret store, and offers Undo until the message is sent.
import { reactive, ref, type Ref } from "vue";
import { secretsApi } from "../../services/api";
import {
  detectSecrets,
  newSecretId,
  sealFindings,
  secretKindLabel,
  secretPlaceholder,
  type SecretFinding,
} from "../../utils/secretDetect";

export interface SealedSecret {
  id: string;
  kind: string;
  value: string;
  status: "uploading" | "ready" | "error";
  error?: string;
  upload: Promise<void>;
}

/** Whether a finding is complete given the caret: a manual mark once the
 *  caret is not inside it, a detected token once the caret is not touching
 *  it (at its end, it may still be being typed). */
export function findingComplete(f: SecretFinding, caret: number): boolean {
  return f.manual ? caret <= f.start || caret >= f.end : caret < f.start || caret > f.end;
}

export function useSecretSealing(enabled: Ref<boolean>, conversationId: () => string | undefined) {
  const sealed = ref<SealedSecret[]>([]);
  const ignore = new Set<string>();
  // Detection runs on every keystroke and caret move, often on the same
  // text: remember the last answer until the text or ignore changes.
  let last: { text: string; findings: SecretFinding[] } | null = null;
  function detect(text: string): SecretFinding[] {
    if (last?.text !== text) last = { text, findings: detectSecrets(text, ignore) };
    return last.findings;
  }

  function upload(entry: SealedSecret, name: string) {
    entry.upload = secretsApi
      .create({
        id: entry.id,
        name,
        kind: entry.kind,
        value: entry.value,
        conversation_id: conversationId() ?? null,
      })
      .then(
        () => {
          entry.status = "ready";
        },
        (e: unknown) => {
          entry.status = "error";
          entry.error = e instanceof Error ? e.message : String(e);
        },
      );
  }

  // A failed delete leaves an unused secret on the management page.
  async function remove(id: string) {
    try {
      await secretsApi.remove(id);
    } catch (e) {
      console.error(`secrets: deleting ${id}`, e);
    }
  }

  /** Seals the complete findings in text (all of them if all), returning
   *  the new text and where the caret moves. */
  function seal(text: string, caret: number, all: boolean): { text: string; caret: number } {
    if (!enabled.value) return { text, caret };
    const findings = detect(text).filter((f) => all || findingComplete(f, caret));
    if (findings.length === 0) return { text, caret };
    const withIds = findings.map((f) => {
      const entry: SealedSecret = reactive({
        id: newSecretId(f.name ?? f.kind),
        kind: f.kind,
        value: f.value,
        status: "uploading",
        upload: Promise.resolve(),
      });
      upload(entry, f.name ?? secretKindLabel(f.kind));
      sealed.value.push(entry);
      return { ...f, id: entry.id };
    });
    let moved = caret;
    for (const f of withIds) {
      if (caret >= f.end) moved += secretPlaceholder(f.id).length - (f.end - f.start);
    }
    return { text: sealFindings(text, withIds), caret: moved };
  }

  /** Whether text holds a secret that is not sealed yet. */
  function holdsUnsealed(text: string): boolean {
    return enabled.value && detect(text).length > 0;
  }

  /** Puts a sealed secret's value back into text and forgets it: deleted
   *  from the store, and never detected again in this message. */
  function undo(id: string, text: string): { text: string; deleted: Promise<void> } {
    const entry = sealed.value.find((s) => s.id === id);
    if (!entry) throw new Error(`no sealed secret ${id}`);
    sealed.value = sealed.value.filter((s) => s !== entry);
    ignore.add(entry.value);
    last = null;
    const deleted = entry.upload.then(() => (entry.status === "ready" ? remove(id) : undefined));
    return { text: text.replace(secretPlaceholder(id), entry.value), deleted };
  }

  /** Waits for the uploads; throws if one failed. */
  async function ready(): Promise<void> {
    await Promise.all(sealed.value.map((s) => s.upload));
    const failed = sealed.value.find((s) => s.status === "error");
    if (failed) throw new Error(`Could not store secret ${failed.id}: ${failed.error}`);
  }

  /** After a send: deletes the secrets the sent message no longer
   *  references and starts over. */
  async function sent(message: string): Promise<void> {
    const orphans = sealed.value.filter(
      (s) => s.status === "ready" && !message.includes(secretPlaceholder(s.id)),
    );
    sealed.value = [];
    ignore.clear();
    last = null;
    await Promise.all(orphans.map((s) => remove(s.id)));
  }

  return { sealed, seal, holdsUnsealed, undo, ready, sent };
}
