import { readFile } from "node:fs/promises";
import postcss from "postcss";

const stylesheetPaths = ["../src/styles.css", "../src/btw.css"].map(
  (path) => new URL(path, import.meta.url),
);
const roots = await Promise.all(
  stylesheetPaths.map(async (path) =>
    postcss.parse(await readFile(path, "utf8"), { from: path.pathname }),
  ),
);

const typeScale = new Map([
  ["--font-size-11", "0.6875rem"],
  ["--font-size-12", "0.75rem"],
  ["--font-size-13", "0.8125rem"],
  ["--font-size-14", "0.875rem"],
]);
const legacyFontSizes = new Set([
  "0.6rem",
  "0.62rem",
  "0.625rem",
  "0.65rem",
  "0.675rem",
  "0.68rem",
  "0.688rem",
  "0.7em",
  "0.7rem",
  "0.72rem",
  "0.74rem",
  "0.75em",
  "0.78rem",
  "0.8rem",
  "0.813rem",
  "0.82rem",
  "0.85em",
  "0.85rem",
  "0.875em",
  "0.9rem",
  "0.9375rem",
  "0.95rem",
  "1rem",
  "1.05rem",
  "1.1rem",
  "1.125rem",
  "1.15rem",
  "1.25rem",
  "1.45rem",
  "1.5rem",
  "1.65rem",
  "2rem",
  "9px",
  "10px",
  "10.5px",
  "12px",
  "13px",
  "16px",
  "inherit",
]);
const expectedLegacyFontSizeDeclarations = 159;
const expectedDuplicateRuleGroups = 182;
const expectedExcessDuplicateRules = 506;
const legacyFontShorthands = new Set([
  "13px/1.55 ui-monospace,\n    SFMono-Regular,\n    Menlo,\n    Consolas,\n    monospace",
]);
const errors = [];

for (const [property, expectedValue] of typeScale) {
  const definitions = [];
  for (const root of roots) {
    root.walkDecls(property, (declaration) => definitions.push(declaration));
  }
  if (definitions.length !== 1 || definitions[0].value !== expectedValue) {
    errors.push(`${property} must be defined exactly once as ${expectedValue}`);
  }
}

let legacyFontSizeDeclarations = 0;
for (const root of roots) {
  root.walkDecls("font-size", (declaration) => {
    if (declaration.value.startsWith("var(")) {
      if (![...typeScale.keys()].some((property) => declaration.value === `var(${property})`)) {
        errors.push(`${declaration.error(`unknown font-size token ${declaration.value}`).message}`);
      }
      return;
    }
    legacyFontSizeDeclarations++;
    if (!legacyFontSizes.has(declaration.value)) {
      errors.push(
        `${declaration.error(`new raw font-size ${declaration.value}; use the shared type scale`).message}`,
      );
    }
  });
  root.walkDecls("font", (declaration) => {
    if (declaration.value !== "inherit" && !legacyFontShorthands.has(declaration.value)) {
      errors.push(`${declaration.error("font shorthand hides a raw font-size").message}`);
    }
  });
}
if (legacyFontSizeDeclarations !== expectedLegacyFontSizeDeclarations) {
  errors.push(
    `raw font-size declarations changed from ${expectedLegacyFontSizeDeclarations} to ${legacyFontSizeDeclarations}; use a shared token when adding one, or lower the baseline after removing one`,
  );
}

const ruleBodies = new Map();
for (const root of roots) {
  root.walkRules((rule) => {
    const declarations = rule.nodes.filter((node) => node.type === "decl");
    if (declarations.length === 0) return;
    const body = declarations
      .map((declaration) => `${declaration.prop}:${declaration.value}:${declaration.important}`)
      .join(";");
    ruleBodies.set(body, (ruleBodies.get(body) ?? 0) + 1);
  });
}
const duplicateCounts = [...ruleBodies.values()].filter((count) => count > 1);
const duplicateRuleGroups = duplicateCounts.length;
const excessDuplicateRules = duplicateCounts.reduce((total, count) => total + count - 1, 0);
if (
  duplicateRuleGroups !== expectedDuplicateRuleGroups ||
  excessDuplicateRules !== expectedExcessDuplicateRules
) {
  errors.push(
    `duplicate declaration blocks changed from ${expectedDuplicateRuleGroups} groups/${expectedExcessDuplicateRules} excess rules to ${duplicateRuleGroups} groups/${excessDuplicateRules}; merge new duplicates, or lower the baseline after removing one`,
  );
}

if (errors.length > 0) {
  console.error(errors.join("\n"));
  process.exit(1);
}
