import React from "react";
import { Model } from "../types";
import ModelPicker from "./ModelPicker";

interface ModelBarProps {
  model?: string | null;
  models?: Model[];
  thinkingLevel?: string | null;
  /** When set, the model name becomes a clickable picker that calls this on selection. */
  onSwitchModel?: (modelId: string) => void;
  onManageModels?: () => void;
  switchDisabled?: boolean;
}

function ModelBar({
  model,
  models = [],
  thinkingLevel,
  onSwitchModel,
  onManageModels,
  switchDisabled,
}: ModelBarProps) {
  if (!model) {
    return null;
  }

  const modelObj = models.find((m) => m.id === model);
  const displayName = modelObj?.display_name || model;

  return (
    <div className="model-bar">
      <div className="model-bar-summary">
        <span className="model-bar-icon">🤖</span>
        <span className="model-bar-label">Model</span>
        {onSwitchModel ? (
          <ModelPicker
            models={models}
            selectedModel={model}
            onSelectModel={onSwitchModel}
            onManageModels={onManageModels || (() => {})}
            disabled={switchDisabled}
          />
        ) : (
          <span className="model-bar-name">{displayName}</span>
        )}
        {thinkingLevel && (
          <>
            <span className="model-bar-label" title="Reasoning effort">
              Reasoning
            </span>
            <span className="model-bar-name">{thinkingLevel}</span>
          </>
        )}
      </div>
    </div>
  );
}

export default ModelBar;
