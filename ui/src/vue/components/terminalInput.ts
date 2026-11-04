// The server takes websocket messages of at most 32 KiB, and a paste is a
// single string of any size, so input goes out in pieces. 4096 UTF-16 code
// units stay under 32 KiB of JSON even if every one needs a \uXXXX escape.
export const MAX_INPUT = 4096;

/** Splits input into pieces of at most MAX_INPUT code units, never between
 *  the two halves of a surrogate pair. */
export function splitInput(data: string): string[] {
  const pieces: string[] = [];
  let start = 0;
  while (data.length - start > MAX_INPUT) {
    let end = start + MAX_INPUT;
    const last = data.charCodeAt(end - 1);
    if (last >= 0xd800 && last <= 0xdbff) end--;
    pieces.push(data.slice(start, end));
    start = end;
  }
  pieces.push(data.slice(start));
  return pieces;
}
