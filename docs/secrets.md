# Secrets: extracted by the server, stored for a day, read by the agent

Behind the `secrets` feature flag (on by default). When the chat API
(`POST /api/conversations/new`, `POST /api/conversation/ID/chat`) or a
channel (`server/channels.go`: texts, web chats, Slack) delivers a message
from the user, the server takes each secret out of it, stores it in
Shelley's secret store for 24 hours, and leaves a marker in its place:

    [Secret extracted by Shelley; shelley secret ID prints the path of a file holding it]

The model only ever sees the marker. When a command needs the value, the
agent runs `shelley secret ID`, which writes it to a new temporary file
(mode 0600) and prints the file's path.

## Syntax

- The marker above is what messages hold and the model sees. ID is
  lowercase letters, digits and dashes, e.g. `github-pat-k3f9`: the kind of
  secret plus four random characters, so it is informative to the model and
  not reused. Messages from before this design hold
  `{{secret:ID}}` placeholders, which render the same way.
- `{{secret=VALUE}}` or `{{secret:NAME=VALUE}}` in a message marks VALUE as
  a secret by hand (the escape hatch for anything detection misses); with a
  NAME, the ID is NAME if it's free. A marked value shorter than 6 bytes
  fails the message: redaction would find it all over ordinary output.

## Extraction (server side, `secretdetect/`)

The server runs detection on the message as sent and stores each new
finding, sealed, with the conversation it came from and an expiry 24 hours
out; a value repeated within the message gets one ID. Values it already
stores, found or not, get their existing marker (and keep their ID and
expiry). Of overlapping spans the longest wins, so a stored value keeps a
token inside it and a new secret a stored value. `/compact` instructions
are extracted the same way.

Known formats come from [betterleaks](https://github.com/betterleaks/betterleaks)'
default rules (MIT; gitleaks' successor, by its author): `go run
./cmd/secretrules -o secretdetect/generated_rules.go` fetches a pinned
release's `config/betterleaks.toml`; its regular expressions are already
Go's. Each rule keeps betterleaks' keywords (a rule runs only on text
containing one) and capture group (only the secret is taken, not
`API_KEY=`). Its filter, an Expr expression, is translated term by term: an
entropy threshold; `tokenRatio` (a value of words, counted in BPE tokens)
as `wordy`, which counts vowel-rich letter runs instead; `matchesAny` and
`containsAny` on the secret or match as allowlists (e.g. AWS's `…EXAMPLE`
key), and negated as patterns the secret must match. Terms on file paths
are dropped; any other term fails the generation, so a release with new
filter shapes needs a look. Left out: the generic rules (`generic-api-key`,
`-password`, `-username`, `-credential-uri`), which flag any `key = random`
assignment, rules whose filters read the text around the match,
component-only rules (`skipReport`: IDs, hosts and URLs that complete
another rule's finding; the primary rule is kept on its own), and rules
scoped to file paths. Bump the version in `cmd/secretrules` to update.

Our own rules add what betterleaks lacks: a private key pasted without its END
line, PuTTY `.ppk` keys, exe.dev tokens (`exe0.PAYLOAD.SIGBLOB` with an
SSHSIG signature, and `exe1.` with 20 to 40 base32 characters, at least one
a digit so all-letter placeholders aren't taken), and Luma's keys. Luma AI keys start `luma-api-`
(Agents API) or `luma-` (Dream Machine); Luma events keys (luma.com) have no
documented format, so they are taken only as the `x-luma-api-key` header or
a `LUMA*_API_KEY` value, or as a `secret-…` value in a paragraph mentioning
Luma. They also add providers betterleaks misses: Gemini's `AQ.` keys and
Google keys in URLs (`?key=AIza…&`), where betterleaks wants a quote or
space after; Claude Code's OAuth tokens (`sk-ant-oat01-`), Tailscale keys,
Jina and Fireworks keys; ElevenLabs, Cursor, Resend and Telegram bot keys and
Discord webhook URLs, which betterleaks takes only beside the provider's
name; the password alone in a Postgres, MySQL, Redis or AMQP URL (not a
placeholder or a word); and, only beside their names, AWS secret access
keys, Railway tokens (UUIDs) and Modal token secrets (`as-`, in a paragraph
mentioning Modal). Provider rules run before betterleaks' generic ones
(`curl-auth-header`), so they keep their kind. A key of 32 or more letters and
digits beside a name that says it's secret (`API_KEY=`, `"client_secret":`,
`DB_PASSWORD=`, any case) is taken even when it is hex or one case. A generic
rule takes long (≥32) tokens, after any `NAME=`, with
high entropy that mix upper case, lower case and digits, and are not hex,
UUIDs, URLs, paths, or names with many dashes and underscores (base64url
has about one in 32 characters; more than one in 12 is a name). False negatives are preferred over mangling normal
text, and the manual syntax above covers the rest.

## Composer

The composer asks `POST /api/secrets/detect {text}` (debounced) which spans
the server would take out (the same spans extraction uses), in UTF-16
offsets, and lists what detection found (not the user's own marks or values
already stored, which go regardless) in a row above the input ("Captured
on send"); clicking one selects it in the text.
"Don't capture" sends the message with `keep_found_secrets: true`: the
server then leaves what detection finds as typed (values already stored and
manual marks are still extracted). The composer sets it only while every
span found in exactly the text being sent was dismissed, so a secret typed
afterwards is extracted with the rest. The draft (local storage and the
server) keeps typing at once, but other text (pasted, dropped, restored,
added by the bookmarklet, or typed or filled in with a run of 12 or more
characters without spaces) only once the server has checked it, and
nothing while its last check found anything. Until then the tab keeps the
text in memory, so it is still there on coming back to the conversation,
but not after a reload.

A channel's sender sees no errors, so there a manual mark that can't be
stored (too short or long) is left as it is, rather than failing the
message; attachments and the channel's tags aren't searched.

## Transport and storage

The chat request carries the plaintext to the server, which never logs
request bodies; what it records, shows and sends to the model has the
marker. Table `secrets` (migrations `069-secrets.sql`,
`088-secret-expiry.sql`, separate from conversation tables): id, name,
kind, the AES-256-GCM sealed value (the id is the additional data, binding
a ciphertext to its row), the source conversation, created and expiry
times. Expired rows are deleted before secrets are read, listed or used for
redaction. The key
is `secrets.key` (32 random bytes, mode 0600) beside the database, created
on first use, so a copy of the database alone does not hold any secret.

API: `GET /api/secrets` (no values), `POST /api/secrets/detect`, `DELETE
/api/secrets/ID`, `POST /api/secrets/ID/reveal` (the modal's Show and
Copy, and `shelley secret`).
Only extraction and detection are behind the flag: secrets already stored
stay readable, manageable, rendered and redacted when it is turned off.

## Agent access

`shelley secret ID` (`client/secret.go`) talks to the server over
`$SHELLEY_SOCKET`, which Shelley sets for the commands it runs, so it works
on the machine Shelley runs on; a task's VM over ssh doesn't have the
socket. For an environment variable the agent writes `TOKEN=$(cat "$(shelley
secret ID)") cmd`. An expired or deleted secret fails with an error.

**Redaction.** Every tool result (text, error, display data and streamed
progress) and every background job notice passes through the store's
redactor, which replaces any stored secret's value with its marker before
it is recorded, shown or sent to the model. This covers an agent that
`cat`s the file, but only for verbatim copies (not base64, not split across
lines), and only until the secret expires.

## UI

In the transcript, a marker (or a legacy placeholder) renders as a pill,
in code too: a lock, the ID, and blurred filler derived from the ID (never
the value). Clicking it opens the secrets modal (also in the command
palette) on its row, or says the secret expired or was deleted. The modal
is a table: each secret's ID, the conversation it came from, when it
expires, and **Show** (the value, in a row below), **Copy** and
**Delete** (which asks first).

## Threat model

This keeps secrets out of the model's context, the provider's logs, the
conversation's stored messages, exports and Shelley's own logs, and pasted
ones out of drafts (a typed one can reach a draft before the server finds
it).
It does not protect against anyone with this VM: the key sits beside the
database, and any process (including the agent's commands) can read a
secret with `shelley secret` while it lasts. An agent can still leak a
value it has read: the redaction pass catches verbatim echoes in tool
output, not transformed ones, and does nothing about a command that sends
the value somewhere else. Other text the agent reads (files, web pages,
Slack it searches) is not extracted; only values already stored are
redacted from tool output.

## Code map

Detection `secretdetect/`, with known formats in
`secretdetect/generated_rules.go` (from `cmd/secretrules`); store, API and
extraction `server/secrets.go`; CLI `client/secret.go`; redaction wrapper
`claudetool/secret.go`. UI: composer `vue/composables/secretPreview.ts`
(used by `MessageInput.vue`); pills `SecretPill.vue`, `markdownRender.ts`
and `utils/secretMarkers.ts`; modal `SecretsModal.vue`. Tests:
`secretdetect/*_test.go`, `cmd/secretrules/main_test.go`,
`server/secrets_test.go`, `client/secret_test.go`,
`claudetool/secret_test.go`, `test/e2e_secrets_test.go`.
