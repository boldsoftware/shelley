// visibleTerminals decides which terminals a conversation offers as tabs.
// Getting this wrong leaks another conversation's terminals into view, or hides
// a terminal the user just created, so each rule is pinned here.

import { nextActiveTab, visibleTerminals } from "./terminalHelpers";

let passed = 0;
let failed = 0;
function assert(cond: boolean, msg: string) {
  if (cond) passed++;
  else {
    failed++;
    console.error(`FAIL: ${msg}`);
  }
}

function term(id: string, conversationId: string | null) {
  return { id, conversationId };
}
function ids(list: Array<{ id: string }>): string {
  return list.map((t) => t.id).join(",");
}

// A terminal belonging to another conversation is not shown.
{
  const terminals = [term("a", "conv-1"), term("b", "conv-2")];
  assert(ids(visibleTerminals(terminals, "conv-1")) === "a", "conv-1 sees only its own terminal");
  assert(ids(visibleTerminals(terminals, "conv-2")) === "b", "conv-2 sees only its own terminal");
}

// Global terminals (conversationId === null) show up first everywhere.
{
  const terminals = [term("a", "conv-1"), term("g", null), term("b", "conv-2")];
  assert(ids(visibleTerminals(terminals, "conv-1")) === "g,a", "conv-1 sees global then its own");
  assert(ids(visibleTerminals(terminals, "conv-2")) === "g,b", "conv-2 sees global then its own");
}

// Global terminals come first, and creation order is preserved within each
// group: re-scoping a terminal must not reshuffle the rest.
{
  const terminals = [
    term("g1", null),
    term("l1", "conv-1"),
    term("g2", null),
    term("l2", "conv-1"),
  ];
  assert(
    ids(visibleTerminals(terminals, "conv-1")) === "g1,g2,l1,l2",
    "globals before locals, each in creation order",
  );
}

// On /new there is no conversation, so only global terminals are reachable.
{
  const terminals = [term("a", "conv-1"), term("g", null)];
  assert(ids(visibleTerminals(terminals, null)) === "g", "no conversation shows only globals");
}

// A conversation whose terminals are all owned elsewhere shows nothing, which
// is what hides the dock.
{
  assert(visibleTerminals([term("a", "conv-1")], "conv-2").length === 0, "nothing visible");
}

// nextActiveTab decides the selected tab after the visible set changed. The
// rule that matters most: switching conversations must not drop a selected
// global terminal, because a global terminal is deliberately the same terminal
// in every conversation.
{
  const inConv1 = ["g", "a"];
  const inConv2 = ["g", "b"];
  assert(
    nextActiveTab(inConv1, ["g", "a"], null).id === "a",
    "on load the newest visible terminal is selected",
  );
  assert(
    nextActiveTab(inConv2, [], "g").id === "g",
    "a selected global terminal survives a conversation switch",
  );
  assert(
    nextActiveTab(inConv2, [], "g").created === false,
    "a conversation switch is not treated as a new terminal",
  );
}

// A brand-new terminal takes the selection away from a global one; that is how
// opening a terminal focuses it.
{
  const r = nextActiveTab(["g", "new"], ["new"], "g");
  assert(r.id === "new" && r.created, "a newly created terminal takes the selection");
}

// Selecting a conversation-local terminal and switching away falls back to the
// new conversation's own terminals, since the selected one is not shown there.
{
  assert(nextActiveTab(["g", "b"], [], "a").id === "b", "an invisible selection falls back");
  assert(nextActiveTab([], [], "a").id === null, "no visible terminals means no selection");
}

// Re-scoping the selected terminal keeps it selected: it is still visible, just
// in the other group.
{
  assert(nextActiveTab(["g", "a"], [], "a").id === "a", "re-scoping keeps the selection");
}

if (failed > 0) {
  console.error(`\n${failed} assertion(s) failed, ${passed} passed`);
  process.exit(1);
}
console.log(`\u2713 terminalHelpers: ${passed} assertions passed`);
