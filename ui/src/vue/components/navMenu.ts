/** Pass-through for every sidebar ⋯ menu (TieredMenu or ContextMenu): one look.
 * Its list is a menu, as its trigger's aria-haspopup says, not PrimeVue's
 * menubar. */
export function navMenuPt(testid: string) {
  return {
    root: { class: "drawer-actions-menu", "data-testid": testid },
    rootList: { role: "menu" },
  };
}
