# Scoped capability notes

Conductor #51 extends the existing linked Markdown collection and derived SQLite
index for Visor #99. No second source store, executor or network endpoint is added.
Deleting and rebuilding the index reproduces the catalog from the notes.

Use explicit frontmatter `id` and `type: capability`, with one fenced
`capability-json` block in the body. JSON gives the typed payload a strict parser
without changing the existing frontmatter parser. IDs must match; filenames may
change. Relations also require `[[stable-id]]` wikilinks, so ordinary links and
backlinks remain available in the existing index. Titles are not identities.
See the [read-only network](examples/capability-hosts/summarize-counters.md) and
[simulated arm](examples/capability-hosts/move-to.md) examples and their linked
nouns. Their limit hashes are deliberately fake fixture values.

Schema 1 includes:

| Field | Contract |
| --- | --- |
| `id`, `kind`, `class` | `noun/...` or `verb/...`; nouns require a class and cannot contain verb fields. |
| `scope`, `owner`, `revision` | Explicit `org/<id>/site/<id>`, stable owner ID, and revision. A reference cannot cross scope or owner. |
| `uses`, `requires`, `observed_by` | Stable IDs with exact revisions. Uses/observed-by target nouns. Missing references, cycles, excessive depth and stale revisions are refused. |
| `limits` | Required immutable references to controller/policy definitions, each bound to a SHA-256 revision. The note does not define or edit their values. |
| `inputs` | At most 32 typed parameters: bounded integer/number, boolean, bounded string with optional enum, or a three-axis bounded vector with a unit. Unknown parameters and wrong types are refused. |
| `preconditions`, `effects`, `locks` | Required bounded lists for verbs; locks are scoped exclusive resource names, acquired in sorted order by the executor. |
| `timeout_ms`, `freshness_ms` | Explicit bounds from 1 to 300000 milliseconds. Inherited bounds can only reduce these budgets. |

Payloads are limited to 64 KiB; catalogs to 256 capability notes. Duplicate JSON
keys (including case aliases), unknown fields and unbounded numeric inputs fail
validation. Numeric integer ranges stay within exact JSON-safe integer bounds.
String lengths count UTF-8 bytes. Relations inherit all limit references, locks
and preconditions; conflicting revisions of one limit are refused. Memoized graph
validation avoids repeatedly expanding shared dependency paths.

## Local projection and proposed changes

```sh
conductor index capabilities --notes docs/examples/capability-hosts \
  --scope org/example/site/lab
conductor index capabilities --notes /private/current-notes \
  --scope org/example/site/lab --proposal /private/draft-notes
```

The first command exports scoped definitions plus source and graph hashes, never
raw note bodies. The second validates a draft and exports it only if existing
contracts stay unchanged or become stricter. Neither command writes source notes
or applies a proposal. Existing `dream` output remains a reviewable Markdown diff.
Use the normal product review process to apply note changes; this adds no GitHub
reviewer requirement. `--db` and `--proposal-db` may select private derived index
paths outside their respective collections under the existing index rules.

A changed definition needs a new revision. A proposal cannot change an existing
capability's scope/owner/kind/class, widen input bounds, remove dependencies or
inherited constraints, extend time budgets, or change its effects. Removing a
capability removes availability; adding a new definition never creates a grant.
Changing physical policy or replacing an immutable limit is outside learned-note
proposals and requires the separate human-owned controller policy process.

These are local operator commands, not a tenant-authenticated discovery API.
The existing note collection token still authorizes its whole configured
collection. Keep private organizations/sites in separately authorized collections;
`--scope` is a projection filter, not proof of authorization. An index warning
blocks capability export until the collection is repaired.

## Call contract for the Visor consumer

`CapabilityCatalog.CheckCall` is a preflight validator, not an action executor.
The request binds scope, owner, verb ID, revision, source hash, graph hash and
arguments. The graph hash binds dependency bytes too: editing a noun without
bumping its revision still invalidates an old verb call. Snapshot/plan values are
copies, so a consumer cannot mutate the catalog through returned data.

The adapter must supply a current trusted `CapabilityAuthority`. It must authorize
the exact call through AIuditor/current RBAC, check live preconditions, and resolve
each immutable limit against the controller's approved registry. Missing authority,
revoked grants, missing limits and failed preconditions refuse the call. Neither
note text nor caller-provided success flags can implement those checks. Observation
time comes from the adapter's sensor state; zero, future and stale times fail.

Use the preflight plan to identify and obtain all locks, then refresh the index
and recheck the same call immediately before dispatch. Reject an expired plan;
enforce its absolute deadline without resetting it, and preserve pause, revocation
and cancellation. The deterministic controller/hardware still enforces
actuator bounds, geofences and e-stop below Visor. This validator cannot turn a
model description into an executable physical limit. No credentials, note bodies
or argument values are included in call-refusal messages.

Visor #99 owns opt-in publication and transport. It must publish only verbs allowed
by the host owner's current grant; the private local catalog is not a public room
layout or network map. No capability is activated by these notes, examples, CLI
commands or unit contracts. ROS, physical execution and simulation batches are
separate work; this schema does not add a ROS dependency.
