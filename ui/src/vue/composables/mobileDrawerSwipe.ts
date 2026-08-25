import { onMounted, onUnmounted, type Ref } from "vue";

const SYSTEM_EDGE_WIDTH = 32;
const SWIPE_DISTANCE = 48;
const DIRECTION_LOCK_DISTANCE = 10;
const HORIZONTAL_BIAS = 1.25;

type Gesture = {
  startX: number;
  startY: number;
  lastX: number;
  lastY: number;
  opening: boolean;
  cancelled: boolean;
};

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

export function useMobileDrawerSwipe(drawerOpen: Ref<boolean>) {
  let gesture: Gesture | null = null;

  function onTouchStart(event: TouchEvent) {
    if (!window.matchMedia("(max-width: 767px)").matches || event.touches.length !== 1) return;

    const touch = event.touches[0];
    const target = event.target instanceof Element ? event.target : null;
    const opening = !drawerOpen.value;

    // Code blocks, tables, diffs, and other wide content own horizontal
    // gestures. Starting a drawer swipe there makes ordinary scrolling
    // unexpectedly navigate the app.
    if (hasHorizontalScrollContainer(target)) return;

    if (opening) {
      // Leave the true screen edge to the browser/OS back gesture.
      if (touch.clientX < SYSTEM_EDGE_WIDTH || !target?.closest(".main-content")) return;
    } else if (!target?.closest(".app-container")) {
      return;
    }

    gesture = {
      startX: touch.clientX,
      startY: touch.clientY,
      lastX: touch.clientX,
      lastY: touch.clientY,
      opening,
      cancelled: false,
    };
  }

  function onTouchMove(event: TouchEvent) {
    if (!gesture) return;
    if (event.touches.length !== 1) {
      gesture = null;
      return;
    }

    const touch = event.touches[0];
    gesture.lastX = touch.clientX;
    gesture.lastY = touch.clientY;

    const dx = touch.clientX - gesture.startX;
    const dy = touch.clientY - gesture.startY;
    const absX = Math.abs(dx);
    const absY = Math.abs(dy);

    if (Math.max(absX, absY) < DIRECTION_LOCK_DISTANCE) return;
    if (absY > absX) {
      gesture.cancelled = true;
      return;
    }

    const intendedDirection = gesture.opening ? dx > 0 : dx < 0;
    if (!gesture.cancelled && intendedDirection) event.preventDefault();
  }

  function finishGesture(event: TouchEvent) {
    if (!gesture) return;

    const touch = event.changedTouches[0];
    if (touch) {
      gesture.lastX = touch.clientX;
      gesture.lastY = touch.clientY;
    }

    const dx = gesture.lastX - gesture.startX;
    const dy = gesture.lastY - gesture.startY;
    const horizontal =
      Math.abs(dx) >= SWIPE_DISTANCE && Math.abs(dx) > Math.abs(dy) * HORIZONTAL_BIAS;

    if (!gesture.cancelled && horizontal) {
      if (gesture.opening && dx > 0) drawerOpen.value = true;
      if (!gesture.opening && dx < 0) drawerOpen.value = false;
    }

    gesture = null;
  }

  function cancelGesture() {
    gesture = null;
  }

  onMounted(() => {
    document.addEventListener("touchstart", onTouchStart, { passive: true });
    document.addEventListener("touchmove", onTouchMove, { passive: false });
    document.addEventListener("touchend", finishGesture, { passive: true });
    document.addEventListener("touchcancel", cancelGesture, { passive: true });
  });

  onUnmounted(() => {
    document.removeEventListener("touchstart", onTouchStart);
    document.removeEventListener("touchmove", onTouchMove);
    document.removeEventListener("touchend", finishGesture);
    document.removeEventListener("touchcancel", cancelGesture);
  });
}
