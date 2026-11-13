export interface Chapter {
  title: string;
  message_id: string;
}

export type ChapterIndex = Record<string, Chapter[]>;

function terms(query: string): string[] {
  return query.toLocaleLowerCase().match(/[\p{L}\p{N}]+/gu) ?? [];
}

export function chapterMatchScore(title: string, query: string): number {
  const words = terms(query);
  const folded = title.toLocaleLowerCase();
  if (!words.length || !words.every((word) => folded.includes(word))) return 0;
  const phrase = words.join(" ");
  if (folded === phrase) return 4;
  if (folded.startsWith(phrase)) return 3;
  return folded.includes(phrase) ? 2 : 1;
}

export function chapterSearchSegments(title: string, query: string) {
  const words = [...new Set(terms(query))].sort((a, b) => b.length - a.length);
  if (!words.length) return [{ text: title, mark: false }];
  const pattern = new RegExp(`(${words.join("|")})`, "giu");
  return title.split(pattern).map((text, index) => ({ text, mark: index % 2 === 1 }));
}

export function scoreChapterIndex(index: ChapterIndex, query: string): Map<string, number> {
  const scores = new Map<string, number>();
  for (const [id, chapters] of Object.entries(index)) {
    const score = Math.max(
      0,
      ...chapters.map((chapter) => chapterMatchScore(chapter.title, query)),
    );
    if (score) scores.set(id, score);
  }
  return scores;
}

export function mergeChapterResults<T extends { conversation_id: string; is_draft?: boolean }>(
  results: T[],
  pool: T[],
  scores: ReadonlyMap<string, number>,
): T[] {
  const merged = new Map(
    results.map((conversation) => [conversation.conversation_id, conversation]),
  );
  for (const conversation of pool) {
    if (
      !conversation.is_draft &&
      scores.has(conversation.conversation_id) &&
      !merged.has(conversation.conversation_id)
    ) {
      merged.set(conversation.conversation_id, conversation);
    }
  }
  return [...merged.values()].sort(
    (a, b) => (scores.get(b.conversation_id) ?? 0) - (scores.get(a.conversation_id) ?? 0),
  );
}
