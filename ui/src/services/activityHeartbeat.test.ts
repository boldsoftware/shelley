import assert from "node:assert/strict";
import { test } from "node:test";
import { ACTIVITY_THROTTLE_MS, startActivityHeartbeat } from "./activityHeartbeat";

test("interaction sends a throttled heartbeat until stopped", (t) => {
  t.mock.timers.enable({ apis: ["Date"] });
  const target = new EventTarget();
  let beats = 0;
  const stop = startActivityHeartbeat(target, () => beats++);
  target.dispatchEvent(new Event("keydown"));
  target.dispatchEvent(new Event("pointermove"));
  target.dispatchEvent(new Event("scroll"));
  assert.equal(beats, 1);
  t.mock.timers.tick(ACTIVITY_THROTTLE_MS);
  target.dispatchEvent(new Event("focus"));
  assert.equal(beats, 2);
  target.dispatchEvent(new Event("unrelated"));
  t.mock.timers.tick(ACTIVITY_THROTTLE_MS);
  target.dispatchEvent(new Event("unrelated"));
  assert.equal(beats, 2);
  stop();
  target.dispatchEvent(new Event("pointerdown"));
  assert.equal(beats, 2);
});
