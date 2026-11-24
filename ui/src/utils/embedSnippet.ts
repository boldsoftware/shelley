// The Edit with Shelley snippet, as in embed/edit-with-shelley.html, which
// embedSnippet.test.ts checks it against. Its frame shows SNIPPET_SHELLEY.
export const SNIPPET = `<!-- Edit with Shelley: a button that opens Shelley beside this page.
     Only people who can sign in to your Shelley can use it,
     so show it only to them (for example, only to your admins). -->
<details class="edit-with-shelley">
  <summary>Edit with Shelley</summary>
  <iframe src="https://shelley.example.com/" title="Shelley" loading="lazy"
    allow="clipboard-read; clipboard-write; microphone; display-capture"></iframe>
</details>
<style>
  .edit-with-shelley { position: fixed; top: 12px; right: 12px; z-index: 2147483647; font: 600 13px system-ui, sans-serif; }
  .edit-with-shelley summary { list-style: none; cursor: pointer; padding: 8px 14px; border: 1px solid #8885;
    border-radius: 99px; background: Canvas; color: CanvasText; box-shadow: 0 2px 10px #0002; }
  .edit-with-shelley summary::-webkit-details-marker { display: none; }
  .edit-with-shelley[open] summary { font-size: 0; }
  .edit-with-shelley[open] summary::before { content: "Close"; content: "Close" / ""; font-size: 13px; }
  .edit-with-shelley iframe { position: fixed; top: 0; right: 0; width: min(480px, 100vw); height: 100%;
    box-sizing: border-box; border: 0; border-left: 1px solid #8884; background: Canvas; }
  .edit-with-shelley[open] { right: calc(min(480px, 100vw) + 12px); }
  @media screen and (min-width: 900px) { html:has(.edit-with-shelley[open]) { margin-right: 480px !important; } }
  @media (max-width: 899px) {
    .edit-with-shelley[open] { right: 12px; }
    .edit-with-shelley iframe { top: 58px; height: calc(100% - 58px); }
  }
  @media print { .edit-with-shelley { display: none; } }
</style>
`;
export const SNIPPET_SHELLEY = "https://shelley.example.com/";

// embedSnippet returns the snippet for the Shelley at location, and the
// VM's own host, as vm.exe.xyz, when that Shelley is the one exe.dev's
// proxy serves at shelleyHost, as vm.shelley.exe.xyz: the proxy decides
// who frames it, and lets only the VM's own pages. Any other Shelley
// decides that itself.
export function embedSnippet(
  location: { origin: string; hostname: string },
  shelleyHost?: string,
): { snippet: string; vmHost: string } {
  const vmHost =
    shelleyHost && location.hostname === shelleyHost ? shelleyHost.replace(".shelley.", ".") : "";
  const snippet = SNIPPET.replace(SNIPPET_SHELLEY, () => `${location.origin}/`);
  return { snippet, vmHost };
}
