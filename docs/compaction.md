# Tail compaction

The cacheable prefix is the system prompt and the tool definitions. Compaction edits only the tail past that prefix.

`compaction.max_tail_tokens` in the endpoints file is the threshold. `0` leaves the tail unchanged. The size check is a local estimate (about four characters to a token), not a provider count. `compaction.recent_messages` keeps that many tail messages after the summary. A tool call and its results stay on the same side of that cut. An empty summary leaves the original tail in place.

When the tail is over the threshold, Conductor asks the cheap tier to summarize the older tail as goals, steps, facts, and notes. The original request is reviewed by the AIuditor hop first. The summarization call is reviewed too, and it uses the cheap tier's protocol and the same credential rules as any other request, including `X-Conductor-Token` when an environment key would be attached. The summary replaces that older tail only after `history.jsonl` accepts the original tail. That file is readable by its owner only. If that write fails, the original request is forwarded. Leading system messages keep the bytes they arrived with, so a prefix cache can still match. The metrics journal records a `summarize` step for the compaction call and does not store the tail text.

The forwarded request is routed with two signals:

- peak context, from the same estimate
- prefix-cache match, when the canonical system and tools equal the configured prefix

A written rule may escalate a class when the estimate is above `escalate_when_peak_tokens_above`. Without that rule, a large tail is compacted and stays on its policy tier. A prefix match may propose the cheap tier. Routing still rejects a proposal that would change the written tier.

Responses API string inputs are not compacted in this slice. Chat completions and Anthropic messages are.
