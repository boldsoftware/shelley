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
    <div className="model-bar">
      <div className="model-bar-summary">
        <span className="model-bar-icon">🤖</span>
        <span className="model-bar-label">Model</span>
        <span className="model-bar-name">{displayName}</span>
        {thinkingLevel && (
          <>
            <span className="model-bar-icon" title="Reasoning effort">
              🧠
            </span>
            <span className="model-bar-name">{thinkingLevel}</span>
          </>
        )}
      </div>
    </div>
  );
}

export default ModelBar;
