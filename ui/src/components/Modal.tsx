import React from "react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

interface ModalProps {
  isOpen: boolean;
  onClose: () => void;
  title: string;
  titleRight?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
}

// Keystone modal wrapper. Built on shadcn's Dialog (Radix) but keeps the exact
// prop API the ~15 existing modals already pass, so callers don't change.
// Radix supplies the overlay, focus-trap, Escape and outside-click close, so
// the previous manual useEscapeClose / backdrop-click handling is gone.
function Modal({
  isOpen,
  onClose,
  title,
  titleRight,
  children,
  className,
}: ModalProps) {
  return (
    <Dialog
      open={isOpen}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent
        aria-describedby={undefined}
        className={cn(
          "flex max-h-[85vh] w-full flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl",
          className
        )}
      >
        <DialogHeader className="flex flex-row items-center gap-3 border-b border-border px-5 py-3 pr-12">
          <DialogTitle className="flex-1 truncate">{title}</DialogTitle>
          {titleRight && <div className="shrink-0">{titleRight}</div>}
        </DialogHeader>
        <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">{children}</div>
      </DialogContent>
    </Dialog>
  );
}

export default Modal;
