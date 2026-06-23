import React, { useState } from "react";
import { CheckIcon, CopyIcon, GitForkIcon, InfoIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

interface MessageActionBarProps {
  onCopy?: () => void;
  onShowUsage?: () => void;
  onFork?: () => void;
}

function MessageActionBar({ onCopy, onShowUsage, onFork }: MessageActionBarProps) {
  const [copyFeedback, setCopyFeedback] = useState(false);

  const handleCopy = (e: React.MouseEvent) => {
    e.stopPropagation();
    if (onCopy) {
      onCopy();
      setCopyFeedback(true);
      setTimeout(() => setCopyFeedback(false), 1500);
    }
  };

  const handleShowUsage = (e: React.MouseEvent) => {
    e.stopPropagation();
    if (onShowUsage) {
      onShowUsage();
    }
  };

  const handleFork = (e: React.MouseEvent) => {
    e.stopPropagation();
    if (onFork) {
      onFork();
    }
  };

  return (
    <div className="flex items-center gap-1" data-action-bar>
      {onCopy && (
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={handleCopy}
          title="Copy"
          className={cn(
            "text-muted-foreground",
            copyFeedback && "text-emerald-600 dark:text-emerald-500"
          )}
        >
          {copyFeedback ? <CheckIcon /> : <CopyIcon />}
        </Button>
      )}
      {onFork && (
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={handleFork}
          title="Fork conversation from here"
          className="text-muted-foreground"
        >
          <GitForkIcon />
        </Button>
      )}
      {onShowUsage && (
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={handleShowUsage}
          title="Details"
          className="text-muted-foreground"
        >
          <InfoIcon />
        </Button>
      )}
    </div>
  );
}

export default MessageActionBar;
