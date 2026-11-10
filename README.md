# Hypermesh Conductor

Budgeted, recoverable AI task runtime for Hypermesh: model and host selection, token and time budgets, bounded agents.

## Status

**Early slice.** Conductor is a localhost proxy written in Go. It classifies a request, asks the AIuditor hop, and forwards it to a cheap or frontier endpoint. Budgets, durable runs, retrieval, planning, the task tracker, and context compaction are still design. Nothing is released.

`go test ./...` runs the tests. The binary is static. Linux `.deb` and `.rpm` packages follow the Visor bundle layout and are not a release. Design notes: [docs/routing.md](docs/routing.md), [docs/measurement.md](docs/measurement.md), [docs/aiuditor.md](docs/aiuditor.md), [docs/compaction.md](docs/compaction.md), [docs/rules.md](docs/rules.md), and [docs/packaging.md](docs/packaging.md).

## What Conductor is

Conductor is the planned runtime that sits between the tools a renter works in and the services that run the model. A renter describes what a task needs and what it may spend. Conductor picks the model and host that fit, enforces the time and token budget, keeps the task recoverable if something fails, and passes every request through AIuditor for permission checks.

Hypermesh is where compute is rented. Visor is where you work. Conductor decides how a task runs and what it may spend. AIuditor decides what the task is allowed to do.

## Why

Today a renter picks a model and a host by hand, watches the spend by hand, and starts over when a session dies. Conductor is meant to move that work into a runtime that is:

- **Budgeted:** a task has a time and token limit that is checked before work is sent, not after.
- **Bounded:** retries, fallbacks, waiting time, child tasks and permissions all have declared limits.
- **Recoverable:** a crash or restart resumes from recorded state and does not charge twice for the same work.

## Architecture

```
Visor / CLI / direct clients
            |
        Conductor        profiles, selection, budgets, durable runs, agents
            |
         AIuditor        grants, approvals, request delivery, audit
            |
     Hypermesh hosts     leased capacity running the models
```

- **Conductor** owns execution profiles, conversations and tasks, agent trees, context checkpoints and runtime budget reservations.
- **AIuditor** supplies grants, contract limits, approvals and audit decisions, and delivers requests under those grants.
- **Hypermesh** owns offers, host placement, leases and usage records. Conductor ranks the acceptable offers and asks Hypermesh to acquire one. It cannot force an unavailable or ineligible host into a lease.
- **Clients** (Visor, the CLI, and direct API clients) talk to Conductor. Existing direct use of Hypermesh is meant to keep working.

Conductor is designed to work without a language model. With rules alone it can choose its first route, restore state, cancel work and enforce limits. Model-assisted steps, such as classification or planning, only propose. Deterministic rules validate them.

## Planned capabilities

**Selection**
- Execution profiles: the model or models a task needs, the hosts it prefers, the substitutions it permits, and what it may spend.
- Deterministic model and host selection by profile: an exact model or an accepted set, a required or preferred host, and bounded waiting and fallback. Every decision records the reason.
- Preview before acquiring anything. A preview is not a promise of capacity.

**Model groups and mixture-of-experts (MoE)**
- Profiles that choose a group of models, or an MoE model, per task, based on capability evidence, host fit and cost.
- Groups of models chosen per task, where one task may use more than one model.

**Local-first model picking**
- Local components in the CLI and Visor can propose or pre-select an efficient model for each action.
- Conductor stays the authority for budgets, allocation and policy. A local suggestion never widens what a task may spend or do.

**Budgets and metering**
- Time budgets built on Hypermesh leases.
- Token budgets with reservations and metering. Token billing depends on certified pricing and metering work that is still in progress, so token budgets are not available yet.
- Usage that is lost or uncertain is tracked as uncertain. It is never reported as free.

**Durable runs**
- A local journal of sessions, runs and attempts, written before work is dispatched.
- Recovery after a crash or restart without charging twice for the same work.
- Inspect, cancel and export.

**Agents and context**
- Bounded subagents and child tasks, with limits on depth, count and concurrency, and a shared budget that children draw from.
- Scoped delegation: a child can have the same permissions as its parent or fewer, never more. Revoking the parent stops its children.
- Context compaction that records what was summarized and keeps the original history.

**Tools**
- A scoped tool bridge to Visor and the workspace, with the existing approval rules. Tool credentials and files stay off the model host.

**External providers**
- The proxy forwards to OpenAI, Anthropic, or any OpenAI-compatible endpoint, including a Hypermesh host or a local model. Client credentials for that provider are passed through. A configured environment variable supplies a key only when the client did not send one, or when the tier is set to `auth: env`. Sending private note bodies to an external provider still needs a separate review.

**Security and safety**
- Policy hooks, defenses against prompt and tool injection, controlled egress, and an audit trail, with integration points to AIuditor.
- Retrieved text and tool output are treated as untrusted data and cannot grant permissions.

## Non-goals

- No custody of funds. Conductor does not hold or move money.
- No bring-your-own-key product and no billing path. Client credentials are forwarded to that client's provider and are kept out of the journal and the default log. A tier may also name an environment variable for a local or hosted key.
- Not a replacement for Hypermesh billing or payment. Conductor reserves and meters against budgets; Hypermesh and its billing services own prices, charges and refunds.
- Not a scheduler for compute. Hypermesh makes the host placement decision.

## Routing and measurement

Conductor listens on `127.0.0.1:8787` and exposes OpenAI-compatible `/v1/chat/completions`, `/v1/responses`, `/v1/embeddings`, and `/v1/models`, plus Anthropic-compatible `/v1/messages`. Streaming responses are server-sent events. A third-party client sets its base URL to this proxy.

Each request is classified, reviewed by the [AIuditor hop](docs/aiuditor.md), then sent to one destination. A failed call stays on that tier.

| Step | Tier |
| --- | --- |
| parse, extract, index, classify, embed, summarize, status | cheap (local is the same tier) |
| plan, architecture, tradeoff, code, review | frontier |

Set the class with `X-Conductor-Class` or a top-level `conductor_class` field (that field is removed before the destination). A suggestion in `X-Conductor-Suggest` that asks for a higher tier is rejected. With no class and no `default_class`, the proxy refuses the call.

When the client omits a system prompt, the configured prefix is inserted. Its canonical hash is stored on the journal row. Stable task and step ids can be sent as `X-Conductor-Task` and `X-Conductor-Step`.

Each row records input tokens, output tokens, tier, model id, latency, and cache-hit tokens when the provider reports them. Missing usage stays null. Prompt bodies and API keys are not written to the journal or the default log. `conductor summary` reports tokens per task, the cheap versus frontier split, and the cache hit rate. Accuracy under compaction modes stays null; those scores belong to [hypermesh-bench#12](https://github.com/hyperme-sh/hypermesh-bench/issues/12). The summary does not publish a savings figure.

```bash
go build -trimpath -ldflags '-s -w' -o conductor ./cmd/conductor
./conductor route --config examples/endpoints.json --class parse
mkdir -p runs
./conductor serve --config examples/endpoints.json --journal runs/journal.jsonl
./conductor summary --journal runs/journal.jsonl
```

`examples/endpoints.json` names loopback and provider placeholders. `serve` and `run` check that the journal directory exists before a provider call. Set `CONDUCTOR_CHEAP_API_KEY` when the cheap tier must use an environment key. Set `CONDUCTOR_PROXY_TOKEN` as well, and send it as `X-Conductor-Token` on any call that should use that key. A client using its own provider credential only needs the proxy token when requesting local note retrieval.

Tails longer than `compaction.max_tail_tokens` are summarized on the cheap tier. The original tail is stored in `history.jsonl` next to the metrics journal. The system prompt and tools stay the prefix that is hashed.

`conductor dream` replays the journal and writes an inactive rule proposal. `conductor rules diff`, `apply`, and `rollback` review that file. Proposals cannot loosen AIuditor rules. Prompt bodies are included only with `dream --bodies`. With `--notes`, dream also writes a markdown diff of proposed links and merges and does not change the notes.

Linked notes live as Markdown under `notes.dir`. `conductor index rebuild` and `conductor index status` maintain a local SQLite index of wikilinks, backlinks, and full text. An authenticated request with `X-Conductor-Token` matching `CONDUCTOR_PROXY_TOKEN` inserts a small neighborhood after the stable prefix. This authorizes the configured local note collection; untrusted callers receive no note IDs, text-search results, or primer. `conductor summary` includes the memory-cache hit rate. See [docs/notes.md](docs/notes.md).

## Contributing

Issues are welcome. Please open an issue to discuss an idea before sending a pull request.

## License

MIT. See [LICENSE](LICENSE).

## Security

Please report security problems to security@hyperme.sh, not in a public issue. Report other bugs to bugs@hyperme.sh. See [SECURITY.md](SECURITY.md).

Notes synchronization accepts at most 10,000 Markdown files, 1 MiB per file,
and 32 MiB of source bytes per sync. Oversized or growing files fail the sync
before replacing the existing index. Named pipes and other nonregular files
are excluded, and rooted reads cannot follow paths outside the notes directory.

### Output limits for generation requests

Set `recipe_file` in the endpoints JSON to a file relative to that configuration.
Conductor validates it at startup. Each model ID must match its configured tier.
For example, a recipe for the example endpoints is:

```json
{
  "schema": "hypermesh.conductor.recipe.v1",
  "cheap": {"model": "local-small", "max_output_tokens": 256},
  "frontier": {"model": "frontier", "max_output_tokens": 1024}
}
```

The proxy applies the tier limit to each completion after translation and review.
It supplies an omitted limit and rejects a higher requested limit. Embeddings
have no generation allowance. Main request journal rows carry the recipe file
SHA-256 and an approximate input size computed from the final serialized payload
with the existing character-based estimator. Provider-reported usage remains
separate. These limits apply per completion; cumulative task budgets require
durable reservations.

### Prepare leased capacity

With the lease-client capable `hypermesh-orchestrator` on PATH and the renter's
`HYPERMESH_API_KEY` in the environment, run `conductor prepare-leases --tenant TENANT`.
Optional `--catalog-id` and `--api-base` select a model and trusted plane.
This command can open offers on the allowed hosts and waits for gateway tickets.
It emits the device, lease ID and ownership flag for each host. The renter is
responsible for closing newly opened leases after use. Preparing capacity is
explicit; ordinary routing and proxy startup do not acquire leases.
