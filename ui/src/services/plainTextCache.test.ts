import { PlainTextCache } from "./plainTextCache";

let strips = 0;
const cache = new PlainTextCache((content) => {
  strips++;
  return content.replace(/[*_]/g, "");
});

function check(actual: unknown, expected: unknown, message: string): void {
  if (actual !== expected) throw new Error(`${message}: ${String(actual)}`);
}

check(cache.get("m1", "**hello**"), "hello", "strips content");
check(cache.get("m1", "**hello**"), "hello", "reuses cached text");
check(strips, 1, "same message and content hash strips once");
check(cache.get("m1", "_updated_"), "updated", "updates text");
check(strips, 2, "message update recomputes plain text");
check(cache.size, 1, "message update invalidates previous content hash");
cache.invalidate("m1");
check(cache.size, 0, "explicit invalidation removes cached text");

console.log("plainTextCache tests passed");
