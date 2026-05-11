import React, { useState } from "react";

interface MessageTimestampProps {
  createdAt: string;
}

function formatTime(d: Date): string {
  return d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
}

function formatAbsolute(d: Date): string {
  return d.toLocaleString([], {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
    second: "2-digit",
  });
}

function formatDay(d: Date, now: Date): string {
  const sameYear = d.getFullYear() === now.getFullYear();
  return d.toLocaleDateString([], {
    weekday: "short",
    month: "short",
    day: "numeric",
    year: sameYear ? undefined : "numeric",
  });
}

function formatRelative(deltaMs: number): string {
  // Guard against clock skew / future timestamps.
  if (deltaMs < 0) deltaMs = 0;
  const sec = Math.round(deltaMs / 1000);
  if (sec < 5) return "just now";
  if (sec < 60) return `${sec}s ago`;
  const min = Math.round(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.round(min / 60);
  if (hr < 24) return `${hr}h ago`;
  const day = Math.round(hr / 24);
  if (day < 30) return `${day}d ago`;
  const mo = Math.round(day / 30);
  if (mo < 12) return `${mo}mo ago`;
  const yr = Math.round(mo / 12);
  return `${yr}y ago`;
}

/**
 * Unobtrusive timestamp separator rendered between messages when the wall-clock
 * minute has advanced since the previous shown timestamp. The first timestamp
 * in a day also gets a horizontal day separator above it.
 *
 * The displayed label is a fixed clock time, so it never changes after mount.
 * The tooltip carries the relative "x minutes ago" string and is computed
 * lazily on hover, so we don't need to keep this component re-rendering.
 */
function MessageTimestamp({ createdAt }: MessageTimestampProps) {
  const date = new Date(createdAt);
  const [tooltip, setTooltip] = useState<string | null>(null);

  if (isNaN(date.getTime())) return null;

  const refreshTooltip = () => {
    setTooltip(`${formatAbsolute(date)} (${formatRelative(Date.now() - date.getTime())})`);
  };

  return (
    <div className="message-timestamp-row" data-testid="message-timestamp">
      <time
        className="message-timestamp"
        dateTime={date.toISOString()}
        title={tooltip ?? formatAbsolute(date)}
        onMouseEnter={refreshTooltip}
        onFocus={refreshTooltip}
      >
        {formatTime(date)}
      </time>
    </div>
  );
}

export { formatDay, formatRelative };
export default MessageTimestamp;
