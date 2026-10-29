// Groups output_iframe calls in a conversation by file path (plus bundled files)
// so a re-shown file reads as a new version of one artifact instead of a fresh
// embed. Only successful showings count as versions.
import { computed, inject, provide, type ComputedRef, type InjectionKey, type Ref } from "vue";
import type { LLMContent, Message } from "../../types";

export interface IframeVersion {
  path: string;
  version: number;
  total: number;
  latestId: string;
}

interface Parsed {
  len: number;
  uses: { id: string; path: string; key: string }[];
  results: { id: string; ok: boolean }[];
}

export type IframeParseCache = Map<string, Parsed>;

const TOOL_USE = 5;
const TOOL_RESULT = 6;

function parse(raw: string): Parsed {
  const out: Parsed = { len: raw.length, uses: [], results: [] };
  const hasUse = raw.includes("output_iframe");
  if (!hasUse && !raw.includes(`"Type":${TOOL_RESULT}`)) return out;
  let content: unknown;
  try {
    content = JSON.parse(raw)?.Content;
  } catch {
    return out;
  }
  if (!Array.isArray(content)) return out;
  for (const c of content as (LLMContent | null)[]) {
    if (!c || typeof c !== "object") continue;
    if (c.Type === TOOL_RESULT && c.ToolUseID) {
      out.results.push({ id: c.ToolUseID, ok: !c.ToolError });
    } else if (hasUse && c.Type === TOOL_USE && c.ToolName === "output_iframe" && c.ID) {
      const input = c.ToolInput as { path?: unknown; files?: unknown } | null;
      if (typeof input?.path !== "string") continue;
      const norm = (p: unknown) => (typeof p === "string" ? p.replace(/^\.\//, "") : p);
      const path = norm(input.path) as string;
      // A shared renderer shows different content per bundled file set.
      const files =
        input.files && typeof input.files === "object"
          ? Object.entries(input.files as Record<string, unknown>)
              .map(([k, v]) => [k, norm(v)])
              .sort()
          : [];
      out.uses.push({
        id: c.ID,
        // With a shared renderer the data files are what the user knows.
        path: files.length ? files.map(([, v]) => v).join(", ") : path,
        key: path + (files.length ? JSON.stringify(files) : ""),
      });
    }
  }
  return out;
}

/** Pure grouping step. `cache` is replaced with entries for `messages` only;
 *  `prev` lets unchanged entries keep their identity. */
export function groupIframeVersions(
  messages: Message[],
  cache: IframeParseCache,
  prev?: Map<string, IframeVersion>,
): { versions: Map<string, IframeVersion>; cache: IframeParseCache } {
  const next: IframeParseCache = new Map();
  const uses: Parsed["uses"] = [];
  const ok = new Set<string>();
  for (const m of messages) {
    const raw = m.llm_data;
    if (typeof raw !== "string" || !raw) continue;
    let p = cache.get(m.message_id);
    if (!p || p.len !== raw.length) p = parse(raw);
    next.set(m.message_id, p);
    uses.push(...p.uses);
    for (const r of p.results) if (r.ok) ok.add(r.id);
  }
  const byKey = new Map<string, { path: string; ids: string[] }>();
  const seen = new Set<string>();
  for (const u of uses) {
    // Compaction can carry a call forward under the same id; count it once.
    if (seen.has(u.id) || !ok.has(u.id)) continue;
    seen.add(u.id);
    const g = byKey.get(u.key) ?? { path: u.path, ids: [] };
    g.ids.push(u.id);
    byKey.set(u.key, g);
  }
  const versions = new Map<string, IframeVersion>();
  for (const { path, ids } of byKey.values()) {
    const latestId = ids[ids.length - 1];
    ids.forEach((id, i) => {
      const old = prev?.get(id);
      const v = { path, version: i + 1, total: ids.length, latestId };
      const same =
        old &&
        old.path === v.path &&
        old.version === v.version &&
        old.total === v.total &&
        old.latestId === v.latestId;
      versions.set(id, same ? old : v);
    });
  }
  return { versions, cache: next };
}

const Key: InjectionKey<ComputedRef<Map<string, IframeVersion>>> = Symbol("iframe-versions");

export function provideIframeVersions(messages: Ref<Message[]>): void {
  let cache: IframeParseCache = new Map();
  provide(
    Key,
    computed((prev) => {
      const out = groupIframeVersions(messages.value, cache, prev);
      cache = out.cache;
      return out.versions;
    }),
  );
}

export function useIframeVersion(
  toolUseId: () => string | undefined,
): ComputedRef<IframeVersion | undefined> {
  const versions = inject(Key, undefined);
  return computed(() => {
    const id = toolUseId();
    return id ? versions?.value.get(id) : undefined;
  });
}
