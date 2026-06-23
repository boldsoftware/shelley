import React, { useState } from "react";
import { formatTime, formatAbsolute, formatDay, formatRelative } from "../utils/messageTime";

interface MessageTimestampProps {
  createdAt: string;
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
