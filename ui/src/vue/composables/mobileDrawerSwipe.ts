import { nextTick, onMounted, onUnmounted, type Ref } from "vue";

const MODAL_OVERLAY_SELECTOR = [
  '[aria-modal="true"]',
  ".diff-viewer-overlay",
  ".image-comment-overlay",
  ".command-palette-overlay",
].join(",");

export function hasHorizontalScrollContainer(target: Element | null): boolean {
  for (let element = target; element; element = element.parentElement) {
    const style = window.getComputedStyle(element);
    if (
      (style.overflowX === "auto" || style.overflowX === "scroll") &&
      element.scrollWidth > element.clientWidth
    ) {
      return true;
    }
  }
  return false;
}

export function eventPathHasHorizontalScrollContainer(path: EventTarget[]): boolean {
  return path.some((target) => target instanceof Element && hasHorizontalScrollContainer(target));
}

export function hasOpenModalOverlay(root: ParentNode): boolean {
  return root.querySelector(MODAL_OVERLAY_SELECTOR) !== null;
}

export function useMobileDrawerSwipe(drawerOpen: Ref<boolean>) {
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
    if (gesture.panel)
      gesture.panel.style.transform = `translateX(${(gesture.progress - 1) * 100}%)`;
    if (gesture.scrim) gesture.scrim.style.opacity = String(gesture.progress);
  }
  function settle(open: boolean) {
    const current = gesture;
    gesture = null;
    if (!current) return;
    drawerOpen.value = open;
    current.panel?.style.removeProperty("transition");
    current.panel?.style.removeProperty("transform");
    current.scrim?.style.removeProperty("transition");
    current.scrim?.style.removeProperty("opacity");
  }
  function start(event: TouchEvent) {
    if (gesture?.dragging) settle(!gesture.opening);
    gesture = null;
    if (
      !matchMedia("(max-width: 767px)").matches ||
      event.touches.length !== 1 ||
      hasOpenModalOverlay(document)
    )
      return;
    const touch = event.touches[0];
    const target = event.target instanceof Element ? event.target : null;
    if (
      eventPathHasHorizontalScrollContainer(event.composedPath()) ||
      target?.closest('.status-hook-row, input, textarea, [contenteditable="true"]')
    )
      return;
    if (touch.clientX < 32 || touch.clientX > innerWidth - 32) return;
    if (!drawerOpen.value && !target?.closest(".main-content")) return;
    if (drawerOpen.value && !target?.closest(".app-container")) return;
    gesture = {
      x: touch.clientX,
      y: touch.clientY,
      lastX: touch.clientX,
      time: event.timeStamp,
      velocity: 0,
      opening: !drawerOpen.value,
      dragging: false,
      progress: drawerOpen.value ? 1 : 0,
      width: 0,
      panel: null,
      scrim: null,
    };
  }
  function move(event: TouchEvent) {
    if (!gesture) return;
    if (event.touches.length !== 1 || hasOpenModalOverlay(document)) {
      cancel();
      return;
    }
    const touch = event.touches[0];
    const dx = touch.clientX - gesture.x;
    const dy = touch.clientY - gesture.y;
    if (!gesture.dragging) {
      if (Math.max(Math.abs(dx), Math.abs(dy)) < 10) return;
      if (Math.abs(dy) * 1.5 >= Math.abs(dx) || (gesture.opening ? dx <= 0 : dx >= 0)) {
        gesture = null;
        return;
      }
      const current = gesture;
      current.panel = document.querySelector<HTMLElement>(".drawer");
      if (!current.panel) throw new Error("Conversation drawer surface missing");
      current.width = current.panel.getBoundingClientRect().width;
      current.dragging = true;
      current.panel.style.transition = "none";
      reveal();
      drawerOpen.value = true;
      void nextTick(() => {
        if (gesture !== current) return;
        current.scrim = document.querySelector<HTMLElement>(".backdrop");
        if (!current.scrim) throw new Error("Conversation drawer backdrop missing");
        current.scrim.style.transition = "none";
        reveal();
      });
    }
    event.preventDefault();
    const elapsed = event.timeStamp - gesture.time;
    if (elapsed > 0) gesture.velocity = (touch.clientX - gesture.lastX) / elapsed;
    gesture.lastX = touch.clientX;
    gesture.time = event.timeStamp;
    gesture.progress = Math.max(0, Math.min(1, (gesture.opening ? 0 : 1) + dx / gesture.width));
    reveal();
  }
  function end(event: TouchEvent) {
    if (hasOpenModalOverlay(document)) {
      cancel();
      return;
    }
    if (!gesture?.dragging) {
      gesture = null;
      return;
    }
    const velocity = event.timeStamp - gesture.time < 100 ? gesture.velocity : 0;
    settle(Math.abs(velocity) > 0.45 ? velocity > 0 : gesture.progress > 0.5);
  }
  function cancel() {
    if (gesture?.dragging) settle(!gesture.opening);
    gesture = null;
  }
  onMounted(() => {
    document.addEventListener("touchstart", start, { passive: true });
    document.addEventListener("touchmove", move, { passive: false });
    document.addEventListener("touchend", end);
    document.addEventListener("touchcancel", cancel);
  });
  onUnmounted(() => {
    document.removeEventListener("touchstart", start);
    document.removeEventListener("touchmove", move);
    document.removeEventListener("touchend", end);
    document.removeEventListener("touchcancel", cancel);
    if (gesture?.dragging) settle(!gesture.opening);
  });
}
