# Reasoning policy contract (#41)

A versioned capability map is keyed by exact provider, model revision, and wire
protocol. Records declare accepted efforts, the default, field paths, exclusive
fields, hard-cap support, cache-sensitive settings, and required tool-loop state.
Unknown combinations fail validation rather than receiving a guessed parameter.
The map is distinct from generic OpenAI-compatible endpoint capability records.

| Adapter | Translation contract | History constraints |
| --- | --- | --- |
| OpenAI Responses | `reasoning.effort`, model-specific accepted enum; output ceiling remains `max_output_tokens` | Preserve opaque items needed by tool calls; use supported `reasoning.context` rather than decoding opaque content |
| OpenAI Chat | `reasoning_effort`, only on models supporting that protocol | Do not translate opaque Responses items into fabricated chat text |
| Anthropic adaptive | `thinking.type=adaptive` and model-specific `output_config.effort` | Preserve signatures and required thinking across unfinished tool loops |
| Anthropic legacy manual | `thinking.type=enabled` and `budget_tokens`, less than output ceiling and at least model minimum | A changed budget may invalidate caching |
| Gemini level | Model-specific `thinking_config.thinking_level` | Preserve thought signatures |
| Gemini budget | `thinking_budget` only for models declaring it | Never combine numeric budget and level |
| Qwen | Declared `enable_thinking`, effort or numeric budget | Use `preserve_thinking` only where supported; never drop required tool state |
| DeepSeek | Declared thinking toggle and native effort | Preserve reasoning content across required tool turns |

This table defines adapters to validate, not a claim that every named generation
supports every field. Provider-specific records must cite official documentation
and pin contract cases before activation. OpenAI's [reasoning guide](https://developers.openai.com/api/docs/guides/reasoning)
confirms model-dependent effort values and the need to preserve reasoning items
for tool calls. Unsupported values must not silently escalate to the nearest
higher effort. Each new model revision needs a reviewed capability record.

Written route policy selects a default and maximum effort, organization maximum,
output allowance, and history mode. A classifier may use job class, input size,
retry count and explicit task difficulty to propose a lower effort. Deterministic
validation applies the most restrictive caps; ambiguous work uses the written
default. A classifier cannot widen budget, locality, tools, or AIuditor authority.
Do not automatically raise effort when a provider refuses.

History policy is `keep`, `last_turn`, or `drop_after_accept`. Required signatures
and reasoning inside an unfinished tool loop take precedence over deletion.
Opaque items are preserved byte-for-byte. Only the tail changes; system and tool
prefix bytes remain stable. Cache-sensitive policy changes are explicit recorded
transitions, not incidental rewrites. A provider-native context control is preferred
when it can enforce the chosen policy without manually editing opaque items.

Soft budget prompts are advisory and their estimation calls count against the
task budget. Native caps and output ceilings remain admission requirements.
Early exit needs a local adapter with access to the stream and an explicit tested
stop delimiter; confidence stopping additionally requires probabilities. Hosted
opaque reasoning cannot support that implementation. Forced continuation is off.

Record policy revision, selected effort, thinking enabled, reported thinking
tokens, total input/output, cache hits, retries, and context peak. Unknown values
stay null; reasoning tokens remain an output subcount, never an extra billable
sum. Dream proposals may revise effort/history tables but cannot edit safety or
RBAC. Evaluate against [bench #16](https://github.com/hyperme-sh/hypermesh-bench/issues/16)
and [compaction #12](https://github.com/hyperme-sh/hypermesh-bench/issues/12).
Required stock Qwen3.8-27B comparisons are low, medium, xhigh and non-thinking;
prompt brevity, soft budgets and history modes are labeled ablations. Accuracy,
task success and total tokens across retries determine the result. No Hypermesh
savings figure is available. Training and third-party weight remixes are excluded.
