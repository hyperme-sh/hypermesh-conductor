# Router policy contract (#45)

Conductor owns classification, provider selection, effort, privacy, prompt assembly
and bounded retries. The gateway owns Hypermesh lease authorization, host routing
and usage submission. Direct third-party requests stay local to Conductor and
that provider; central gateway enforcement is an explicit organization setting.

One versioned policy specifies allowed model revisions, required capabilities,
locality, effort ceilings, task limits and fallback order. Provider adapters
translate only validated settings. A classifier or dream proposal cannot increase
these limits or weaken AIuditor. Existing owners are [routing](routing.md),
[reasoning](reasoning.md), [measurement](measurement.md) and [rules](rules.md).

Reserve rendered input plus bounded output before each attempt. Token caps use
the existing atomic task ledger. A money cap requires authoritative prices and an
atomic accounting hold in the specified currency/unit; a cost proxy cannot enforce
it. Missing prices or an ambiguous hold fail closed for a requested hard money cap.
Cache and reasoning counts are subcounts, never additional charges. Replays reuse
attempt identity; actual extra inference needs a new hold. Interruptions retain
uncertain usage. Gateway checks cannot replace local admission on direct routes.

Proposed sticky routing stores a bounded task-scoped key: tenant, task, policy
revision, exact model/runtime revision, provider, region and stable prefix hash.
No prompt or key goes in the affinity store. Revalidate permission, capability,
locality and remaining budget each turn before considering affinity. Prefer the
previous destination only among eligible candidates. Expire affinity at task end,
policy/model changes or a bounded idle timeout; a prefix change starts a new key.
A deny, cap miss, capability change, unavailability or authorized refusal fallback
breaks stickiness. Never retry visible output or duplicate an uncertain attempt.
This is an affinity design, not a shipped multi-host load balancer.

Fallback order is written policy. Filter candidates before ranking by cost or
cache affinity. Transport failures require an idempotency/dispatch determination;
unknown dispatch does not authorize an automatic replay. Capability refusal happens
before upload. Provider refusal requires AIuditor's explicit allowed candidate
list. Every additional attempt consumes its own admission and records its parent.

A bounded route explanation contains policy revision, job class, selected model,
destination kind/host, capability result, budget outcome, affinity hit/miss reason,
fallback trigger and attempt identifiers. It excludes bodies, credentials and raw
provider error text. Unit/recording-stub contracts cover competing admission,
uncertain replay, cache affinity invalidation, no eligible candidate, denied
fallback, wrong-origin credentials and provider field translation. Bench owns
quality/token comparisons; scheduled main CI owns process scenarios.

Reuse follows the issue's constraints: reimplement patterns by default. If code is
later selected, retain the applicable attribution and notices. LiteLLM's
[non-enterprise code](https://github.com/BerriAI/litellm/blob/main/LICENSE) and
[Portkey's open gateway](https://github.com/Portkey-AI/gateway/blob/main/LICENSE)
are the MIT sources named by the issue; never copy LiteLLM enterprise code.
[RouteLLM](https://github.com/lm-sys/RouteLLM/blob/main/LICENSE) reuse must preserve
Apache-2.0 notices. OpenRouter is public behavioral inspiration only, with no
product code copied. No additional gateway dependency is introduced.
