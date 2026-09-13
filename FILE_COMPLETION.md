# File and folder completion in the chat composer

Type `@` at the start of a word to search file and folder names in the conversation's
working directory. New conversations use the currently selected directory;
reopened drafts restore their saved directory. Subsequent draft directory picks
apply to completion immediately, before the server's persistence response.

- `@readme` fuzzy-searches file and folder names, including empty folders.
- Folders have a folder icon and a trailing `/`; choosing one inserts its
  quoted directory path, not its contents.
- An explicit path such as `@./docs` offers that folder itself. Add a trailing
  slash (`@./docs/`) to browse its contents instead.
- `@"My Notes` searches a name containing spaces. Quotes also allow punctuation
  in the query; unquoted queries stop at whitespace or prose punctuation such
  as commas, colons, and parentheses.
- Up/Down selects a result, Enter/Tab inserts it, and clicking works too.
- Escape dismisses without blurring. Shift+Enter still inserts a newline.
- Selecting a file or folder replaces only the token around the cursor with a quoted,
  escaped absolute path. Surrounding text is preserved.
- Completion inserts a reference only: it does **not** read or attach contents.
- Email addresses and shell-mode input do not trigger completion.

Search reuses the existing `findFiles` API in name-only mode, opting into folders
with `include_dirs=true`. Folder matches have `is_dir=true` and a trailing slash
in their relative path. Other callers (including the editor's file finder) still
receive files only by default. Gitignore rules and bounded traversal apply to
normal searches. Path queries can
change the search scope; results are resolved against the API's `search_dir`.
Requests are debounced and cancelled when their input or context changes;
server-side listing and Git subprocesses share the request's cancellation.
At the candidate limit, mixed searches reserve capacity for both files and
folders, borrowing unused slots without letting either kind crowd out the other.
Errors and empty results leave normal keyboard input available.

## Tests

The regular UI unit suite (`pnpm test` from `ui/`) includes token parsing,
insertion, quoting, cancellation, and stale-response tests. After building
Shelley, run `pnpm exec playwright test e2e/file-completion.spec.ts` from `ui/`
for end-to-end keyboard/mouse, draft-directory, and no-result/error checks.
