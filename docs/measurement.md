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

Cache-hit tokens are read from `usage.prompt_tokens_details.cached_tokens` when that field is present, otherwise from `usage.prompt_cache_hit_tokens`. A missing field stays null. A reported zero stays zero.

Usage that is missing, partial, negative, or the wrong JSON type is `uncertain`. Token counts and the cost proxy on that row are null.

`cost_proxy_rates` in the endpoints file is optional. Each tier needs `input_per_million`, `output_per_million`, and a `source` string naming where the rate was published. The label is `proxy: tokens × published rate (<source>); not a charge`. With no rates, the proxy stays null. The proxy is not a bill.

## Summary

`conductor summary --journal <file>` prints:

- tokens per task (`input_tokens`, `output_tokens`, `peak_context_tokens`)
- `by_tier.cheap` and `by_tier.frontier` step counts and token totals
- `cache_hit_rate` (cache-hit tokens divided by input tokens) when every step with input tokens reported a cache figure
- `prefix_reuse_rate`, the fraction of steps that share the most common prefix hash, when the task has at least two steps
- `cost_per_step`, the mean labeled proxy, when every step has one
- `accuracy` left null

When any step in the group is uncertain, the group's `input_tokens`, `output_tokens`, `peak_context_tokens`, `cache_hit_rate`, and `cost_proxy` are null. `measured_input_tokens` and `measured_output_tokens` still hold the sum of the measured rows and are partial. An unused tier has `steps: 0` and null token totals.

A journal line that is not the step schema, or a measured row with null tokens, fails the summary. Rows are not dropped.

## Bench alignment

[hypermesh-bench issue 12](https://github.com/hyperme-sh/hypermesh-bench/issues/12) asks for peak context, cost per step, and accuracy under three context modes. Those fields are `peak_context_tokens`, `cost_per_step`, and:

- `accuracy.full_history`
- `accuracy.end_of_conversation_compaction`
- `accuracy.single_action_compaction`

Accuracy stays null. Compaction-mode scores belong to that bench issue. This summary does not add a speed figure.

`numbers_policy` on every summary repeats the same rule: unmeasured fields stay null, uncertain usage is never recorded as zero, and a cost figure is a labeled proxy or null.
