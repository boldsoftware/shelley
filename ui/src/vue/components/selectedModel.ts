// Shared "which model do we start with" logic. Both the composer and the
// customized-build rebase action need a *ready* model id, seeded from the
// user's last pick, and both must agree on where that pick is stored.
import type { Model } from "../../types";

export const SELECTED_MODEL_KEY = "shelley_selected_model";

export function storedSelectedModel(): string | undefined {
  return localStorage.getItem(SELECTED_MODEL_KEY) || undefined;
}

// pickReadyModel resolves the model to start with: the caller's preference if
// it is ready, else the server's default if that is ready, else any ready
// model. "" when the catalog serves nothing ready — deliberately no invented
// fallback id, which would turn "no models configured" into a confusing
// "Unsupported model" from the server.
export function pickReadyModel(models: Pick<Model, "id" | "ready">[], preferred?: string): string {
  const ready = (id?: string) => !!id && models.some((m) => m.id === id && m.ready);
  if (ready(preferred)) return preferred as string;
  const serverDefault = window.__SHELLEY_INIT__?.default_model;
  if (ready(serverDefault)) return serverDefault as string;
  return models.find((m) => m.ready)?.id || "";
}
