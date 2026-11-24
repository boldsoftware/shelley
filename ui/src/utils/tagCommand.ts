import { foldTag } from "./tagFilter";

export type TagEdit = { tag: string; add: boolean };

export function parseTagCommand(args: string): TagEdit[] {
  return args
    .trim()
    .split(/\s+/)
    .map((token) => {
      const [, operation, name] = /^([+-])([^+-]\S*)$/.exec(token) ?? [];
      if (!operation || !foldTag(name ?? "")) throw new Error("Usage: /tag +tag -tag");
      return { tag: name, add: operation === "+" };
    });
}

export function diffTags(before: string[], after: string[]): TagEdit[] {
  const oldKeys = new Set(before.map(foldTag));
  const newKeys = new Set(after.map(foldTag));
  return [
    ...before.filter((tag) => !newKeys.has(foldTag(tag))).map((tag) => ({ tag, add: false })),
    ...after.filter((tag) => !oldKeys.has(foldTag(tag))).map((tag) => ({ tag, add: true })),
  ];
}
