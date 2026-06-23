import React from "react";
import { Button } from "@/components/ui/button";

interface VimToggleProps {
  enabled: boolean;
  onChange: (enabled: boolean) => void;
}

// Compact toggle for enabling Monaco's vim emulation. Desktop only - mobile
// users don't have a useful keyboard for vim, and the parent component
// decides whether to render this at all.
export default function VimToggle({ enabled, onChange }: VimToggleProps) {
  return (
    <Button
      type="button"
      size="sm"
      variant={enabled ? "default" : "outline"}
      onClick={() => onChange(!enabled)}
      title={enabled ? "Disable Vim mode" : "Enable Vim mode"}
      aria-pressed={enabled}
    >
      Vim
    </Button>
  );
}
