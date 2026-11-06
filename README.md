# Hypermesh Conductor

Budgeted, recoverable AI task runtime for Hypermesh: model and host selection, token and time budgets, bounded agents.

## Status

**Early slice.** Tiered routing and per-task measurement are in the tree. Budgets, durable runs, retrieval, planning, the task tracker, and context compaction are still design. Nothing is released.

The library under `src/conductor` is Python 3.11+ and uses the standard library for HTTP. `python -m pytest` runs the tests after `pip install -r requirements-dev.txt`. Design notes: [docs/routing.md](docs/routing.md) and [docs/measurement.md](docs/measurement.md).

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
- OpenAI-compatible endpoints are named in a local config file. API keys stay in the environment variables that file names. Sending private note bodies to an external provider still needs a separate review. The tests call a local fake server.

**Security and safety**
- Policy hooks, defenses against prompt and tool injection, controlled egress, and an audit trail, with integration points to AIuditor.
- Retrieved text and tool output are treated as untrusted data and cannot grant permissions.

## Non-goals

- No custody of funds. Conductor does not hold or move money.
- No bring-your-own-key product. A configured endpoint reads its key from the environment for this routing slice. Conductor does not store the key and does not bill for it.
- Not a replacement for Hypermesh billing or payment. Conductor reserves and meters against budgets; Hypermesh and its billing services own prices, charges and refunds.
- Not a scheduler for compute. Hypermesh makes the host placement decision.

## Routing and measurement

This slice classifies each step and records what the provider reports.

| Step | Tier |
| --- | --- |
| parse, extract, index, classify, embed, summarize, status | cheap (local is the same tier) |
| plan, architecture, tradeoff, code, review | frontier |

A suggestion that asks for a higher tier is rejected, and the policy tier is the one that is called. The system prompt and tool definitions stay a stable prefix ahead of the step text. Their hash is stored on every journal row.

Each row records input tokens, output tokens, tier, model id, latency, and cache-hit tokens when the provider reports them. Missing usage stays null. `python -m conductor summary` reports tokens per task, the cheap versus frontier split, and the cache hit rate. Accuracy under compaction modes stays null; those scores belong to [hypermesh-bench#12](https://github.com/hyperme-sh/hypermesh-bench/issues/12). The summary does not publish a savings figure.

```bash
python -m conductor route --config examples/endpoints.json --class parse
python -m conductor summary --journal path/to/journal.jsonl
```

`examples/endpoints.json` points at local placeholders. Set `CONDUCTOR_CHEAP_API_KEY` and `CONDUCTOR_FRONTIER_API_KEY` before `run`.

## Contributing

Issues are welcome. Please open an issue to discuss an idea before sending a pull request.

## License

MIT. See [LICENSE](LICENSE).

## Security

Please report security problems to security@hyperme.sh, not in a public issue. Report other bugs to bugs@hyperme.sh. See [SECURITY.md](SECURITY.md).
