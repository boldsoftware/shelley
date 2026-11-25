// Humanized model names. Model ids like "claude-opus-4.8" or
// "deepseek-v4-pro-fireworks" are what the API speaks, but they read as
// machine noise in the picker. prettyModelName() renders a display label for
// ids whose family it recognizes ("Claude Opus 4.8", "DeepSeek V4 Pro") and
// passes everything else through verbatim — an unknown id is never mangled,
// and OpenAI's deliberately lowercase brands (o3, codex-mini-latest) stay
// untouched because their families are intentionally absent from the map.
//
// prettyModelLabels() applies this across a model list with a collision
// guard: if two ids would prettify to the same label (e.g. a "-fireworks"
// hosting suffix was the only difference), those entries keep their raw ids.

// Families that opt an id into prettification, keyed by the alphabetic prefix
// of the first token. Values are the branded capitalization.
const FAMILIES: Record<string, string> = {
  claude: "Claude",
  gpt: "GPT",
  glm: "GLM",
  deepseek: "DeepSeek",
  grok: "Grok",
  kimi: "Kimi",
  minimax: "MiniMax",
  qwen: "Qwen",
  gemini: "Gemini",
  llama: "Llama",
  mistral: "Mistral",
};

// Known word tokens and their display forms.
const WORDS: Record<string, string> = {
  opus: "Opus",
  sonnet: "Sonnet",
  haiku: "Haiku",
  fable: "Fable",
  oss: "OSS",
  pro: "Pro",
  flash: "Flash",
  plus: "Plus",
  mini: "Mini",
  nano: "Nano",
  code: "Code",
  coder: "Coder",
  codex: "Codex",
  chat: "Chat",
  instruct: "Instruct",
  thinking: "Thinking",
  preview: "Preview",
  turbo: "Turbo",
  astra: "Astra",
  sol: "Sol",
  terra: "Terra",
  luna: "Luna",
  argon: "Argon",
  carbon: "Carbon",
  hydrogen: "Hydrogen",
  neon: "Neon",
};

// Hosting-provider suffixes that carry no model identity; dropped when they
// are the trailing token ("glm-5.2-fireworks" -> "GLM 5.2").
const HOSTING_SUFFIXES = new Set(["fireworks"]);

const PURE_INT = /^\d+$/;
// Numeric-ish tokens kept verbatim: versions ("4.8"), sizes ("20b"), and
// suffixed versions ("4o").
const NUMERICISH = /^\d+(\.\d+)*[a-z]?$/;

// "5p1" -> "5.1" (gateway spelling of a dotted version).
function dotify(s: string): string {
  return s.replace(/^(\d+)p(\d+)$/, "$1.$2");
}

// Prettify one token, or return null when the token is unrecognized (which
// vetoes prettification of the whole id).
function prettyToken(tok: string): string | null {
  if (WORDS[tok]) return WORDS[tok];
  if (NUMERICISH.test(tok)) {
    // Uppercase a size suffix: "20b" -> "20B". Version letters ("4o") stay.
    return /^\d+b$/.test(tok) ? tok.toUpperCase() : tok;
  }
  if (/^\d+p\d+$/.test(tok)) return dotify(tok);
  // Single letter + digits: "v4" -> "V4", "k2.7" -> "K2.7", "m2p7" -> "M2.7".
  const m = tok.match(/^([a-z])(\d.*)$/);
  if (m && NUMERICISH.test(dotify(m[2]))) return m[1].toUpperCase() + dotify(m[2]);
  return null;
}

export function prettyModelName(id: string): string {
  const tokens = id.split("-");
  const first = tokens[0];
  const alpha = (first.match(/^[a-z]+/) || [""])[0];
  const family = FAMILIES[alpha];
  if (!family) return id;

  // First token: branded family name plus any attached version ("qwen3.7").
  const firstRest = first.slice(alpha.length);
  if (firstRest && !NUMERICISH.test(dotify(firstRest))) return id;
  const out: string[] = [family + dotify(firstRest)];

  let rest = tokens.slice(1);
  if (rest.length && HOSTING_SUFFIXES.has(rest[rest.length - 1])) rest = rest.slice(0, -1);

  // Merge consecutive pure-integer tokens into a dotted version:
  // ["4","5"] -> "4.5" (ids like claude-opus-4-5).
  const merged: string[] = [];
  for (const tok of rest) {
    if (PURE_INT.test(tok) && merged.length && PURE_INT.test(merged[merged.length - 1])) {
      merged[merged.length - 1] += "." + tok;
    } else {
      merged.push(tok);
    }
  }

  for (const tok of merged) {
    const pretty = prettyToken(tok);
    if (pretty === null) return id; // unknown token: leave the whole id verbatim
    out.push(pretty);
  }

  // Brand-style joins: "GPT" + version -> "GPT-5.6"; "GPT" + "OSS" -> "GPT-OSS".
  if (out[0] === "GPT" && out.length > 1 && (/^\d/.test(out[1]) || out[1] === "OSS")) {
    out.splice(0, 2, `GPT-${out[1]}`);
  }
  return out.join(" ");
}

export interface NamedModel {
  id: string;
  display_name?: string;
}

// Display label for one model: an explicit, distinct display_name wins;
// otherwise the prettified id.
function labelFor(m: NamedModel): string {
  if (m.display_name && m.display_name !== m.id) return m.display_name;
  return prettyModelName(m.id);
}

// Labels for a whole list, with the collision guard described above.
export function prettyModelLabels(models: NamedModel[]): Map<string, string> {
  const labels = new Map<string, string>();
  const counts = new Map<string, number>();
  for (const m of models) {
    const l = labelFor(m);
    labels.set(m.id, l);
    counts.set(l, (counts.get(l) || 0) + 1);
  }
  for (const m of models) {
    const l = labels.get(m.id)!;
    // Only derived labels fall back; explicit display_name collisions are the
    // server's/user's own choice.
    if (counts.get(l)! > 1 && !(m.display_name && m.display_name !== m.id)) {
      labels.set(m.id, m.id);
    }
  }
  return labels;
}
