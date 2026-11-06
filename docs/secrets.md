# Secrets: capture in the browser, store in Shelley, inject by id

Behind the `secrets` feature flag (off by default). A user can type or
paste a secret into the composer; the browser takes it out of the message
before anything is sent, stores it in Shelley's secret store, and leaves a
placeholder in its place. The model only ever sees the placeholder, and gets
one tool to put the value where a command can use it.

## Syntax

- `{{secret:ID}}` is a reference: what messages hold and the model sees.
  ID is lowercase letters, digits and dashes, e.g. `openai-key-k3f9`: the
  kind of secret plus four random characters, so it is informative to the
  model and never reused. It is immutable; the secret's *name* (a label
  shown on the management page) can be renamed.
- `{{secret=VALUE}}` or `{{secret:NAME=VALUE}}` in the composer marks VALUE
  as a secret by hand (the escape hatch for anything detection misses); its
  ID is `secret-…` or `NAME-…`. The composer's lock button wraps the
  selection in a mark, or opens an empty one at the caret.

## Detection (client side, `ui/src/utils/secretDetect.ts`)

Known formats come from [gitleaks](https://github.com/gitleaks/gitleaks)'
default rules (MIT): `go run ./cmd/secretrules -o
ui/src/generated-secret-rules.ts` fetches a pinned release's
`config/gitleaks.toml` and translates each Go regular expression to
JavaScript through its parsed syntax tree. Each rule keeps gitleaks'
keywords (a rule runs only on text containing one), capture group (only the
secret is sealed, not `API_KEY=`), entropy threshold and allowlists (e.g.
AWS's `…EXAMPLE` key). Left out: `generic-api-key`, which flags any
`key = random` assignment, and rules scoped to file paths. The generator
drops the optional runs (`[\w.-]{0,50}?`) that start gitleaks' keyword
rules: they only move where a match starts, but make JavaScript's
backtracking quadratic. One lookahead regex finds every rule keyword in a
single pass. Bump the version in `cmd/secretrules` to update.

Detection runs on every keystroke, so it works paragraph by paragraph
(split at blank lines): a rule runs on a paragraph containing its keyword,
and the candidates of recently seen paragraphs (up to 1M characters) are
remembered, so an edit costs a scan of its own paragraph, not of the whole
message. Only private keys, whose PEM and PGP blocks hold blank lines, are
matched across the whole text. The composer also reuses the last result
while the text is unchanged (caret moves).

Our own rules add what gitleaks lacks: a private key pasted without its END
line, and PuTTY `.ppk` keys. A generic rule takes long (≥32) tokens with
high entropy that mix upper case, lower case and digits, and are not hex,
UUIDs, URLs, paths, or names with many dashes and underscores (base64url
has about one in 32 characters; more than one in 12 is a name). False negatives are preferred over mangling normal
text, and the manual paths (syntax above, or the lock button) cover the
rest.

The composer seals a finding as soon as it is complete: on paste, when the
caret leaves the token, when a `{{secret=…}}` closes, and at the latest on
send. Sealing replaces the text with `{{secret:ID}}` synchronously (the
browser picks the ID) and uploads the value in the background; send waits
for the uploads and fails if one failed. A bar above the composer lists the
secrets sealed in this message with **Undo**, which puts the text back,
deletes the stored secret and stops detection from taking that text again.
While any unsealed finding remains, the composer's draft is not saved, so
plaintext does not reach drafts on the server.

## Transport and storage

`POST /api/secrets {id, kind, name, value, conversation_id}` is the only way
in; the request body is never logged (Shelley's request log has no bodies).
The chat endpoints only ever carry the placeholder.

Table `secrets` (migration `069-secrets.sql`, separate from conversation
tables): id, name, kind, the AES-256-GCM sealed value (the id is the
additional data, binding a ciphertext to its row), the source conversation,
created time. `secret_injections` records each time the tool wrote a
secret: conversation, machine, path, time. The key is `secrets.key` (32
random bytes, mode 0600) beside the database, created on first use, so a
copy of the database alone does not hold any secret.

Management API: `GET /api/secrets` (no values), `PATCH /api/secrets/ID
{name}`, `DELETE /api/secrets/ID`, `POST /api/secrets/ID/reveal`. Only
capture (POST) and the tool are behind the flag: secrets already stored stay
manageable, rendered and redacted when it is turned off.

## Tool access

`write_secret {id, path, ssh_target}` writes a secret's value to a file with
mode 0600 (directories 0700), relative paths resolving against the working
directory of the chosen machine: this host, or the task's VM over ssh, the
value going over the ssh connection's stdin, never argv. The result names
the path and size, not the value. For an environment variable the agent
writes `TOKEN=$(cat path) cmd`.

**Redaction.** Every tool result (text, error, display data and streamed
progress) and every background job notice passes through the store's
redactor, which replaces any stored secret's value with its placeholder
before it is recorded, shown or sent to the model. This covers an agent
that `cat`s the injected file, but only for verbatim copies (not base64,
not split across lines).

**Future:** a secret recognized as, say, an API key may move into an exe.dev
integration and be deleted here (`POST /api/secrets/ID/move`, a disabled
button on the management page today).

## UI

In the transcript, `{{secret:ID}}` renders as a pill: a lock, the ID, and
blurred filler derived from the ID (never the value). Hovering says what it
is; clicking opens `/secrets#ID`, the management page: name (rename),
kind, created time, source conversation, where it was injected, and reveal
and copy (after a confirmation), delete.

## Threat model

This keeps secrets out of the model's context, the provider's logs, the
conversation's stored messages, drafts, exports and Shelley's own logs.
It does not protect against anyone with this VM: the key sits beside the
database, and any process (including the agent's commands) can read an
injected file. An agent can still leak a value it has read: the redaction
pass catches verbatim echoes in tool output, not transformed ones, and does
nothing about a command that sends the value somewhere else. Secrets typed
outside the web composer (texts, channels, the API) are not captured.

## Code map

Detection `ui/src/utils/secretDetect.ts`, with known formats in
`ui/src/generated-secret-rules.ts` (from `cmd/secretrules`); composer
sealing `ui/src/vue/composables/secretSealing.ts` (used by
`MessageInput.vue`); pills `SecretPill.vue` and `markdownRender.ts`; page
`SecretsPage.vue`; tool widget `tools/WriteSecretTool.vue`. Server:
`server/secrets.go` (store, API, flag), `claudetool/secret.go` (tool,
redaction wrapper). Tests: `secretDetect.test.ts`, `secretSealing.test.ts`,
`cmd/secretrules/main_test.go`, `server/secrets_test.go`,
`claudetool/secret_test.go`.
