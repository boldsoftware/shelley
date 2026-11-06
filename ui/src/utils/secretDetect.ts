// Secret detection for the composer (docs/secrets.md). Runs in the browser,
// before text leaves it. Known formats (gitleaks' rules, generated into
// generated-secret-rules.ts, and a few of our own), a conservative rule for
// long random tokens, and the manual {{secret=VALUE}} /
// {{secret:NAME=VALUE}} marks.
// False negatives beat mangled prose: the marks cover what this misses.

import { GITLEAKS_ALLOW, GITLEAKS_RULES, type GitleaksAllow } from "../generated-secret-rules";

export interface SecretFinding {
  start: number;
  end: number;
  value: string;
  kind: string;
  /** The ID a manual {{secret:NAME=VALUE}} mark asks for. */
  name?: string;
  /** A manual mark: complete as soon as it is typed. */
  manual?: boolean;
}

/** The ID part of a {{secret:ID}} placeholder. */
export const SECRET_ID = "[a-z0-9]+(?:-[a-z0-9]+)*";
const PLACEHOLDER_RE = new RegExp(`\\{\\{secret:(${SECRET_ID})\\}\\}`, "g");
const MARK_RE = new RegExp(`\\{\\{secret(?::(${SECRET_ID}))?=([\\s\\S]+?)\\}\\}`, "g");

export function secretPlaceholder(id: string): string {
  return `{{secret:${id}}}`;
}

/** Splits text into plain runs and {{secret:ID}} placeholders. */
export function splitPlaceholders(text: string): Array<{ text: string; secretId?: string }> {
  const out: Array<{ text: string; secretId?: string }> = [];
  let last = 0;
  for (const m of text.matchAll(PLACEHOLDER_RE)) {
    if (m.index > last) out.push({ text: text.slice(last, m.index) });
    out.push({ text: m[0], secretId: m[1] });
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push({ text: text.slice(last) });
  return out;
}

const hasUpper = (s: string) => /[A-Z]/.test(s);
const hasLower = (s: string) => /[a-z]/.test(s);
const hasDigit = (s: string) => /[0-9]/.test(s);
const mixed = (s: string) => hasUpper(s) && hasLower(s) && hasDigit(s);

interface Allow {
  /** The regexes test the whole match, not the secret. */
  match: boolean;
  regexes: RegExp[];
  stopwords: string[];
}

interface Rule {
  kind: string;
  label: string;
  /** See indexed. */
  re: RegExp;
  /** Lowercase; the rule runs only on text containing one. */
  keywords: string[];
  /** The capture group holding the secret; 0 means the first that matched,
   *  or the whole match. */
  secretGroup: number;
  /** The secret's minimum entropy, in bits per character. */
  entropy: number;
  allow: Allow[];
  ok?: (secret: string) => boolean;
}

function toAllow(a: GitleaksAllow): Allow {
  return {
    match: a.match ?? false,
    regexes: (a.regexes ?? []).map((r) => new RegExp(r, "u")),
    stopwords: a.stopwords ?? [],
  };
}

const GLOBAL_ALLOW = toAllow(GITLEAKS_ALLOW);

// The flags rules run with: "d" for the secret's capture group offsets.
function indexed(re: RegExp | string): RegExp {
  return new RegExp(typeof re === "string" ? re : re.source, "dgu");
}

// Formats gitleaks (generated-secret-rules.ts) misses: a private key pasted
// without its END line (taken to the end of its base64 lines, which are
// long or end in padding), and PuTTY's .ppk files.
const SHELLEY_RULES: Rule[] = [
  {
    kind: "private-key",
    label: "Private key",
    re: indexed(
      /-----BEGIN[ A-Z0-9_-]{0,100}PRIVATE KEY(?: BLOCK)?-----(?:\r?\n[A-Za-z-]+:[^\r\n]*)*(?:(?:\r?\n)+(?:[A-Za-z0-9+/]{16,}={0,2}|[A-Za-z0-9+/]*=+))+/,
    ),
    keywords: ["-----begin"],
    secretGroup: 0,
    entropy: 0,
    allow: [],
    ok: (s) => (s.replace(/^-----[^\n]*-----/, "").match(/[A-Za-z0-9+/=]/g) ?? []).length >= 64,
  },
  {
    kind: "putty-private-key",
    label: "PuTTY private key",
    re: indexed(
      /PuTTY-User-Key-File-[23]: [^\r\n]*(?:\r?\n[^\r\n]*){0,80}?\r?\nPrivate-MAC: [0-9a-fA-F]+/,
    ),
    keywords: ["putty-user-key-file"],
    secretGroup: 0,
    entropy: 0,
    allow: [],
  },
];

const RULES: Rule[] = [
  ...GITLEAKS_RULES.map((r) => ({
    kind: r.id,
    label: r.label,
    re: indexed(r.regex),
    keywords: r.keywords,
    secretGroup: r.secretGroup ?? 0,
    entropy: r.entropy ?? 0,
    allow: (r.allow ?? []).map(toAllow),
  })),
  ...SHELLEY_RULES,
];

// Every rule's keywords in one pass: a lookahead tries each position, and
// the longest keyword there implies the ones inside it (gitleaks' keyword
// filter, without 230 scans of the text).
const KEYWORDS = [...new Set(RULES.flatMap((r) => r.keywords))].sort((a, b) => b.length - a.length);
const KEYWORD_RE = new RegExp(
  `(?=(${KEYWORDS.map((k) => k.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|")}))`,
  "g",
);
const IMPLIED = new Map(KEYWORDS.map((k) => [k, KEYWORDS.filter((j) => k.includes(j))]));

/** The rule keywords in text. */
export function keywordsIn(text: string): Set<string> {
  const found = new Set<string>();
  for (const m of text.toLowerCase().matchAll(KEYWORD_RE)) {
    if (!found.has(m[1])) for (const k of IMPLIED.get(m[1])!) found.add(k);
  }
  return found;
}

function allowed(a: Allow, secret: string, match: string): boolean {
  const lower = secret.toLowerCase();
  return (
    a.regexes.some((re) => re.test(a.match ? match : secret)) ||
    a.stopwords.some((w) => lower.includes(w))
  );
}

// Where in a match its secret is.
function secretSpan(m: RegExpExecArray, group: number): [number, number] | undefined {
  const spans = m.indices!;
  if (group > 0) return spans[group];
  for (let i = 1; i < spans.length; i++) if (m[i]) return spans[i];
  return spans[0];
}

function entropy(s: string): number {
  const counts = new Map<string, number>();
  for (const c of s) counts.set(c, (counts.get(c) ?? 0) + 1);
  let h = 0;
  for (const n of counts.values()) h -= (n / s.length) * Math.log2(n / s.length);
  return h;
}

// A long random token: 32 to 512 characters of base64/base62, with upper
// and lower case and digits, high entropy, and not mostly words (a long
// CamelCase identifier), hex (hashes), or a path or URL (no slashes).
// Base64url has 2 separators in 64 characters, so a long token has a few
// dashes and underscores; a snake_case or kebab-case name has many more.
export function looksRandom(token: string): boolean {
  if (token.length < 32 || token.length > 512) return false;
  if (!/^[A-Za-z0-9+=_-]+$/.test(token) || !mixed(token)) return false;
  if (/^[0-9a-fA-F-]+$/.test(token)) return false;
  if ((token.match(/[-_]/g) ?? []).length > Math.max(3, token.length / 12)) return false;
  if ((token.match(/[0-9]/g) ?? []).length < 3) return false;
  const words = (token.match(/[A-Z]?[a-z]{4,}/g) ?? []).join("").length;
  if (words > token.length / 3) return false;
  return entropy(token) >= 4.2;
}

// A run that could be a token, bounded by whitespace, quotes, brackets or
// punctuation that can't be inside one (an email's @, a hostname's dot, a
// URL's colon); a slash anywhere means a path or URL. The lookbehind starts
// each attempt at the start of a run: without it, every word costs time
// quadratic in its length.
const TOKEN_RE = /(?<![\w+/=.:@-])[\w+/=-]{32,}/g;

function overlaps(findings: SecretFinding[], start: number, end: number): boolean {
  return findings.some((f) => start < f.end && f.start < end);
}

// Candidate findings of rules, in rule order, then random tokens.
function candidates(
  text: string,
  rules: Rule[],
  keywords: Set<string>,
  tokens: boolean,
): SecretFinding[] {
  const out: SecretFinding[] = [];
  for (const rule of rules) {
    if (rule.keywords.length > 0 && !rule.keywords.some((k) => keywords.has(k))) continue;
    for (const m of text.matchAll(rule.re)) {
      const span = secretSpan(m, rule.secretGroup);
      if (!span) continue;
      const value = text.slice(span[0], span[1]);
      if (value.length === 0 || entropy(value) < rule.entropy || (rule.ok && !rule.ok(value)))
        continue;
      if ([GLOBAL_ALLOW, ...rule.allow].some((a) => allowed(a, value, m[0]))) continue;
      out.push({ start: span[0], end: span[1], value, kind: rule.kind });
    }
  }
  if (!tokens) return out;
  for (const m of text.matchAll(TOKEN_RE)) {
    if (text[m.index + m[0].length] === "@") continue;
    if (!looksRandom(m[0])) continue;
    out.push({ start: m.index, end: m.index + m[0].length, value: m[0], kind: "token" });
  }
  return out;
}

// Detection runs on every keystroke, so it works paragraph by paragraph and
// remembers the candidates of recent paragraphs: an edit costs a scan of its
// paragraph, not of the whole message. Only private keys span paragraphs
// (PEM and PGP blocks hold blank lines); everything else is found within one.
const SPANNING = RULES.filter((r) => r.keywords.includes("-----begin"));
const SPANNING_KEYWORDS = new Set(SPANNING.flatMap((r) => r.keywords));
const SPANNING_RE = new RegExp(
  [...SPANNING_KEYWORDS].map((k) => k.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|"),
  "i",
);
const LOCAL = RULES.filter((r) => !SPANNING.includes(r));
const BLANK_LINE = /\n[ \t\r]*\n/g;
const PARAGRAPH_CHARS_KEPT = 1 << 20;
const paragraphs = new Map<string, SecretFinding[]>();
let paragraphChars = 0;

function paragraphCandidates(text: string): SecretFinding[] {
  const kept = paragraphs.get(text);
  if (kept) {
    paragraphs.delete(text); // Most recently used goes last.
    paragraphs.set(text, kept);
    return kept;
  }
  const found = candidates(text, LOCAL, keywordsIn(text), true);
  paragraphs.set(text, found);
  paragraphChars += text.length;
  for (const old of paragraphs.keys()) {
    if (paragraphChars <= PARAGRAPH_CHARS_KEPT) break;
    paragraphs.delete(old);
    paragraphChars -= old.length;
  }
  return found;
}

/** Finds secrets in text, in order, without overlaps. Placeholders and
 *  detected values in ignore are left alone. */
export function detectSecrets(
  text: string,
  ignore: ReadonlySet<string> = new Set(),
): SecretFinding[] {
  const found: SecretFinding[] = [];
  const add = (f: SecretFinding) => {
    if (ignore.has(f.value) || overlaps(found, f.start, f.end)) return;
    found.push(f);
  };
  for (const m of text.matchAll(PLACEHOLDER_RE)) {
    found.push({ start: m.index, end: m.index + m[0].length, value: "", kind: "placeholder" });
  }
  // Marks are explicit: they win over everything, even ignored values.
  for (const m of text.matchAll(MARK_RE)) {
    const start = m.index;
    const end = start + m[0].length;
    if (overlaps(found, start, end)) continue;
    found.push({ start, end, value: m[2], kind: "secret", name: m[1], manual: true });
  }
  if (SPANNING_RE.test(text)) candidates(text, SPANNING, SPANNING_KEYWORDS, false).forEach(add);
  let start = 0;
  for (const end of [...[...text.matchAll(BLANK_LINE)].map((m) => m.index), text.length]) {
    for (const f of paragraphCandidates(text.slice(start, end))) {
      add({ ...f, start: f.start + start, end: f.end + start });
    }
    start = end;
  }
  return found.filter((f) => f.kind !== "placeholder").sort((a, b) => a.start - b.start);
}

/** Replaces each finding's span with its ID's placeholder. */
export function sealFindings(
  text: string,
  findings: Array<SecretFinding & { id: string }>,
): string {
  let out = text;
  for (const f of [...findings].sort((a, b) => b.start - a.start)) {
    out = out.slice(0, f.start) + secretPlaceholder(f.id) + out.slice(f.end);
  }
  return out;
}

/** A new secret's ID: its kind (or the name a mark asked for) and four
 *  random characters, e.g. openai-key-k3f9. */
export function newSecretId(kind: string, random: () => number = Math.random): string {
  const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789";
  let suffix = "";
  for (let i = 0; i < 4; i++) suffix += alphabet[Math.floor(random() * alphabet.length)];
  return `${kind}-${suffix}`;
}

const KIND_LABELS = new Map<string, string>([
  ...RULES.map((r): [string, string] => [r.kind, r.label]),
  ["token", "Random-looking token"],
  ["secret", "Marked secret"],
]);

export function secretKindLabel(kind: string): string {
  return KIND_LABELS.get(kind) ?? kind;
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

/** What a {{secret:ID}} pill shows and links to. */
export function secretPill(id: string): { href: string; title: string; filler: string } {
  return {
    href: `/secrets#${id}`,
    title: `Secret ${id}: stored in Shelley, never sent to the model. Open the secrets page.`,
    filler: secretFiller(id),
  };
}
