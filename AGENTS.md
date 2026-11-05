## Version, tag and release authority

The owner has not authorized the services' 0.24.0 release. Service packages
retain their recorded pre-0.24.0 versions. Host and visor have a separate release
cycle. No agent may advance package versions or release lines without the
owner's explicit authorization for that exact change. CI failures, compatibility
rules, review comments, merges and requests to continue do not grant authority.

Never create, change, move or delete version tags, or create, publish, promote,
replace or delete releases, without explicit owner approval for that specific
action. Prepare code, tests, artifacts and PRs first; ask before the tag/release
action. Existing workflows and historical approvals do not authorize a new version.

Development qualification must work without a prior release. Use immutable
source revisions, fresh artifacts and consistent locks. Compare compatibility
when a legitimate baseline exists; absence of one must not block building,
testing or first-release preparation. Preserve historical tags and evidence.

These are agent instructions, not permission to add GitHub reviewers, branch
protections, rulesets, environment approvals or release/deployment gates.
Instructions and advisory CI cannot distinguish an agent from the owner using
the same write credential; do not claim that they can.

