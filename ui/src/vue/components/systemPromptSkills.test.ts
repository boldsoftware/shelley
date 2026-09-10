import assert from "node:assert/strict";
import { extractSystemPromptSkills } from "./systemPromptSkills";

const prompt = `Before
<available_skills>
<skill>
<name>diagrams</name>
<description>Draw &#34;quoted&#34; &amp; explain &lt;things&gt;.</description>
<activate>shelley skill cat diagrams</activate>
</skill>
<skill>
<name>deploy</name>
<description>Ship it.</description>
<activate>shelley skill cat deploy</activate>
</skill>
</available_skills>
After`;

assert.deepEqual(extractSystemPromptSkills(prompt), [
  {
    name: "diagrams",
    description: 'Draw "quoted" & explain <things>.',
    activate: "shelley skill cat diagrams",
  },
  {
    name: "deploy",
    description: "Ship it.",
    activate: "shelley skill cat deploy",
  },
]);
assert.deepEqual(
  extractSystemPromptSkills(prompt, [
    {
      name: "diagrams",
      description: "Original metadata description",
      activate: "original activation",
      source_path: "/tmp/diagrams/SKILL.md",
      origin: "File",
      compatibility: "Requires SVG support",
    },
  ]),
  [
    {
      name: "diagrams",
      description: 'Draw "quoted" & explain <things>.',
      activate: "shelley skill cat diagrams",
      source_path: "/tmp/diagrams/SKILL.md",
      origin: "File",
      compatibility: "Requires SVG support",
    },
    {
      name: "deploy",
      description: "Ship it.",
      activate: "shelley skill cat deploy",
    },
  ],
);
assert.deepEqual(extractSystemPromptSkills("No skills here"), []);

console.log("systemPromptSkills: 3 passed");
