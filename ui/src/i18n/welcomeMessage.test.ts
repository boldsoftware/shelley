// Verifies the two welcome-message variants keep their intended placeholders
// across every locale:
//   * welcomeMessage (exe.dev host) advertises the proxy, so it must include
//     the {docsLink} and {proxyLink} placeholders.
//   * welcomeMessageLocal (non-exe.dev host) omits proxy details, so it must
//     NOT include {docsLink} or {proxyLink}.
// Both must reference {hostname} and advertise Shelley being open-source and
// customizable via {openSourceLink} and {customizeLink}. This guards against a
// translation being copied without stripping the proxy sentence.
//
// Run with: tsx src/i18n/welcomeMessage.test.ts
import type { TranslationKeys } from "./types";
import { en } from "./en";
import { ja } from "./ja";
import { fr } from "./fr";
import { ru } from "./ru";
import { es } from "./es";
import { upgoer5 } from "./upgoer5";
import { zhCN } from "./zh-CN";
import { zhTW } from "./zh-TW";
import { vi } from "./vi";

const locales: Record<string, TranslationKeys> = {
  en,
  ja,
  fr,
  ru,
  es,
  "zh-CN": zhCN,
  "zh-TW": zhTW,
  upgoer5,
  vi,
};

let passed = 0;
let failed = 0;
const failures: string[] = [];
function check(name: string, cond: boolean, detail?: unknown) {
  if (cond) {
    passed++;
  } else {
    failed++;
    failures.push(`✗ ${name}${detail !== undefined ? `\n   ${JSON.stringify(detail)}` : ""}`);
  }
}

for (const [name, dict] of Object.entries(locales)) {
  const full = dict.welcomeMessage;
  const local = dict.welcomeMessageLocal;

  check(`${name}: welcomeMessage defined`, typeof full === "string" && full.length > 0);
  check(`${name}: welcomeMessageLocal defined`, typeof local === "string" && local.length > 0);

  check(`${name}: welcomeMessage has {hostname}`, full.includes("{hostname}"), full);
  check(`${name}: welcomeMessage has {docsLink}`, full.includes("{docsLink}"), full);
  check(`${name}: welcomeMessage has {proxyLink}`, full.includes("{proxyLink}"), full);

  check(`${name}: welcomeMessageLocal has {hostname}`, local.includes("{hostname}"), local);
  check(`${name}: welcomeMessageLocal omits {docsLink}`, !local.includes("{docsLink}"), local);
  check(`${name}: welcomeMessageLocal omits {proxyLink}`, !local.includes("{proxyLink}"), local);

  // Both variants advertise Shelley being open-source and customizable via
  // the {openSourceLink} and {customizeLink} placeholders.
  for (const [variant, msg] of [
    ["welcomeMessage", full],
    ["welcomeMessageLocal", local],
  ] as const) {
    check(`${name}: ${variant} has {openSourceLink}`, msg.includes("{openSourceLink}"), msg);
    check(`${name}: ${variant} has {customizeLink}`, msg.includes("{customizeLink}"), msg);
  }
  check(
    `${name}: welcomeOpenSource defined`,
    typeof dict.welcomeOpenSource === "string" && dict.welcomeOpenSource.length > 0,
  );
  check(
    `${name}: welcomeCustomize defined`,
    typeof dict.welcomeCustomize === "string" && dict.welcomeCustomize.length > 0,
  );
}

if (failed > 0) {
  console.error(failures.join("\n"));
  console.error(`\n${passed} passed, ${failed} failed`);
  process.exit(1);
}
console.log(`welcomeMessage: ${passed} passed`);
