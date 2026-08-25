// lastLine.ts — "what is happening right now" projection of a blob of
// streamed output: its last non-blank line, ANSI-stripped and truncated.
// Shared by the subagent activity strip and the running tool pills.
import { stripAnsi } from "./ansi";

// Truncation bound. Every caller renders into a single clipped line, so this
// only has to be short enough that no caller pays for a pathological line.
const MAX_LEN = 120;

// An escape sequence with no terminator: the stream is sampled while the tool
// writes, so the last chunk can stop mid-sequence. stripAnsi only removes
// complete sequences, and the remainder would otherwise show up as "[32" (a
// cut CSI) or "]0;my-tit" (a cut OSC/string sequence) noise.
/* eslint-disable no-control-regex */
const PARTIAL_SEQ = /(?:\x1b\[|\x9b)[\x30-\x3f]*[\x20-\x2f]*$|\x1b[\]PX^_][^\x07\x1b]*$|\x1b$/;
/* eslint-enable no-control-regex */

// A line that only looks like whitespace. trim() and \s cover Unicode spaces
// (including U+FEFF), but not the zero-width formatting characters below, and
// callers read "" as "nothing to show" (a tool pill falls back to its
// headline), so such a line must not count as content. Tested rather than
// stripped, because U+200D is meaningful mid-line -- it joins emoji sequences
// (\u{1F469}\u200D\u{1F4BB}), which stripping would break.
const BLANK = /^[\s\u200b-\u200d\u2060]*$/;

/**
 * Last non-blank line of text (see BLANK: whitespace and zero-width characters
 * don't count as content), with ANSI escapes removed and truncated to maxLen
 * (an ellipsis marks truncation). Carriage returns split lines too:
 * progress bars rewrite one line with \r, and the final chunk is that line's
 * current state.
 *
 * Scans backwards a line at a time rather than splitting the whole blob: this
 * runs on every stream event (per tool-progress tick, per assistant text
 * delta) against text that only grows, so it must cost the length of the last
 * line, not the length of the output so far.
 */
export function lastLine(text: string, maxLen = MAX_LEN): string {
  for (let end = text.length; end > 0; ) {
    const start = Math.max(text.lastIndexOf("\n", end - 1), text.lastIndexOf("\r", end - 1)) + 1;
    const line = stripAnsi(text.slice(start, end)).trimEnd().replace(PARTIAL_SEQ, "").trim();
    if (!BLANK.test(line)) {
      if (line.length > maxLen) {
        // Don't split a surrogate pair: a lone half renders as U+FFFD.
        const cut = /[\uD800-\uDBFF]/.test(line[maxLen - 1]) ? maxLen - 1 : maxLen;
        return line.slice(0, cut) + "\u2026";
      }
      return line;
    }
    end = start - 1;
  }
  return "";
}
