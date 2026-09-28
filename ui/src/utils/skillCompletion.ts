import type { SkillDescriptor } from "../services/api";

export interface SkillToken {
  start: number;
  end: number;
  query: string;
}

/** A skill query runs from /skills to the caret on the same line. Text after
 * the caret belongs to the surrounding draft and is never consumed. */
export function skillTokenAt(
  text: string,
  cursor: number,
  selectionEnd = cursor,
): SkillToken | null {
  if (cursor !== selectionEnd || cursor < 0 || cursor > text.length) return null;
  let token: SkillToken | null = null;
  for (const match of text.matchAll(/(?:^|\s)\/skills(?=$|\s)/g)) {
    const start = match.index! + match[0].indexOf("/");
    const commandEnd = start + "/skills".length;
    if (cursor < commandEnd) continue;
    const query = text.slice(commandEnd, cursor);
    if (/[\r\n]/.test(query)) continue;
    token = { start, end: cursor, query: query.trim() };
  }
  return token;
}

export function filterSkills(skills: SkillDescriptor[], query: string) {
  const terms = query.toLowerCase().split(/\s+/).filter(Boolean);
  return skills.filter((skill) => {
    const text = `${skill.name} ${skill.description}`.toLowerCase();
    return terms.every((term) => text.includes(term));
  });
}

export function insertSkill(text: string, token: SkillToken, skill: SkillDescriptor) {
  const instruction = `Use the \`${skill.name}\` skill for this task.\nLoad its instructions with \`${skill.activate}\`.\n\n`;
  return {
    text: text.slice(0, token.start) + instruction + text.slice(token.end),
    cursor: token.start + instruction.length,
  };
}
