// The snippet that puts embed.js's button on a page, for the Shelley at
// location, and the VM's own host, as vm.exe.xyz, when that Shelley is the
// one exe.dev's proxy serves at shelleyHost, as vm.shelley.exe.xyz: the
// proxy serves its script and decides who frames it. Any other Shelley
// decides that itself.
export function embedSnippet(
  location: { origin: string; hostname: string },
  shelleyHost?: string,
): { snippet: string; vmHost: string } {
  const vmHost =
    shelleyHost && location.hostname === shelleyHost ? shelleyHost.replace(".shelley.", ".") : "";
  const src = `${location.origin}${vmHost ? "/__exe.dev" : ""}/embed.js`;
  return { snippet: `<script src="${src}" defer></` + `script>`, vmHost };
}
