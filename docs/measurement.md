# Measurement

The journal is a measurement log written after the provider responds. It is not the durable run journal, and it does not compute a savings figure.

Schema for one step: `hypermesh.conductor.step.v0`.
Schema for `conductor summary`: `hypermesh.conductor.efficiency.v0`.

## Step fields

| Field | Meaning |
| --- | --- |
| `task_id`, `step_id` | Stable ids for the task and the step |
| `job_class`, `tier`, `model_id`, `reason` | Routing preview |
| `input_tokens`, `output_tokens` | Provider prompt and completion tokens |
| `cache_hit_tokens` | Provider-reported prefix cache tokens, or null |
| `latency_ms` | Provider round trip |
| `prefix_sha256` | Canonical system prompt and tool definitions |
| `peak_context_tokens` | Input tokens for this step, or null when usage is uncertain |
| `usage_status` | `measured` or `uncertain` |
| `cost_proxy`, `cost_proxy_label`, `cost_per_step` | Labeled proxy, or null |
| `accuracy` | Always null here |

Cache-hit tokens are read from `usage.prompt_tokens_details.cached_tokens` when that field is present, otherwise from Responses `usage.input_tokens_details.cached_tokens`, otherwise from `usage.prompt_cache_hit_tokens`, otherwise from Anthropic `cache_read_input_tokens`. A missing field stays null. A reported zero stays zero. Anthropic `input_tokens` counts only uncached input, so the journal adds `cache_read_input_tokens` and `cache_creation_input_tokens` to that input total. OpenAI `prompt_tokens` is already the total.

Usage that is missing, partial, negative, or the wrong JSON type is `uncertain`. Token counts and the cost proxy on that row are null.

`cost_proxy_rates` in the endpoints file is optional. Each tier needs `input_per_million`, `output_per_million`, and a `source` string naming where the rate was published. The label is `proxy: tokens × published rate (<source>); not a charge`. With no rates, the proxy stays null. The proxy is not a bill.

## Summary

`conductor summary --journal <file>` prints:

- tokens per task (`input_tokens`, `output_tokens`, `peak_context_tokens`)
- `by_tier.cheap` and `by_tier.frontier` step counts and token totals
- `cache_hit_rate` (cache-hit tokens divided by input tokens) when every step with input tokens reported a cache figure, and no measured step reports more cache-hit tokens than input tokens. One step's extra input cannot hide another step's report.
- `prefix_reuse_rate`, the fraction of steps that share the most common prefix hash, when the task has at least two steps
- `cost_per_step`, the mean labeled proxy, when every step has one
- `accuracy` left null
- `memory.hit_rate` for the note and summary cache, null when nothing was looked up

When any step in the group is uncertain, the group's `input_tokens`, `output_tokens`, `peak_context_tokens`, `cache_hit_rate`, and `cost_proxy` are null. `measured_input_tokens` and `measured_output_tokens` still hold the sum of the measured rows and are partial. An unused tier has `steps: 0` and null token totals.

A journal line that is not the step schema, or a measured row with null tokens, fails the summary. Rows are not dropped.

## Bench alignment

[hypermesh-bench issue 12](https://github.com/hyperme-sh/hypermesh-bench/issues/12) asks for peak context, cost per step, and accuracy under three context modes. Those fields are `peak_context_tokens`, `cost_per_step`, and:

- `accuracy.full_history`
- `accuracy.end_of_conversation_compaction`
- `accuracy.single_action_compaction`

Accuracy stays null. Compaction-mode scores belong to that bench issue. This summary does not add a speed figure.

`numbers_policy` on every summary repeats the same rule: unmeasured fields stay null, uncertain usage is never recorded as zero, and a cost figure is a labeled proxy or null.

Use `conductor summary --journal journal.jsonl --task "$lease_id"` for one managed Visor lease. The command reads both retained journal segments and reports an error if the task is absent. Each summary block includes the summed `estimated_input_tokens` when every included step has an estimate. Missing estimates leave it null. Compare it with `input_tokens` for complete provider usage, or inspect `measured_input_tokens` and `uncertain_steps` when a step was interrupted.

### Device-bound lease reservations

A single-tenant instance may set CONDUCTOR_ACCOUNTING_URL to its trusted HTTPS
plane base, CONDUCTOR_ACCOUNTING_TENANT and CONDUCTOR_ACCOUNTING_LEASE to the
assigned UUIDs, and CONDUCTOR_ACCOUNTING_TOKEN to its lease owner bearer. Use
this mode only for endpoints served by that lease's host. Authenticate local
clients with CONDUCTOR_PROXY_TOKEN. Each accounted tier needs a total task
limit, counted input and a bounded output. The instance records the original
counts and a fresh attempt UUID in the task SQLite transaction, registers the
hold with the plane and sends X-Hypermesh-Token-Attempt to the inference adapter.
An ambiguous registration keeps its local hold and prevents inference dispatch.
Reconcile that attempt before retrying. The plane credential is separate from
the inference credential. The host adapter owns final usage submission.

### Recipe snapshots and reload (#16)

The server exposes authenticated `GET /v1/config` and `POST /v1/config/reload` for
clients holding `X-Conductor-Token`. The effective view returns model, recipe
hash, context/output/task limits only. Reload validates the file and atomically
publishes a new immutable snapshot. Invalid input retains the last good revision;
in-flight requests retain their original snapshot. Existing tasks retain their
ledger recipe revision and cannot silently switch budgets. Endpoint, listener,
review, notes, and policy changes require restart rather than partial reload.

`reasoning_tokens` records provider-reported reasoning/thinking output as a
separate nullable subcount. It is never added to output totals or reservations.
A subcount above total output is invalid; an absent field stays null. The parser
accepts OpenAI completion/output detail fields and Anthropic thinking detail fields.

For peers supporting revision-bound token attempts, a tier may declare
`model_revision` as the exact model artifact revision. Conductor includes it in
the reservation and requires an identical acknowledgement before inference.
A mismatch, omission or unsupported peer keeps the hold uncertain and prevents
dispatch. Omitted configuration preserves the legacy wire document; it does not
claim a known model revision. Enable the field only with the corresponding Plane
and host contract support. Runtime revision and model artifact revision are
separate identities. Optional cache/reasoning counts remain input/output subcounts.
