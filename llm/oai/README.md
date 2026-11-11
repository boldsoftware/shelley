# Reasoning rules and tests

`reasoning_replay.go` holds the outgoing Chat Completions reasoning policy;
`chat_request.go` applies it. Ordinary requests use the SDK serializer. Only
requests needing an explicit empty `reasoning_content` use Shelley's serializer.
Stored history and returned thinking are never changed.

`reasoning_policy_test.go` checks serialized requests for each route.
`reasoning_live_test.go` checks live endpoints: raw omitted/null/empty/space/text
probes, then adapter tool loops and model switches, with and without streaming.

Live tests require `SHELLEY_OAI_LIVE_TESTS=1`; each provider also needs its key:

```sh
SHELLEY_OAI_LIVE_TESTS=1 go test -count=1 -v ./llm/oai -run '^TestLiveReasoning$'
```

| Variable | Routes |
| --- | --- |
| `FIREWORKS_API_KEY` | Fireworks GLM-5.3, DeepSeek V4.1 Flash, Kimi K3 |
| `OPENAI_API_KEY` | OpenAI GPT-6 Luna, reasoning effort `none` (required for Chat Completions tools) |
| `DEEPSEEK_API_KEY` | DeepSeek Flash |
| `ZAI_API_KEY` | Z.ai GLM-5.3 |
| `MOONSHOT_API_KEY` | Moonshot Kimi K3 |

For low-rate accounts, run one case at a time, e.g.
`-run '^TestLiveReasoning$/^moonshot$/^wire$/^empty$'`. A 429 is an account
limit, not a field rejection.

When adding a rule, add a serialized-request case and a live case. One
provider's acceptance does not establish another's. DeepSeek needs the field on
tool-call history even with replay disabled; never collapse its `""` into
omission or null.

## OpenRouter Responses

Use the Responses API format, endpoint `https://openrouter.ai/api/v1`, and model
`google/gemini-4-carbon`. Leave reasoning replay on `auto`. The adapter preserves
each reasoning item's `id`, `format`, `encrypted_content`, and summary through
stored message JSON and tool-result replay. Provider-formatted reasoning is not
replayed to native OpenAI or other non-OpenRouter routes.

Gemini length stops resume only with a nonempty `gemini-continuation` checkpoint
and output budget remaining. The adapter resends the original input plus the
latest checkpoint, with the same cumulative `max_output_tokens`; it never raises
the configured budget. Completed checkpoints and content-filter stops do not
resume. Repeated checkpoints, missing output usage, or more than 16 hops fail.
Interrupted-stream checkpoint recovery is not implemented.

Billable input, output, and cost are summed across hops. Context-window checks
use the first hop's input plus all output, rather than counting the prompt again
on every hop. OpenRouter's `usage.cost` is used when no gateway cost header exists.

Protocol: https://openrouter.ai/docs/cookbook/evaluate-and-optimize/model-migrations/gemini-4#responses-api

Run the offline JSON/SSE, persistence, origin-isolation, and continuation tests:

```sh
go test ./llm/oai -run '^TestOpenRouterResponses' -count=1
```
