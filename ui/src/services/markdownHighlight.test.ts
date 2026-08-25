import { normalizeCodeLanguage } from "./markdownHighlight";

const cases: Array<[string | undefined, string | undefined]> = [
  ["js", "javascript"],
  [" JavaScript ", "javascript"],
  ["ts", "typescript"],
  ["py", "python"],
  ["sh", "shellscript"],
  ["shell", "shellscript"],
  ["rb", "ruby"],
  ["yml", "yaml"],
  ["c++", "cpp"],
  ["cs", "csharp"],
  ["md", "markdown"],
  ["tsx", "tsx"],
  ["  Elixir  ", "elixir"],
  ["hs", "haskell"],
  ["ZIG", "zig"],
  ["Dockerfile", "docker"],
  ["Nix", "nix"],
  ["clj", "clojure"],
  ["unknown", undefined],
  [undefined, undefined],
];

let failed = 0;
for (const [input, expected] of cases) {
  const actual = normalizeCodeLanguage(input);
  if (actual !== expected) {
    failed++;
    console.error(
      `FAIL: ${String(input)} normalized to ${String(actual)}, expected ${String(expected)}`,
    );
  }
}

if (failed > 0) process.exit(1);
console.log(`${cases.length} code-language aliases passed`);
