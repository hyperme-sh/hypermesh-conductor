# Production model review

Configured `aiuditor.mode: "http"` uses the AIuditor service contract at
`/api/v1/aiuditor/review`. HTTPS with a trusted server certificate is required, including on loopback.
The service credential comes only from `CONDUCTOR_AIUDITOR_TOKEN` (at least 32
characters); `CONDUCTOR_AIUDITOR_TENANT_ID` is its nonzero tenant UUID. These must
match the service's dedicated Conductor credential and tenant. They are never
stored in endpoint JSON, inherited from caller Authorization, or forwarded to
model providers. Authentication, schema and service failures always refuse
execution, even when an older config says `fail_closed: false`.

Each configured tier supplies a `policy_provider` identifier and `locality`
(`local` or `remote`). A validated capability record may supply locality instead;
conflicting declarations are refused. Gateway tiers always identify as provider
`hypermesh`. Protocol names such as `openai-compatible` never imply vendor or
locality. Missing metadata refuses production review. Captured direct endpoints
need the same metadata and remain pinned to their selected endpoint.

Example authority block:

```json
{"aiuditor":{"mode":"http","url":"https://authority.example/api/v1/aiuditor/review","fail_closed":true}}
```

A normal call first sends a correlated request with the chosen model and ordered
configured candidates, chosen first. AIuditor's model/provider selectors retain
the chosen model if permitted, otherwise select the first permitted candidate.
The selected route then passes the normal capability, locality, task/output
budget, credential-origin and protocol checks. A route cannot weaken the
original hard budget, required capabilities or output cap. The final payload is
reviewed again for that selected model; a changed rule fails closed rather than
causing an unbounded routing loop. Local-only requests cannot contact a remote
review authority. Calls on a captured endpoint or an already authorized refusal
fallback do not broaden into the other tier.

Compaction is pinned to the configured cheap tier; OCR is pinned to its explicitly
selected verified endpoint. Both send the same structured service contract and
honor deny/modify constraints. Upstream refusals identify the refused model and
send only the eligible remaining fallback. The `allowed_models` set must agree
with the returned route constraint. Existing attempt limits and the no-retry-after-
content rule remain enforced. Refusal text is reduced to a fixed category.

The in-process stub and direct `aiuditor.HTTP` adapter remain explicit unit/legacy
fixtures. The CLI and a proxy built from HTTP configuration select
`ProductionHTTP`; neither silently substitutes those fixtures for the service.
This model-review contract is separate from signed machine capability review.
