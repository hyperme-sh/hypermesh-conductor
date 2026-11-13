# Controller-backed capability dispatch

`conductor capability-dispatch --config PROFILE` reads a capability call from
stdin. `capability-catalog --config PROFILE` lists only verb bindings enabled by
the machine bridge and still matching the immutable note catalog. Neither command
enables a capability or weakens a controller. The Linux root bridge launches these
fixed commands; the non-root Visor process never talks directly to the controller.

Profile fields: `schema: 1`, absolute `notes`, `index_db`, `journal`, `bridge` and
`controller` paths. Files must be owned regular files without group/other write
permission. Index and journal parents must already be owned mode-0700 directories.
The bridge checks the root-owned profile and binary before launch. Both IPC peers
must have kernel UID zero; no HTTP header, TCP endpoint, redirect, user-controlled
UID setting or bearer substitute is accepted. Controllers must reject all
non-root peers before processing their API. This closes the path where a Visor
account could bypass policy by calling a controller directly.

Calls contain scope, owner, capability ID/revision, source/graph SHA256, a fresh
UUID `call_id`, and `arguments_json` containing the exact tool argument bytes.
The dispatcher validates the binding, durably reserves the call ID, then asks the
controller to prepare. A failed or uncertain call ID cannot be reused, including
after process restart. The journal holds at most 100,000 calls and refuses further
work at capacity; it never automatically deletes IDs or replays operations.

The controller API is HTTP over its root-authenticated Unix socket:

- `POST /prepare`: `{call, requirements}`. Requirements come from the validated
  catalog and include inherited conditions, immutable limit revisions, locks,
  timeout and freshness bounds. Preparation reserves all locks without actuation.
  Reply: version 1, call ID and SHA256 of the compact JSON call, a random 64-hex
  permit, `observed_at_ms`, `expires_at_ms`, current conditions/limit revisions,
  and the complete reserved lock list. Expiry is at most 30 seconds away.
- `POST /execute`: `{call, call_sha256, permit, deadline_ms}`. Atomically consume
  the permit, recheck current preconditions and hard limits, verify all bindings
  and deadline, then execute at most once. The controller must independently
  retain duplicate-call protection across restarts. Reply: version 1, matching
  call ID/hash, `completed: true`, and bounded `output_json`.
- `POST /cancel`: `{call_id, permit}` releases a reservation, including after
  denial or cancellation. Expiry must also release locks if cleanup is lost.

A controller must compare requested requirements against its own immutable driver
rules; request-provided names or booleans never define physical limits. It owns
e-stop, actuator, chassis/geofence and device-specific checks below Visor.
Unimplemented operations and unknown limit revisions must be refused. Preparation
must not actuate. Root ownership authenticates a controller process; it does not
certify its hardware implementation or make a fixture controller a physical one.

While the controller reservation is held, Conductor validates typed arguments and
fresh state with the existing capability catalog, obtains current machine review
through the bridge, and checks every limit and precondition. Dispatch uses the
earliest note, controller and policy deadline, rechecks freshness after review,
and never retries. Errors release the reservation with a bounded cleanup attempt.
A deny, missing controller, stale state, changed graph, expired grant, mismatched
response or uncertain result never becomes success. Controller output is bounded;
prompt and argument bodies are not logged by these commands.
