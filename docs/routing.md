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

`examples/endpoints.json` names an API root and a model id for each tier. `provider` is `openai`, `anthropic`, or `openai-compatible`. `base_url` is the API root, such as `http://127.0.0.1:8080/v1` or `https://api.anthropic.com/v1`. Keys are not written in the file. `api_key_env` names the environment variable that holds a key. A URL with userinfo is rejected. Either `tiers.cheap` or `tiers.frontier` may be omitted. A tier key that is present must be a valid tier object. At least one of those two tiers is required. A job class whose tier was omitted is rejected.

## Prefix

The system prompt and tool definitions are canonicalized (sorted tool names and sorted JSON keys) and hashed. When a client omits a system prompt, the configured prefix is inserted and the hash matches that prefix. When a client sends its own system prompt, that text is the prefix that is hashed. Step text stays outside the hash. Tail compaction is not implemented.

## Proxy

`conductor serve` binds the `listen` address. The default is `127.0.0.1:8787`, and the default `listen_mode` is `loopback`. The paths are `/v1/chat/completions`, `/v1/responses`, `/v1/embeddings`, `/v1/models`, and `/v1/messages`.

`hyperme-sh/controlplane-edge` `src/controlplane_edge/product_routes.json` lists no conductor product and no hostname. Operators who need reachability beyond the host terminate TLS on their own reverse proxy and forward to the loopback listener. `listen_mode: network` is the in-process alternative: `listen` may be a non-loopback `host:port`, `tls.cert_file` and `tls.key_file` are required, and `CONDUCTOR_PROXY_TOKEN` must be set or `serve` exits. Callers send that token as `X-Conductor-Token` on every request. The key material stays in those files.

`X-Conductor-Class` or `conductor_class` selects the job class. `X-Conductor-Suggest` is a suggestion. `X-Conductor-Task` and `X-Conductor-Step` are optional stable ids. Conductor fields are removed before the destination sees the body. With no class and no `default_class`, Conductor logs a fallback to `parse` on the cheap tier, or to `code` when the file configures only the frontier tier. HTTP 400 `no tiers are configured` is reserved for a process with neither tier. A class that selects an omitted tier returns HTTP 400 `tier cheap is not configured` or `tier frontier is not configured`. The policy model replaces the client's model. Clients that require a specific destination model can send its exact ID in `X-Conductor-Expected-Model`. A mismatch returns HTTP 409 before review, compaction, or provider dispatch.

A tier with `"gateway": true` is an OpenAI-compatible Hypermesh gateway. Its `model` must be a catalog id the gateway matches to the lease ticket (`llama-3.1-8b-q4`, `llama-3.1-70b-q4`, `qwen-2.5-32b-q4`, `whisper-small`, `embed-minilm`), or `catalog_model` must name one of those ids. That catalog id is what replaces the client's model. Conductor forwards `X-Api-Key` or `Authorization`, and forwards `X-Tenant-ID`, `X-Hypermesh-Lease-Id`, and `X-Lease-Id`. It sends one of `Authorization` or `X-Api-Key`, because the gateway rejects both. JSON `lease_id` is kept on chat and responses bodies. Anthropic messages does not accept that field, so a gateway tier also receives it as `X-Hypermesh-Lease-Id`. Values that start with `hm_dev_`, `hm_rtr_`, `hm_site_`, or `hm_gw_` are refused and are not sent as renter identity.

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

## Single-hop prompt submission

An operator can configure a destination for an explicit submission with a routes file:

```json
{"lab":{"origin":"https://lab.example","allowed_classes":["summarize"],"token_env":"LAB_PROMPT_GRANT"}}
```

Run `conductor submit -routes routes.json -destination lab -class summarize` and supply one user prompt on stdin. Selecting the class declares the intended job; the operator should grant it only to callers allowed to send the supplied text. This CLI reads its caller's routes file and is intended for a trusted local operator.

The client POSTs a JSON object containing `destination`, `class` and `prompt` to `/v1/peer/prompt-submit` using TLS 1.3 and the configured bearer credential. Provision a dedicated grant for that destination. Its ingress must authenticate the caller, bind the grant to its own destination and permitted classes, and authorize dispatch. Enable the receiver with `conductor serve -config endpoints.json -peer-grant grant.json`. Its grant file contains `{"destination":"lab","allowed_classes":["summarize"],"token_env":"INBOUND_SUMMARY_GRANT"}`. Provision the matching secret independently of the local proxy token. Expose only the peer POST path through an operator-managed TLS ingress to the loopback listener. The destination constructs a fresh local inference request, applies its own routing and review, and excludes local note retrieval. Omitting the grant leaves the peer path disabled. The client sends no conversation history, tools or local provider keys and rejects redirects. This path is independent of automatic tier routing.

The receiver reads the grant file for every peer submission. Remove it to revoke access immediately for subsequent requests. To change the authorized destination, classes or credential reference, write a complete replacement and rename it over the configured path. Missing or invalid grants deny dispatch. Requests already admitted may finish.

## Revision-bound capabilities

A tier may declare `runtime_revision` and a `capabilities` object containing
`model`, `runtime_revision`, `protocol` (the tier provider), `local`, and
`features`. Each feature is `verified`, `unknown`, or `unsupported`; absent
features are unknown. Model, runtime, and protocol must match at configuration
load and dispatch, so changing either revision invalidates the record.

Features are `chat`, `streaming`, `tools`, `structured_output`, `image`,
`embeddings`, and `ocr`. The proxy derives requirements from structured request
fields. A configured record must verify every requested feature before dispatch.
`X-Conductor-Local-Only: true` requires a local record;
`X-Conductor-Require-Capabilities: true` requires a record even on legacy tiers.
Legacy requests without either header remain compatible on unqualified tiers;
that compatibility path does not claim verified capability support.

## Direct provider credentials (#37)

`direct: true` explicitly identifies a provider tier and conflicts with
`gateway: true`. For passthrough credentials, configure `credential_origin` as
the exact scheme and authority of `base_url`, without a path. Capturing clients
send `X-Conductor-Credential-Origin` from their original configured endpoint.
Conductor rejects a missing or different origin before sending credentials.
Environment-authenticated tiers use only their configured environment key.
Legacy tiers remain compatible; clients can send the origin header to enforce
binding there as well. An opaque provider key cannot independently prove origin.

`gateway_enforcement: true` refuses any configured non-gateway tier. It defaults
off. Journal rows include `destination` (`direct` or `gateway`) and `provider_host`
without credentials or prompt text. CLI capture configuration remains owned by
hypermesh-cli; this contract does not claim that its route command has migrated.

## Refusal fallback (#36)

Set `refusal_fallback_tier` to an explicitly permitted alternate tier and
`max_refusal_attempts` (default 2, range 1–8). Without a written fallback, a refusal
is returned unchanged. Structured refusal/content-filter responses and policy
error codes trigger an `upstream_refusal` AIuditor event. Only a `reroute` verdict
whose `allowed_models` contains that candidate permits another attempt. Ordinary
allow does not authorize rerouting. A natural-language refusal classifier is off.

Streaming buffers a bounded prefix until text/tool content or a refusal appears.
After visible content, no reroute occurs. A retry uses a distinct step, reserves
again under the same task, and cannot escape its existing task limits. Missing
usage remains uncertain. Credentials stay bound to the original origin unless
the new tier explicitly uses its own environment credential. The response header
`X-Conductor-Attempted-Models` records tried models. The second request is reviewed
again, including protocol translation and capability/locality checks.

### Captured provider profiles (#37)

`capture_dir` explicitly enables a local directory of JSON profiles written by
`hypermesh route enable`. Each filename is a tool ID (for example `codex.json`);
symlinks, invalid IDs and more than 64 profiles are rejected. Profiles load at
startup and changing them requires restart. Unknown profiles never proxy traffic.

```json
{
  "schema": "hypermesh.conductor.capture.v1",
  "provider": "openai-compatible",
  "base_url": "https://provider.example/v1",
  "model": "$client",
  "direct": true,
  "auth": "passthrough",
  "credential_origin": "https://provider.example"
}
```

Clients use `/capture/codex/v1/chat/completions` or the corresponding Responses,
Anthropic or embeddings path. This ingress binds the credential to the configured
origin; a conflicting client binding fails before dispatch. `$client` preserves
the caller's model. Classification and AIuditor review still apply. Frontier work
uses the captured provider; a policy-selected cheap route requires its own
environment credential. A gateway receives only its configured Hypermesh key.
Network clients still require `X-Conductor-Token`; capture never bypasses listener
authentication or browser-origin checks. `gateway_enforcement` rejects captures.

Existing recipe limits cannot disappear: the captured model and counter must
match the active frontier recipe, whose limits and revision carry across. Unknown
capabilities still fail explicit capability/locality requirements. Provider models
do not inherit another model's published cost proxy. Refusal fallback to a written
cheap tier is supported; fallback from cheap to a dynamically captured frontier
is refused because that candidate is not the configured frontier model reviewed
by AIuditor. Credentials remain bound across every attempt.

The CLI preserves credential selectors in the tool config and backs up both the
tool and profile files. It does not store keys in these profiles or install a CA.
