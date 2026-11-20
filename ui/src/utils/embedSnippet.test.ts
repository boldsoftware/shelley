import { embedSnippet } from "./embedSnippet";

function check(origin: string, shelleyHost: string | undefined, src: string, vmHost: string): void {
  const got = embedSnippet({ origin, hostname: new URL(origin).hostname }, shelleyHost);
  const want = { snippet: `<script src="${src}" defer></script>`, vmHost };
  if (JSON.stringify(got) !== JSON.stringify(want)) {
    throw new Error(
      `${origin}, ${shelleyHost}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`,
    );
  }
  console.log(`\u2713 ${origin} ${shelleyHost}`);
}

check("http://localhost:9000", undefined, "http://localhost:9000/embed.js", "");
check(
  "https://vm.shelley.exe.xyz",
  "vm.shelley.exe.xyz",
  "https://vm.shelley.exe.xyz/__exe.dev/embed.js",
  "vm.exe.xyz",
);
check(
  "https://vm.shelley.exe-staging.xyz",
  "vm.shelley.exe-staging.xyz",
  "https://vm.shelley.exe-staging.xyz/__exe.dev/embed.js",
  "vm.exe-staging.xyz",
);
// A local exe.dev's proxy has a port.
check(
  "http://vm.shelley.exe.cloud:8080",
  "vm.shelley.exe.cloud",
  "http://vm.shelley.exe.cloud:8080/__exe.dev/embed.js",
  "vm.exe.cloud",
);
// Other Shelleys on the VM: behind the proxy on a port, on localhost, or
// on a domain of the user's own.
check("https://vm.exe.xyz:8000", "vm.shelley.exe.xyz", "https://vm.exe.xyz:8000/embed.js", "");
check("http://localhost:3843", "vm.shelley.exe.xyz", "http://localhost:3843/embed.js", "");
check(
  "https://vm.shelley.example.com",
  "vm.shelley.exe.xyz",
  "https://vm.shelley.example.com/embed.js",
  "",
);
