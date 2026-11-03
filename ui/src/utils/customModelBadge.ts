import type { Model } from "../types";

// Host (with port) of an endpoint URL, or the raw string when it does not parse.
export function endpointHost(url: string | undefined): string {
  if (!url) return "";
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

// A DB-backed custom model has source "custom" and no source_id; provider
// connection models carry a source_id and are not custom. The label is the
// server's source string, intentionally untranslated.
export function customModelBadge(
  model: Pick<Model, "source" | "source_id" | "base_url"> | undefined | null,
): { label: string; title: string } | null {
  if (!model || model.source !== "custom" || model.source_id) return null;
  return { label: "custom", title: endpointHost(model.base_url) };
}
