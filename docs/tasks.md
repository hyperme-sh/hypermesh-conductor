# Task graph (#7)

Each task has a random UUID, feature ID, PRD revision, title, acceptance criteria,
mode (`AFK` or `HITL`), dependencies, discovered-from task, revision, and status:
`open`, `in_progress`, `blocked`, or `done`. IDs remain stable across compaction.
Missing dependency IDs and cycles are errors. Done requires recorded outcomes;
a process exit alone is not a completion result.

Claim is a compare-and-swap transaction from open to in_progress with owner,
expected revision, and bounded claim expiry. Competing claims cannot both win.
Expired claims become eligible for recovery, not silently done. A claim does not
grant tool permissions. Cancellation and a lost worker retain uncertain attempts.
Only the current claim owner can update an active task; updates increment revision.

Ready work is open work whose dependencies are all done and which has no live
claim. A blocked task names the blocker; newly found work receives a new UUID and
a discovered-from link instead of being hidden in prose. A done dependency that
reopens makes dependent open tasks unready; already active tasks need reevaluation.

The in-loop payload is bounded JSON: active task ID, status, blocker IDs, next
ready IDs, and PRD revision. The compacted tail stores this payload and a lookup
pointer, not a copy of the board or transcript. Measurement groups attempts by
this task ID, including retries. The existing X-Conductor-Task field is the wire
correlation key. This defines the board contract; it does not turn the current
usage journal into a task database or claim a shipped board command.
