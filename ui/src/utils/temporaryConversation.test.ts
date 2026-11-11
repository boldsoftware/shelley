import assert from "node:assert/strict";
import { isTemporaryConversation, archiverOn, temporaryTooltip } from "./temporaryConversation";

assert.equal(archiverOn("https://archive-store.int.exe.xyz/"), true);
for (const off of ["", null, undefined, true, false]) {
  assert.equal(archiverOn(off), false, `${JSON.stringify(off)} is off`);
}

assert.equal(isTemporaryConversation({ conversation_options: '{"temporary":true}' }), true);
assert.equal(isTemporaryConversation({ conversation_options: '{"profile":"x"}' }), false);
assert.equal(isTemporaryConversation({ conversation_options: "" }), false);
assert.equal(isTemporaryConversation(null), false);

// Off, the tooltip says the archive store's readers can read the conversation; on, that it isn't archived.
assert.match(temporaryTooltip(false), /anyone with access to the store can read it/);
assert.match(temporaryTooltip(true), /isn't archived/);
console.log("temporaryConversation: ok");
