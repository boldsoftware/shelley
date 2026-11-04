import { describe, expect, it, report } from "../../test/vitestShim";
import { MAX_INPUT, splitInput } from "./terminalInput";

const messageBytes = (data: string) =>
  new TextEncoder().encode(JSON.stringify({ type: "input", data })).length;

describe("splitInput", () => {
  it("sends short input as is", () => {
    expect(splitInput("ls\r")).toEqual(["ls\r"]);
  });

  it("splits a long paste into messages the server takes", () => {
    // Control characters are the worst case: each one is a \uXXXX escape.
    const paste = "\u0001".repeat(MAX_INPUT * 2 + 1);
    const pieces = splitInput(paste);
    expect(pieces.length).toBe(3);
    expect(pieces.join("")).toBe(paste);
    expect(pieces.every((p) => messageBytes(p) <= 32 << 10)).toBe(true);
  });

  it("never splits a character", () => {
    const paste = "a" + "😀".repeat(MAX_INPUT);
    const pieces = splitInput(paste);
    expect(pieces.join("")).toBe(paste);
    // In unicode mode a pair is one code point; only a lone half matches.
    expect(pieces.some((p) => /\p{Surrogate}/u.test(p))).toBe(false);
  });
});

report("terminalInput");
