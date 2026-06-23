import React from "react";
import { CheckIcon, ChevronDownIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";

export type ThinkingLevel = "off" | "minimal" | "low" | "medium" | "high" | "xhigh";

export const DEFAULT_THINKING_LEVEL: ThinkingLevel = "medium";

export const THINKING_LEVELS: { value: ThinkingLevel; label: string }[] = [
  { value: "off", label: "off" },
  { value: "minimal", label: "minimal" },
  { value: "low", label: "low" },
  { value: "medium", label: "medium" },
  { value: "high", label: "high" },
  { value: "xhigh", label: "xhigh" },
];

interface ThinkingLevelPickerProps {
  value: ThinkingLevel;
  onChange: (level: ThinkingLevel) => void;
  disabled?: boolean;
}

function ThinkingLevelPicker({ value, onChange, disabled = false }: ThinkingLevelPickerProps) {
  const current =
    THINKING_LEVELS.find((l) => l.value === value) ||
    THINKING_LEVELS.find((l) => l.value === DEFAULT_THINKING_LEVEL)!;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          disabled={disabled}
          type="button"
          title={`Reasoning effort: ${current.label}`}
          className="group/trigger gap-1.5"
        >
          <span>{current.label}</span>
          <ChevronDownIcon className="size-3 text-muted-foreground transition-transform group-aria-expanded/trigger:rotate-180" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-[8rem]">
        {THINKING_LEVELS.map((level) => (
          <DropdownMenuItem
            key={level.value}
            onSelect={() => onChange(level.value)}
            className={cn(level.value === value && "font-medium text-foreground")}
          >
            <span className="flex-1">{level.label}</span>
            {level.value === value && <CheckIcon className="size-3.5 text-primary" />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

export default ThinkingLevelPicker;
