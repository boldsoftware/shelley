// Design tokens for the status-bar PrimeVue Select (ModelPicker.vue). Rather
// than override PrimeVue's internal .p-select-* classes in styles.css, we feed
// our theme's CSS variables straight into PrimeVue's own token system via the
// :dt prop. PrimeVue then renders itself (trigger, label, overlay, options,
// hover/selected states) using our colors, so almost no bespoke CSS is needed.
// Paired with size="small", which drives the compact status-bar
// font-size/padding from the sm.* tokens below.
export const statusPickerDt = {
  background: "var(--bg-tertiary)",
  color: "var(--text-primary)",
  borderColor: "var(--border)",
  hoverBorderColor: "var(--blue-text)",
  focusBorderColor: "var(--blue-text)",
  borderRadius: "0.25rem",
  shadow: "none",
  disabledBackground: "var(--bg-tertiary)",
  disabledColor: "var(--text-secondary)",
  dropdown: { color: "var(--text-primary)" },
  // size="small" reads these for the compact 0.75rem status-bar look.
  sm: { fontSize: "0.75rem", paddingX: "0.5rem", paddingY: "0.25rem" },
  overlay: {
    background: "var(--bg-secondary)",
    borderColor: "var(--border)",
    borderRadius: "0.375rem",
    color: "var(--text-primary)",
    shadow: "0 4px 12px rgba(0, 0, 0, 0.3)",
  },
  list: { padding: "0.25rem 0", gap: "0" },
  option: {
    color: "var(--text-primary)",
    // ModelPicker's option wrapper pads itself, to take the whole option.
    padding: "0",
    borderRadius: "0",
    focusBackground: "var(--bg-tertiary)",
    focusColor: "var(--text-primary)",
    selectedBackground: "var(--bg-tertiary)",
    selectedColor: "var(--text-primary)",
    selectedFocusBackground: "var(--bg-tertiary)",
    selectedFocusColor: "var(--text-primary)",
  },
};
