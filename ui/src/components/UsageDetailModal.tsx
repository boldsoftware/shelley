import React from "react";
import { Usage } from "../types";
import Modal from "./Modal";

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

  const rows: { label: string; value: React.ReactNode }[] = [];
  if (usage.model) rows.push({ label: "Model", value: usage.model });
  rows.push({ label: "Input Tokens", value: usage.input_tokens.toLocaleString() });
  if (usage.cache_read_input_tokens > 0)
    rows.push({ label: "Cache Read", value: usage.cache_read_input_tokens.toLocaleString() });
  if (usage.cache_creation_input_tokens > 0)
    rows.push({ label: "Cache Write", value: usage.cache_creation_input_tokens.toLocaleString() });
  rows.push({ label: "Output Tokens", value: usage.output_tokens.toLocaleString() });
  if (usage.cost_usd > 0) rows.push({ label: "Cost", value: `$${usage.cost_usd.toFixed(4)}` });
  if (durationMs !== null) rows.push({ label: "Duration", value: formatDuration(durationMs) });
  if (usage.end_time) rows.push({ label: "Timestamp", value: formatTimestamp(usage.end_time) });

  return (
    <Modal isOpen onClose={onClose} title="Usage Details" className="sm:max-w-md">
      <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2.5 text-sm">
        {rows.map((row) => (
          <React.Fragment key={row.label}>
            <dt className="text-muted-foreground">{row.label}</dt>
            <dd className="text-right font-mono break-all text-foreground">{row.value}</dd>
          </React.Fragment>
        ))}
      </dl>
    </Modal>
  );
}

export default UsageDetailModal;
