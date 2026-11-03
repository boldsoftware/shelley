import type { Model } from "../types";

type NamedModel = Pick<Model, "id" | "display_name">;

export function normalizeModelName(s: string): string {
  return s.trim().toLowerCase().replace(/\./g, "-");
}

// Returns the first model whose id or display_name matches name after
// normalization, or null. Empty names never collide.
export function findDisplayNameCollision(
  name: string,
  models: ReadonlyArray<NamedModel>,
): NamedModel | null {
  const want = normalizeModelName(name);
  if (!want) return null;
  for (const m of models) {
    if (normalizeModelName(m.id ?? "") === want) return m;
    if (normalizeModelName(m.display_name ?? "") === want) return m;
  }
  return null;
}
