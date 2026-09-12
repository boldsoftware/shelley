# Filename completion in the chat composer

Type `@` at the start of a word to search filenames in the conversation's
working directory. New conversations and drafts use the currently selected
directory.

- `@readme` fuzzy-searches filenames.
- `@"My Notes` searches a name containing spaces. Quotes also allow punctuation
  in the query; unquoted queries stop at whitespace or prose punctuation such
  as commas and parentheses.
- Up/Down selects a result, Enter/Tab inserts it, and clicking works too.
- Escape dismisses without blurring. Shift+Enter still inserts a newline.
- Selecting a file replaces only the token around the cursor with a quoted,
  escaped absolute path. Surrounding text is preserved.
- Completion inserts a reference only: it does **not** read or attach contents.
- Email addresses and shell-mode input do not trigger completion.

Search reuses the existing `findFiles` API in name-only mode. Path queries can
change the search scope; results are resolved against the API's `search_dir`.
Requests are debounced and cancelled when their input or context changes.
Errors and empty results leave normal keyboard input available.

## Tests

The regular UI unit suite (`pnpm test` from `ui/`) includes token parsing,
insertion, quoting, cancellation, and stale-response tests. After building
Shelley, run `pnpm exec playwright test e2e/file-completion.spec.ts` from `ui/`
for end-to-end keyboard/mouse, draft-directory, and no-result/error checks.
