import React from "react";
import { Usage } from "../types";
import { useEscapeClose } from "./useEscapeClose";

interface UsageDetailModalProps {
  usage: Usage;
  durationMs: number | null;
  onClose: () => void;
}

function UsageDetailModal({ usage, durationMs, onClose }: UsageDetailModalProps) {
  // Format duration in human-readable format
  const formatDuration = (ms: number): string => {
    if (ms < 1000) return `${ms}ms`;
    if (ms < 60000) return `${(ms / 1000).toFixed(2)}s`;
    return `${(ms / 60000).toFixed(2)}m`;
  };

  // Format timestamp for display
  const formatTimestamp = (isoString: string): string => {
    const date = new Date(isoString);
    return date.toLocaleString(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
  };

  useEscapeClose(true, onClose);

  return (
    <div className="usage-detail-overlay" onClick={onClose}>
      <div className="usage-detail-modal" onClick={(e) => e.stopPropagation()}>
        <div className="usage-detail-header">
          <h2 className="usage-detail-title">Usage Details</h2>
          <button onClick={onClose} className="usage-detail-close-button" aria-label="Close">
            ×
          </button>
        </div>
        <div className="usage-detail-grid">
          {usage.model && (
            <>
              <div className="usage-detail-label">Model:</div>
              <div className="usage-detail-value">{usage.model}</div>
            </>
          )}
          <div className="usage-detail-label">Input Tokens:</div>
          <div className="usage-detail-value">{usage.input_tokens.toLocaleString()}</div>
          {usage.cache_read_input_tokens > 0 && (
            <>
              <div className="usage-detail-label">Cache Read:</div>
              <div className="usage-detail-value">
                {usage.cache_read_input_tokens.toLocaleString()}
              </div>
            </>
          )}
          {usage.cache_creation_input_tokens > 0 && (
            <>
              <div className="usage-detail-label">Cache Write:</div>
              <div className="usage-detail-value">
                {usage.cache_creation_input_tokens.toLocaleString()}
              </div>
            </>
          )}
          <div className="usage-detail-label">Output Tokens:</div>
          <div className="usage-detail-value">{usage.output_tokens.toLocaleString()}</div>
          {usage.cost_usd > 0 && (
            <>
              <div className="usage-detail-label">Cost:</div>
              <div className="usage-detail-value">${usage.cost_usd.toFixed(4)}</div>
            </>
          )}
          {durationMs !== null && (
            <>
              <div className="usage-detail-label">Duration:</div>
              <div className="usage-detail-value">{formatDuration(durationMs)}</div>
            </>
          )}
          {usage.end_time && (
            <>
              <div className="usage-detail-label">Timestamp:</div>
              <div className="usage-detail-value">{formatTimestamp(usage.end_time)}</div>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

export default UsageDetailModal;
