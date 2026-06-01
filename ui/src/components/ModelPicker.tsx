import React, { useState, useRef, useEffect, useCallback } from "react";
import { Model } from "../types";

const dropdownViewportGutterPx = 8;
const dropdownMaxHeightPx = 500;

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
  const [isOpen, setIsOpen] = useState(false);
  const [openUpward, setOpenUpward] = useState(false);
  const [dropdownMaxHeight, setDropdownMaxHeight] = useState(dropdownMaxHeightPx);
  const containerRef = useRef<HTMLDivElement>(null);
  const dropdownRef = useRef<HTMLDivElement>(null);

  // Close dropdown when clicking outside
  useEffect(() => {
    function handleClickOutside(event: MouseEvent) {
      if (containerRef.current && !containerRef.current.contains(event.target as Node)) {
        setIsOpen(false);
      }
    }

    if (isOpen) {
      document.addEventListener("mousedown", handleClickOutside);
      return () => document.removeEventListener("mousedown", handleClickOutside);
    }
  }, [isOpen]);

  // Close on escape
  useEffect(() => {
    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") {
        setIsOpen(false);
      }
    }

    if (isOpen) {
      document.addEventListener("keydown", handleKeyDown);
      return () => document.removeEventListener("keydown", handleKeyDown);
    }
  }, [isOpen]);

  const updateDropdownPlacement = useCallback(() => {
    if (!containerRef.current) {
      return;
    }

    const rect = containerRef.current.getBoundingClientRect();
    const viewportHeight = window.visualViewport?.height ?? window.innerHeight;
    const spaceAbove = Math.max(0, rect.top - dropdownViewportGutterPx);
    const spaceBelow = Math.max(0, viewportHeight - rect.bottom - dropdownViewportGutterPx);
    const shouldOpenUpward = spaceAbove > spaceBelow;
    const availableSpace = shouldOpenUpward ? spaceAbove : spaceBelow;

    setOpenUpward(shouldOpenUpward);
    setDropdownMaxHeight(Math.min(dropdownMaxHeightPx, availableSpace));
  }, []);

  // Open toward the larger side and cap height to the visible viewport.
  useEffect(() => {
    if (!isOpen) {
      return;
    }

    updateDropdownPlacement();
    window.addEventListener("resize", updateDropdownPlacement);
    window.visualViewport?.addEventListener("resize", updateDropdownPlacement);
    window.visualViewport?.addEventListener("scroll", updateDropdownPlacement);

    return () => {
      window.removeEventListener("resize", updateDropdownPlacement);
      window.visualViewport?.removeEventListener("resize", updateDropdownPlacement);
      window.visualViewport?.removeEventListener("scroll", updateDropdownPlacement);
    };
  }, [isOpen, models.length, updateDropdownPlacement]);

  const selectedModelObj = models.find((m) => m.id === selectedModel);
  const displayName = selectedModelObj?.display_name || selectedModel;
  const displayWithSource =
    selectedModelObj?.source && selectedModelObj.source !== "custom"
      ? `${displayName} (${selectedModelObj.source})`
      : displayName;

  const handleSelect = (modelId: string) => {
    onSelectModel(modelId);
    setIsOpen(false);
  };

  const handleManageModels = () => {
    setIsOpen(false);
    onManageModels();
  };

  const manageOption = (
    <button
      className="model-picker-option model-picker-manage"
      onClick={handleManageModels}
      type="button"
    >
      <svg
        width="14"
        height="14"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
      >
        <path d="M12 4v16m-8-8h16" />
      </svg>
      <span>Add / Remove Models...</span>
    </button>
  );

  return (
    <div className="model-picker" ref={containerRef}>
      <button
        className="model-picker-trigger"
        onClick={() => !disabled && setIsOpen(!isOpen)}
        disabled={disabled}
        type="button"
        title={displayWithSource}
      >
        <span className="model-picker-value">{displayName}</span>
        <svg
          className={`model-picker-chevron ${isOpen ? "open" : ""}`}
          width="12"
          height="12"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
        >
          <path d="M6 9l6 6 6-6" />
        </svg>
      </button>

      {isOpen && (
        <div
          className={`model-picker-dropdown ${openUpward ? "open-upward" : ""}`}
          ref={dropdownRef}
          style={{ maxHeight: `${dropdownMaxHeight}px` }}
        >
          <div className="model-picker-action">
            {manageOption}
            <div className="model-picker-divider" />
          </div>
          <div className="model-picker-options">
            {models.map((model) => (
              <button
                key={model.id}
                className={`model-picker-option ${model.id === selectedModel ? "selected" : ""} ${!model.ready ? "disabled" : ""}`}
                onClick={() => model.ready && handleSelect(model.id)}
                disabled={!model.ready}
                type="button"
              >
                <div className="model-picker-option-content">
                  <span className="model-picker-option-name">{model.display_name || model.id}</span>
                  {model.source && (
                    <span className="model-picker-option-source">{model.source}</span>
                  )}
                </div>
                {!model.ready && <span className="model-picker-option-badge">not ready</span>}
                {model.id === selectedModel && (
                  <svg
                    className="model-picker-option-check"
                    width="14"
                    height="14"
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="2"
                  >
                    <path d="M20 6L9 17l-5-5" />
                  </svg>
                )}
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

export default ModelPicker;
