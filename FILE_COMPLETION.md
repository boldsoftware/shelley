# File and folder completion in the chat composer

Type `@` at the start of a word to search file and folder names in the conversation's
working directory. New conversations use the currently selected directory;
reopened drafts restore their saved directory. Subsequent draft directory picks
apply to completion immediately, before the server's persistence response.

- `@readme` fuzzy-searches file and folder names and, inside Git repositories,
  file contents. Content matches show the matching line and highlight the terms that
  selected the file. Empty folders remain searchable by name.
- Folders have a folder icon and a trailing `/`; choosing one inserts its
  relative directory reference, not its contents.
- An explicit path such as `@./docs` offers that folder itself. Add a trailing
  slash (`@./docs/`) to browse its contents instead.
- `@"My Notes` searches a name containing spaces. Quotes also allow punctuation
  in the query; unquoted queries stop at whitespace or prose punctuation such
  as commas, colons, and parentheses.
- Up/Down selects a result, Enter/Tab inserts it, and clicking works too. Enter and
  Tab wait while either name or content search is still in progress.
- Escape dismisses without blurring. Shift+Enter still inserts a newline.
- Selecting a file or folder replaces only the token around the cursor with an
  `@` reference relative to the conversation's working directory. Ordinary paths stay
  unquoted; spaces and token-delimiting punctuation are quoted and escaped. Surrounding
  text is preserved.
- Completion inserts a reference only: it does **not** read or attach contents.
- Email addresses and shell-mode input do not trigger completion.

Search reuses the existing two-phase `findFiles` flow used by the Command-Shift-P
file finder: a fast name request renders first, while a parallel Git content search
adds matching snippets and content-only files when it finishes. Both requests are
cancelled together when the token or context changes. The name phase opts into folders
with `include_dirs=true`. Results are converted from the API's `search_dir` scope
back to paths relative to the conversation working directory before display and
insertion. Folder matches have `is_dir=true` and a trailing slash
in their relative path. Other callers (including the editor's file finder) still
receive files only by default. Gitignore rules and bounded traversal apply to
normal searches. Path queries can
change the search scope; results are resolved against the API's `search_dir`.
Requests are debounced and cancelled when their input or context changes;
server-side listing, content search, and Git subprocesses share the request's cancellation.
At the candidate limit, mixed searches reserve capacity for both files and
folders, borrowing unused slots without letting either kind crowd out the other.
Errors and empty results leave normal keyboard input available.

## Tests

The regular UI unit suite (`pnpm test` from `ui/`) includes token parsing,
insertion, quoting, cancellation, and stale-response tests. After building
Shelley, run `pnpm exec playwright test e2e/file-completion.spec.ts` from `ui/`
for end-to-end keyboard/mouse, draft-directory, and no-result/error checks.
