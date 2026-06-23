import React from "react";
import { Message } from "../types";
import Modal from "./Modal";

interface MessageInfoModalProps {
  message: Message;
  onClose: () => void;
}

// MessageInfoModal shows basic metadata for a message that has no token-usage
// data (e.g. user messages). It mirrors UsageDetailModal so the info action is
// available symmetrically across message types.
function MessageInfoModal({ message, onClose }: MessageInfoModalProps) {
  const formatTimestamp = (isoString: string): string => {
    const date = new Date(isoString);
    if (Number.isNaN(date.getTime())) return isoString;
    return date.toLocaleString(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
  };

  return (
    <Modal isOpen onClose={onClose} title="Message Details" className="sm:max-w-md">
      <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
        <dt className="font-medium text-muted-foreground">Type</dt>
        <dd className="font-mono text-foreground">{message.type}</dd>
        {message.created_at && (
          <>
            <dt className="font-medium text-muted-foreground">Timestamp</dt>
            <dd className="text-foreground">{formatTimestamp(message.created_at)}</dd>
          </>
        )}
      </dl>
    </Modal>
  );
}

export default MessageInfoModal;
