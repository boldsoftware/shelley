// Secrets in messages (docs/secrets.md): the chat API replaces each secret
// it takes out of a message with a marker, which the conversation shows as
// a pill. Messages from before hold {{secret:ID}} placeholders.

const SECRET_ID = "[a-z0-9]+(?:-[a-z0-9]+)*";
const MARKER_RE = new RegExp(
  `\\[Secret extracted by Shelley; shelley secret (${SECRET_ID}) prints the path of a file holding it\\]|\\{\\{secret:(${SECRET_ID})\\}\\}`,
  "g",
);

/** Whether text may hold a marker: a quick test before splitting. */
export function mayHoldSecretMarker(text: string): boolean {
  return text.includes("[Secret extracted by Shelley") || text.includes("{{secret:");
}

/** Splits text into plain runs and secret markers. */
export function splitSecretMarkers(text: string): Array<{ text: string; secretId?: string }> {
  if (!mayHoldSecretMarker(text)) return text ? [{ text }] : [];
  const out: Array<{ text: string; secretId?: string }> = [];
  let last = 0;
  for (const m of text.matchAll(MARKER_RE)) {
    if (m.index > last) out.push({ text: text.slice(last, m.index) });
    out.push({ text: m[0], secretId: m[1] ?? m[2] });
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push({ text: text.slice(last) });
  return out;
}

/** Meaningless filler for a pill, derived from the ID, never the value. */
export function secretFiller(id: string, length = 14): string {
  let h = 2166136261;
  for (const c of id) h = Math.imul(h ^ c.charCodeAt(0), 16777619);
  const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789";
  let out = "";
  for (let i = 0; i < length; i++) {
    h = Math.imul(h ^ (h >>> 15), 2246822507) >>> 0;
    out += alphabet[h % alphabet.length];
  }
  return out;
}
