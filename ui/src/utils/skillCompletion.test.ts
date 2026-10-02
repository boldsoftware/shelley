import assert from "node:assert/strict";
import { test } from "node:test";
import { filterSkills, insertSkill, skillTokenAt } from "./skillCompletion";

const foo = { name: "foo", description: "Make diagrams", activate: "custom loader --skill foo" };

test("exact command and multi-word queries work at the caret, including mid-draft", () => {
  assert.deepEqual(skillTokenAt("/skills", 7), { start: 0, end: 7, query: "" });
  assert.deepEqual(skillTokenAt("Please /skills Make diagrams then continue", 28), {
    start: 7,
    end: 28,
    query: "Make diagrams",
  });
  assert.deepEqual(skillTokenAt("one\n/skills foo\nlast", 15), { start: 4, end: 15, query: "foo" });
  assert.deepEqual(skillTokenAt("/skills foo /skills bar", 23), {
    start: 12,
    end: 23,
    query: "bar",
  });
});

test("partial commands, paths, quoted examples, selection, and other lines are not skill tokens", () => {
  for (const text of [
    "/skill",
    "/skillsx",
    "x/skills",
    "\\/skills",
    "`/skills`",
    "/skills\nnext",
    "https://a/skills",
  ]) {
    assert.equal(skillTokenAt(text, text.length), null, text);
  }
  assert.equal(skillTokenAt("/skills", 7, 0), null);
  assert.equal(skillTokenAt("/skills", -1), null);
  assert.equal(skillTokenAt("/skills", 8), null);
});

test("insertion uses server activation verbatim and preserves surrounding text and caret", () => {
  const text = "Before /skills foo after\nnotes";
  const token = skillTokenAt(text, 18)!;
  const result = insertSkill(text, token, foo);
  const inserted =
    "Use the `foo` skill for this task.\nLoad its instructions with `custom loader --skill foo`.\n\n";
  assert.equal(result.text, `Before ${inserted} after\nnotes`);
  assert.equal(result.cursor, "Before ".length + inserted.length);
  assert.equal(result.text.slice(result.cursor), " after\nnotes");
  const secondDraft = `${result.text}\n/skills`;
  const repeated = insertSkill(secondDraft, skillTokenAt(secondDraft, secondDraft.length)!, foo);
  assert.equal(repeated.text.split(inserted).length - 1, 2);
});

test("filtering is case-insensitive across name and description, not source or activation", () => {
  const skills = [
    foo,
    { name: "web", description: "Search online", activate: "foo", source_path: "diagrams" },
  ];
  assert.deepEqual(filterSkills(skills, "FOO DIAGRAMS"), [foo]);
  assert.deepEqual(filterSkills(skills, "online"), [skills[1]]);
  assert.deepEqual(filterSkills(skills, "  "), skills);
  assert.deepEqual(filterSkills(skills, "unknown"), []);
});
