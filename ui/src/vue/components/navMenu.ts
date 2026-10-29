import type { MenuItem } from "primevue/menuitem";

/** Pass-through for every sidebar ⋯ menu (TieredMenu or ContextMenu): one
 * look, plus an item's `note` as a second line under its label, which is how
 * a disabled item says why (a tooltip never shows on touch screens). Its list
 * is a menu, as its trigger's aria-haspopup says, not PrimeVue's menubar. */
export function navMenuPt(testid: string) {
  return {
    root: { class: "drawer-actions-menu", "data-testid": testid },
    rootList: { role: "menu" },
    itemLabel: ({ context }: { context: { item: MenuItem } }) => ({
      "data-note": context.item.note,
    }),
  };
}
