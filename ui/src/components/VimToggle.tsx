import React from "react";

interface VimToggleProps {
  enabled: boolean;
  onChange: (enabled: boolean) => void;
}

// Compact toggle for enabling Monaco's vim emulation. Desktop only - mobile
// users don't have a useful keyboard for vim, and the parent component
// decides whether to render this at all.
export default function VimToggle({ enabled, onChange }: VimToggleProps) {
  return (
    <button
      type="button"
      className={`vim-toggle ${enabled ? "active" : ""}`}
      onClick={() => onChange(!enabled)}
      title={enabled ? "Disable Vim mode" : "Enable Vim mode"}
      aria-pressed={enabled}
    >
      Vim
    </button>
  );
}
