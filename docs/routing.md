# Tiered routing

Rules choose the model tier for a step. A suggestion may agree with that choice. The recorded tier is the policy tier.

## Job classes

| Class | Default tier | Call |
| --- | --- | --- |
| parse, extract, index, classify, summarize, status | cheap | the tier destination, in the client's protocol |
| embed | cheap | embeddings on an OpenAI-compatible destination |
| plan, architecture, tradeoff, code, review | frontier | the tier destination, in the client's protocol |

`embed` uses the embeddings method on the cheap endpoint. `local` is another name for the cheap tier. `strong` is another name for the frontier tier. Labels such as `status update`, `code review`, and `code generation` map onto the classes above.

A profile file may set `class_tiers` for a known class. That map is policy. A suggestion is not.

## Suggestions

The rank is cheap, then frontier.

- A suggestion equal to the policy tier is accepted.
- A suggestion above the policy tier is rejected. The reason starts with `rejected out-of-policy upgrade`. The cheap or local endpoint still receives the step.
- Any other suggestion is rejected and the policy tier still stands.

## Fallback

A failed call is recorded as uncertain usage on the tier that was selected. The other tier is not called, and the step is not retried.

## Preview fields

`conductor route` prints:

- `job_class`
- `tier`
- `model_id`
- `reason`
- `suggestion_tier` and `suggestion_accepted` (null when no suggestion was offered)
- `prefix_sha256`

The preview does not include the endpoint URL or an API key.

## Endpoints

`examples/endpoints.json` names an API root and a model id for each tier. `provider` is `openai`, `anthropic`, or `openai-compatible`. `base_url` is the API root, such as `http://127.0.0.1:8080/v1` or `https://api.anthropic.com/v1`. Keys are not written in the file. `api_key_env` names the environment variable that holds a key. A URL with userinfo is rejected.

## Prefix

The system prompt and tool definitions are canonicalized (sorted tool names and sorted JSON keys) and hashed. When a client omits a system prompt, the configured prefix is inserted and the hash matches that prefix. When a client sends its own system prompt, that text is the prefix that is hashed. Step text stays outside the hash. Tail compaction is not implemented.

## Proxy

`conductor serve` binds the `listen` address. That address must be loopback. The paths are `/v1/chat/completions`, `/v1/responses`, `/v1/embeddings`, `/v1/models`, and `/v1/messages`.

`X-Conductor-Class` or `conductor_class` selects the job class. `X-Conductor-Suggest` is a suggestion. `X-Conductor-Task` and `X-Conductor-Step` are optional stable ids. Conductor fields are removed before the destination sees the body. The policy model replaces the client's model. Clients that require a specific destination model can send its exact ID in `X-Conductor-Expected-Model`. A mismatch returns HTTP 409 before review, compaction, or provider dispatch.

`auth` on a tier is `passthrough`, `env`, or `passthrough_or_env` (the default). Passthrough forwards the client's `Authorization` header to an OpenAI-compatible destination, or `x-api-key` to Anthropic. It does not forward a client key to the other provider. `env` uses only `api_key_env`. Attaching that key also requires `X-Conductor-Token` to match `CONDUCTOR_PROXY_TOKEN`. A redirect from the destination is not followed, so that credential stays on the configured origin. When the client protocol and the destination protocol differ, Conductor translates text, tool definitions, tool calls, and the common stream events.

`/v1/models` lists the configured tier models. It does not call upstream.

A provider error is journaled as uncertain usage. The other tier is not called.

## Commands

```bash
conductor route --config examples/endpoints.json --class parse
conductor route --config examples/endpoints.json --class parse --suggest frontier
conductor serve --config examples/endpoints.json --journal runs/journal.jsonl
conductor run --config examples/endpoints.json --journal runs/journal.jsonl \
  --task task-1 --step step-1 --class summarize --message "Summarize the note."
conductor summary --journal runs/journal.jsonl
```

The journal directory must exist before `serve` or `run`. Exit status is 0 when the command finishes, 1 when the provider call or a denied hop finishes after a journal row is written, and 2 when the config, class, or journal is not usable.

Journal retention is bounded to the current 32 MiB segment and one previous
segment (`.1`). Summaries read both retained segments; older segments are
replaced on rotation. Archive them externally before rotation if longer history
is required. AIuditor denial explanations are capped at 1,024 bytes.

For chat generation, the recipe's output allowance covers all candidates in the request. With an allowance of 100 tokens and `n: 4`, the default per-candidate cap is 25. If the provider supports `best_of`, Conductor budgets every generated candidate, including candidates the provider discards. An explicit per-candidate limit that exceeds the allowance is rejected before dispatch.

A recipe may set the top-level integer `max_task_output_tokens` to share one output allowance across both tiers. Supply a stable task ID (managed Visor uses the lease UUID). A fresh step ID means a new attempt. Reusing an admitted step returns HTTP 409 before another generation. On restart use the same journal path and recipe. A different recipe digest on a budgeted attempt for the same task returns HTTP 409.

Conductor reserves the capped provider request's aggregate output in `<journal>.output.sqlite` before sending it. A measured response replaces the reservation with the provider's output count. Missing usage, an interrupted response, or a crash retains the reservation. Keep this file with the journal across restarts and rotations. Start a fresh task when enabling the feature on an existing journal; earlier requests have no reservations. Budgeted runs skip model-assisted compaction. The allowance covers generated output; input estimates are reported separately.

### Input counts and context windows

For a vLLM chat deployment, set `"input_counter": "vllm"` on its OpenAI-compatible endpoints-file tier (base URL ending in `/v1`). Set `context_window_tokens` on the matching recipe tier to enforce a window. Use a deployment that routes both chat and the sibling `/tokenize` endpoint to the same model revision, tokenizer and chat template. Confirm this pairing when qualifying a host.

Conductor sends the final model, messages, tool definitions and supported template options to the authenticated tokenizer after review and context assembly. Generation is admitted when counted input plus the capped output **per candidate** fits both the recipe window and the provider's reported `max_model_len`. Compaction generation on a context-limited cheap tier undergoes the same check. An unavailable count, an unsupported protocol or an unrecognized prompt option prevents generation on a hard-window tier. The supported surface is text chat and tool history. With only the counter configured, unavailable counts fall back to estimate-only reporting.

Step receipts distinguish `counted_input_tokens` (preflight tokenizer), `estimated_input_tokens` (heuristic), and `input_tokens` (reported after inference). Summary blocks return a null counted total when any row lacks a preflight count. Context admission applies to each generation; the task-wide output ledger accounts for all candidates and steps.

Set the recipe's top-level `max_task_tokens` to limit combined input and output for a task across tiers. Both tiers must have a vLLM input counter. Supply an explicit task ID and keep the journal and its ledger together on restart. Each generation provisionally charges its counted input once per generated candidate plus its aggregate output cap. A measured receipt replaces both provisional charges with reported usage. OpenAI prompt usage already includes cached input. Uncertain or interrupted attempts keep their full reservation. An unavailable count or an unsupported request prevents generation. When both task limits are present, the ledger enforces them together. Start a fresh task when changing recipes.
