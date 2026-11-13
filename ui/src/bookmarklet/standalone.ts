// dist/shelley-bookmarklet.html: make a bookmarklet for any Shelley.
import { bookmarkletHref } from "./href";
import { BOOKMARKLET_SOURCE } from "./source";

const input = document.getElementById("origin") as HTMLInputElement;
const link = document.getElementById("link") as HTMLAnchorElement;

function update() {
  let origin = "";
  try {
    const url = new URL(input.value.trim());
    if (url.protocol === "https:" || url.protocol === "http:") origin = url.origin;
  } catch {
    // Not a URL yet.
  }
  link.toggleAttribute("href", !!origin);
  if (origin) link.href = bookmarkletHref(BOOKMARKLET_SOURCE, origin);
}

if (location.protocol === "https:" || location.protocol === "http:") input.value = location.origin;
input.addEventListener("input", update);
// Dragged to the bookmarks bar, not run here.
link.addEventListener("click", (event) => event.preventDefault());
update();
