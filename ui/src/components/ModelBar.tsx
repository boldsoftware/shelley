import React from "react";
import { Model } from "../types";

interface ModelBarProps {
  model?: string | null;
  models?: Model[];
  thinkingLevel?: string | null;
}

function ModelBar({ model, models = [], thinkingLevel }: ModelBarProps) {
  if (!model) {
    return null;
  }

  // Find the model object to get display name
  const modelObj = models.find((m) => m.id === model);
  const displayName = modelObj?.display_name || model;

  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 px-1 py-1 text-xs text-muted-foreground">
      <span aria-hidden>🤖</span>
      <span className="font-medium">Model</span>
      <span className="text-foreground">{displayName}</span>
      {thinkingLevel && (
        <>
          <span className="font-medium" title="Reasoning effort">
            Reasoning
          </span>
          <span className="text-foreground">{thinkingLevel}</span>
        </>
      )}
    </div>
  );
}

export default ModelBar;
