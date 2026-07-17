// Stack-aware Escape handling for stacked modals.
//
// PrimeVue's Dialog binds its own document-level Escape listener per instance
// with no awareness of stacking, so when modals are layered (e.g. the add/edit
// dialog on top of the Manage Models list) a single Escape closes ALL of them.
// We disable PrimeVue's built-in close-on-escape (see Modal.vue) and route
// Escape through this shared stack instead, so only the topmost open modal
// closes. Each open Modal registers its close callback; one document listener
// (installed lazily) invokes just the last-registered one.

import { isImeComposing } from "../../utils/imeComposing";

const stack: Array<() => void> = [];

function onKeydown(event: KeyboardEvent) {
  if (event.key !== "Escape" || isImeComposing(event) || stack.length === 0) return;
  event.stopPropagation();
  // Close only the topmost modal.
  stack[stack.length - 1]();
}

let listening = false;

export function pushModalEscape(close: () => void): void {
  if (!listening) {
    document.addEventListener("keydown", onKeydown);
    listening = true;
  }
  stack.push(close);
}

export function popModalEscape(close: () => void): void {
  const i = stack.lastIndexOf(close);
  if (i !== -1) stack.splice(i, 1);
}
