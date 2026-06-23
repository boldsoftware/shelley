import React from "react";
import { CheckIcon, ChevronDownIcon, PlusIcon } from "lucide-react";
import { Model } from "../types";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

interface ModelPickerProps {
  models: Model[];
  selectedModel: string;
  onSelectModel: (modelId: string) => void;
  onManageModels: () => void;
  disabled?: boolean;
}

function ModelPicker({
  models,
  selectedModel,
  onSelectModel,
  onManageModels,
  disabled = false,
}: ModelPickerProps) {
  const selectedModelObj = models.find((m) => m.id === selectedModel);
  const displayName = selectedModelObj?.display_name || selectedModel;
  const displayWithSource =
    selectedModelObj?.source && selectedModelObj.source !== "custom"
      ? `${displayName} (${selectedModelObj.source})`
      : displayName;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          disabled={disabled}
          title={displayWithSource}
          className="group min-w-0 max-w-56 justify-between gap-1.5"
        >
          <span className="min-w-0 truncate">{displayName}</span>
          <ChevronDownIcon className="size-3 shrink-0 text-muted-foreground transition-transform group-aria-expanded:rotate-180" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="start"
        className="max-h-[500px] w-(--radix-dropdown-menu-trigger-width) min-w-56"
      >
        <DropdownMenuItem onSelect={onManageModels}>
          <PlusIcon className="size-3.5" />
          <span>Add / Remove Models...</span>
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        {models.map((model) => {
          const isSelected = model.id === selectedModel;
          return (
            <DropdownMenuItem
              key={model.id}
              disabled={!model.ready}
              onSelect={() => model.ready && onSelectModel(model.id)}
              className="gap-2"
            >
              <div className="flex min-w-0 flex-1 items-center gap-2">
                <span className={cn("truncate", isSelected && "font-medium")}>
                  {model.display_name || model.id}
                </span>
                {model.source && (
                  <span className="shrink-0 text-xs text-muted-foreground">
                    {model.source}
                  </span>
                )}
              </div>
              {!model.ready && (
                <Badge variant="secondary" className="shrink-0">
                  not ready
                </Badge>
              )}
              {isSelected && (
                <CheckIcon className="size-3.5 shrink-0 text-primary" />
              )}
            </DropdownMenuItem>
          );
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

export default ModelPicker;
