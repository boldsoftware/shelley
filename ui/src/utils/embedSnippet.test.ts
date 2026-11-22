import { readFileSync } from "node:fs";
import { embedSnippet, SNIPPET, SNIPPET_SHELLEY } from "./embedSnippet";

const file = readFileSync(
  new URL("../../../embed/edit-with-shelley.html", import.meta.url),
  "utf8",
);
if (SNIPPET !== file) throw new Error("SNIPPET differs from embed/edit-with-shelley.html");
console.log("\u2713 SNIPPET is embed/edit-with-shelley.html");

function check(origin: string, shelleyHost: string | undefined, vmHost: string): void {
  const got = embedSnippet({ origin, hostname: new URL(origin).hostname }, shelleyHost);
  const want = { snippet: SNIPPET.replace(SNIPPET_SHELLEY, `${origin}/`), vmHost };
  if (JSON.stringify(got) !== JSON.stringify(want) || !got.snippet.includes(`src="${origin}/"`)) {
    throw new Error(
      `${origin}, ${shelleyHost}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`,
    );
  }
  console.log(`\u2713 ${origin} ${shelleyHost}`);
}

check("http://localhost:9000", undefined, "");
check("https://vm.shelley.exe.xyz", "vm.shelley.exe.xyz", "vm.exe.xyz");
check("https://vm.shelley.exe-staging.xyz", "vm.shelley.exe-staging.xyz", "vm.exe-staging.xyz");
// A local exe.dev's proxy has a port.
check("http://vm.shelley.exe.cloud:8080", "vm.shelley.exe.cloud", "vm.exe.cloud");
// Other Shelleys on the VM: behind the proxy on a port, on localhost, or
// on a domain of the user's own.
check("https://vm.exe.xyz:8000", "vm.shelley.exe.xyz", "");
check("http://localhost:3843", "vm.shelley.exe.xyz", "");
check("https://vm.shelley.example.com", "vm.shelley.exe.xyz", "");
