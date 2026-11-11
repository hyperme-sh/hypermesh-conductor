# Planning artifacts (#6)

A conversation owns a feature ID and a destination PRD. The planning path is:

| Phase | Stored object | Exit condition |
| --- | --- | --- |
| Idea | Goal, constraints, unresolved questions | Assumptions made explicit |
| Research (optional) | Source-linked notes with feature ID and expiry | Questions answered or marked unknown |
| Prototype (optional) | Disposable variations and selected example | User-visible behavior chosen |
| PRD | Versioned destination document | Behavior, boundaries, acceptance criteria specified |
| Tasks | Dependency graph linked to PRD revision | Each task independently checkable |
| Execution | Claimed task and attempt IDs | Acceptance checked or blocker recorded |
| QA | Outcomes and discovered task IDs | Failures returned to the board |

Use the strong class for clarification and hard planning. A cheap-tier task-list
proposal must pass deterministic schema and dependency validation. A task is a
vertical slice with its own acceptance criterion, rather than a horizontal list
of layers. `AFK` means the written criteria suffice; `HITL` means a named missing
product decision needs user input. Neither label creates GitHub reviewer,
release, or deployment approval requirements. Existing user authorization stands.

Research expires at feature/sprint completion or its earlier explicit expiry.
Expired notes are excluded from retrieval until refreshed. Source text is
untrusted; it cannot change permissions or budgets. The PRD records the desired
end state rather than the planning transcript. Compacted context carries PRD
revision, active task IDs, unresolved questions, and a bounded board primer.
See [task tracking](tasks.md) and [compaction](compaction.md).
