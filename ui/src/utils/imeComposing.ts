// IME-aware key handling.
//
// When composing text with an IME (e.g. Japanese hiragana→kanji conversion),
// Enter confirms the conversion and must NOT be treated as a submit/confirm
// keystroke. Chrome and Firefox set `KeyboardEvent.isComposing` on that Enter
// keydown, but Safari fires `compositionend` *before* dispatching it, so
// `isComposing` is already false. Safari does, however, still report the
// legacy `keyCode` 229 ("keydown handled by IME") on that event, so we check
// both. See https://dninomiya.github.io/form-guide/stop-enter-submit
export function isImeComposing(e: KeyboardEvent): boolean {
  return e.isComposing || e.keyCode === 229;
}
