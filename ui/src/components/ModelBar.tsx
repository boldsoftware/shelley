import React from "react";
import { Model } from "../types";

interface ModelBarProps {
  model?: string | null;
  models?: Model[];
}

function ModelBar({ model, models = [] }: ModelBarProps) {
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
      </div>
    </div>
  );
}

export default ModelBar;
