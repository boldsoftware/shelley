import React, { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { MessageSquareIcon } from "lucide-react";
import { Button } from "@/components/ui/button";

interface Props {
  onComment: (messageId: string, snippet: string) => void;
}

interface ToolbarState {
  messageId: string;
  snippet: string;
  rect: DOMRect;
}

// Walk up from a node to find the nearest commentable message container.
function findCommentableAncestor(node: Node | null): HTMLElement | null {
  let el: Node | null = node;
  while (el) {
    if (el.nodeType === Node.ELEMENT_NODE) {
      const he = el as HTMLElement;
      if (he.dataset && he.dataset.commentable === "true" && he.dataset.messageId) {
        return he;
      }
    }
    el = (el as Node).parentNode;
  }
  return null;
}

// Build toolbar state from the current selection, or null if it isn't
// a non-collapsed selection entirely within one commentable message.
function computeState(): ToolbarState | null {
  const sel = window.getSelection();
  if (!sel || sel.isCollapsed || sel.rangeCount === 0) return null;
  const text = sel.toString();
  if (!text.trim()) return null;
  const range = sel.getRangeAt(0);
  const startEl = findCommentableAncestor(range.startContainer);
  const endEl = findCommentableAncestor(range.endContainer);
  if (!startEl || startEl !== endEl) return null;
  const rect = range.getBoundingClientRect();
  if (rect.width === 0 && rect.height === 0) return null;
  return {
    messageId: startEl.dataset.messageId!,
    snippet: text,
    rect,
  };
}

function MessageSelectionToolbar({ onComment }: Props) {
  const [state, setState] = useState<ToolbarState | null>(null);
  const rafRef = useRef<number | null>(null);

  useEffect(() => {
    const recompute = () => {
      if (rafRef.current != null) cancelAnimationFrame(rafRef.current);
      rafRef.current = requestAnimationFrame(() => {
        rafRef.current = null;
        setState(computeState());
      });
    };
    const hide = () => setState(null);

    document.addEventListener("selectionchange", recompute);
    window.addEventListener("resize", hide);
    // Hide during scrolling to avoid stale positioning; user can re-select.
    window.addEventListener("scroll", hide, true);

    return () => {
      document.removeEventListener("selectionchange", recompute);
      window.removeEventListener("resize", hide);
      window.removeEventListener("scroll", hide, true);
      if (rafRef.current != null) cancelAnimationFrame(rafRef.current);
    };
  }, []);

  if (!state) return null;

  const BTN_WIDTH = 40;
  const BTN_HEIGHT = 36;
  // The native copy/paste menu on mobile sits directly above the selection
  // and is horizontally centered. To avoid colliding with it we anchor our
  // button to the right edge of the selection, vertically centered with the
  // selection's midpoint. On narrow selections we clamp to the viewport.
  const isCoarse = window.matchMedia("(pointer: coarse)").matches;
  let top: number;
  let left: number;
  if (isCoarse) {
    const GAP = 8;
    // Prefer to the right of the selection.
    const rightCandidate = state.rect.right + GAP;
    if (rightCandidate + BTN_WIDTH <= window.innerWidth - 8) {
      left = rightCandidate;
    } else {
      // Otherwise to the left.
      const leftCandidate = state.rect.left - BTN_WIDTH - GAP;
      left = leftCandidate >= 8 ? leftCandidate : window.innerWidth - BTN_WIDTH - 8;
    }
    const midY = state.rect.top + state.rect.height / 2 - BTN_HEIGHT / 2;
    top = Math.max(8, Math.min(window.innerHeight - BTN_HEIGHT - 8, midY));
  } else {
    // Desktop: anchor above the selection; fall back below if no room.
    const BTN_GAP = 8;
    const preferTop = state.rect.top - BTN_HEIGHT - BTN_GAP;
    top = preferTop > 8 ? preferTop : state.rect.bottom + BTN_GAP;
    const centered = state.rect.left + state.rect.width / 2 - BTN_WIDTH / 2;
    left = Math.max(8, Math.min(window.innerWidth - BTN_WIDTH - 8, centered));
  }

  const handleClick = (e: React.MouseEvent) => {
    e.stopPropagation();
    onComment(state.messageId, state.snippet);
    // Clear selection after injecting so toolbar hides.
    window.getSelection()?.removeAllRanges();
    setState(null);
  };

  // preventDefault on pointerdown prevents the selection from being
  // cleared/collapsed before we can read it in onClick.
  const swallow = (e: React.SyntheticEvent) => e.preventDefault();

  return createPortal(
    <Button
      type="button"
      variant="outline"
      size="icon"
      className="fixed z-[10000] h-9 touch-manipulation rounded-full shadow-lg"
      style={{ top, left, width: BTN_WIDTH }}
      onMouseDown={swallow}
      onPointerDown={swallow}
      onTouchStart={swallow}
      onClick={handleClick}
      data-testid="message-selection-comment-btn"
      aria-label="Comment"
      title="Comment"
    >
      <MessageSquareIcon className="size-[18px]" />
    </Button>,
    document.body,
  );
}

export default MessageSelectionToolbar;
