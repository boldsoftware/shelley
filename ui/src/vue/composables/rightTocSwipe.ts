import { nextTick, onMounted, onUnmounted } from "vue";
import { eventPathHasHorizontalScrollContainer } from "./mobileDrawerSwipe";

export function useRightTocSwipe(
  isOpen: () => boolean,
  show: (event: Event, target: HTMLElement) => void,
  hide: () => void,
) {
  type Gesture = {
    x: number;
    y: number;
    lastX: number;
    time: number;
    velocity: number;
    opening: boolean;
    dragging: boolean;
    progress: number;
    width: number;
    panel: HTMLElement | null;
    scrim: HTMLElement | null;
  };
  let gesture: Gesture | null = null;
  function reveal() {
    if (!gesture) return;
    gesture.panel?.style.setProperty(
      "translate",
      `${(1 - gesture.progress) * 100}% 0`,
      "important",
    );
    if (gesture.scrim) gesture.scrim.style.opacity = String(gesture.progress);
  }
  function bindSurface(current: Gesture) {
    if (gesture !== current) return;
    current.panel = document.querySelector<HTMLElement>(".toc-drawer");
    current.scrim = document.querySelector<HTMLElement>(".toc-drawer-scrim");
    if (!current.panel || !current.scrim) throw new Error("TOC drawer surface missing");
    current.width = current.panel.getBoundingClientRect().width;
    current.panel.style.setProperty("transition", "none", "important");
    current.panel.style.opacity = "1";
    current.scrim.style.transition = "none";
    reveal();
  }
  function settle(open: boolean) {
    const current = gesture;
    gesture = null;
    if (!current) return;
    current.panel?.style.removeProperty("transition");
    current.panel?.style.removeProperty("translate");
    current.panel?.style.removeProperty("opacity");
    current.scrim?.style.removeProperty("transition");
    current.scrim?.style.removeProperty("opacity");
    if (!open) hide();
  }
  function hasBlockingModal() {
    return [
      ...document.querySelectorAll(
        '[aria-modal="true"], .diff-viewer-overlay, .image-comment-overlay, .command-palette-overlay',
      ),
    ].some((element) => !element.matches(".toc-drawer"));
  }
  function start(event: TouchEvent) {
    if (gesture?.dragging) settle(!gesture.opening);
    gesture = null;
    if (!matchMedia("(max-width: 767px)").matches || event.touches.length !== 1) return;
    if (hasBlockingModal()) return;
    const touch = event.touches[0];
    const target = event.target instanceof Element ? event.target : null;
    if (
      eventPathHasHorizontalScrollContainer(event.composedPath()) ||
      target?.closest('.status-hook-row, input, textarea, [contenteditable="true"]')
    )
      return;
    if (touch.clientX < 32 || touch.clientX > innerWidth - 32) return;
    if (!isOpen() && !target?.closest(".main-content")) return;
    gesture = {
      x: touch.clientX,
      y: touch.clientY,
      lastX: touch.clientX,
      time: event.timeStamp,
      velocity: 0,
      opening: !isOpen(),
      dragging: false,
      progress: isOpen() ? 1 : 0,
      width: Math.min(innerWidth * 0.88, 360),
      panel: null,
      scrim: null,
    };
    if (isOpen()) event.stopPropagation();
  }
  function move(event: TouchEvent) {
    if (!gesture) return;
    if (event.touches.length !== 1 || hasBlockingModal()) {
      cancel();
      return;
    }
    const touch = event.touches[0];
    const dx = touch.clientX - gesture.x;
    const dy = touch.clientY - gesture.y;
    if (!gesture.dragging) {
      if (Math.max(Math.abs(dx), Math.abs(dy)) < 10) return;
      if (Math.abs(dy) * 1.5 >= Math.abs(dx) || (gesture.opening ? dx >= 0 : dx <= 0)) {
        gesture = null;
        return;
      }
      const button = document.querySelector<HTMLElement>(".toc-button");
      if (!button) {
        gesture = null;
        return;
      }
      gesture.dragging = true;
      const current = gesture;
      if (gesture.opening) {
        show(event, button);
        void nextTick(() => bindSurface(current));
      } else bindSurface(current);
    }
    event.preventDefault();
    const elapsed = event.timeStamp - gesture.time;
    if (elapsed > 0) gesture.velocity = (touch.clientX - gesture.lastX) / elapsed;
    gesture.lastX = touch.clientX;
    gesture.time = event.timeStamp;
    gesture.progress = Math.max(0, Math.min(1, (gesture.opening ? 0 : 1) - dx / gesture.width));
    reveal();
  }
  function end(event: TouchEvent) {
    if (hasBlockingModal()) {
      cancel();
      return;
    }
    if (!gesture?.dragging) {
      gesture = null;
      return;
    }
    const velocity = event.timeStamp - gesture.time < 100 ? gesture.velocity : 0;
    settle(Math.abs(velocity) > 0.45 ? velocity < 0 : gesture.progress > 0.5);
  }
  function cancel() {
    if (gesture?.dragging) settle(!gesture.opening);
    gesture = null;
  }
  onMounted(() => {
    document.addEventListener("touchstart", start, { capture: true, passive: true });
    document.addEventListener("touchmove", move, { passive: false });
    document.addEventListener("touchend", end);
    document.addEventListener("touchcancel", cancel);
  });
  onUnmounted(() => {
    document.removeEventListener("touchstart", start, true);
    document.removeEventListener("touchmove", move);
    document.removeEventListener("touchend", end);
    document.removeEventListener("touchcancel", cancel);
    if (gesture?.dragging) settle(!gesture.opening);
  });
}
