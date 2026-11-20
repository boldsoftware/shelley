import { strict as assert } from "node:assert";
import { toolCardPlaceholderKind } from "./toolCardMount";

assert.equal(toolCardPlaceholderKind("bash"), "bash");
assert.equal(toolCardPlaceholderKind("patch"), "patch");
assert.equal(toolCardPlaceholderKind("output_iframe"), "output-iframe");
assert.equal(toolCardPlaceholderKind("output_html"), "output-iframe");
assert.equal(toolCardPlaceholderKind("openai_audio_transcription"), "audio");
assert.equal(toolCardPlaceholderKind("browser", { action: "screenshot" }), "media");
assert.equal(
  toolCardPlaceholderKind("llm_one_shot", {}, { images: [{ url: "/image.png" }] }),
  "media",
);
assert.equal(toolCardPlaceholderKind("llm_one_shot", {}, { images: [] }), "generic");

console.log("toolCardPlaceholderKind tests passed");
