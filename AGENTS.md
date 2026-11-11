## Host and CI test boundary

Do not run, plan, or propose integration, end-to-end, or full-flow tests on
hosts, VMs, or shared resources. Those run only in CI and the nightly batch
against main. In PRs, issues, comments, plans, and chat, report unit and
contract test results only. Never use the words l-i-f-e-c-y-c-l-e,
r-e-c-e-i-p-t, or e-v-i-d-e-n-c-e outside .github/workflows.

Close the issue when its PR merges with green CI. Do not leave issues open
pending the nightly batch or any live run. Nightly failures are filed as new
issues.

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
testing or first-release preparation. Preserve historical tags and release records.

These are agent instructions, not permission to add GitHub reviewers, branch
protections, rulesets, environment approvals or release/deployment gates.
Instructions and advisory CI cannot distinguish an agent from the owner using
the same write credential; do not claim that they can.

## Nightly integration batch

No agent runs integration or end-to-end tests by hand on hosts
or other shared resources, and none run those suites outside CI. On a pull
request, agents run only unit and contract tests. CI may add a smoke check
scoped to the services the pull request touches. A toggle or configuration
change needs a unit or contract test for that change, not a full integration or
end-to-end suite.

The full integration suite runs once as a nightly CI batch against main.
That batch uses a single concurrency group and cancels any run it supersedes.
When a nightly run fails on something whose issue is already closed, CI files
a new issue for that failure and links the closed issue when one exists.

CI results and full integration output are not a Product Sign-off gate. Green
CI and clean reviews are enough for Sign-off.

Authorized CI workflows, including test-bundle publication and existing smoke
or owner-integration jobs, may keep running in CI. Agents must not start or
repeat those jobs by hand on shared hosts.

## Service deployments and binary provenance

Each TEST service automatically tracks the latest PR merged into its repository's default branch. Deploy the tested build through a moving TEST channel. Do not pin running services to versions, commit SHAs or image digests, require manual pin updates, or require peer services to run matching builds. Preserve patch compatibility, including during early development, and verify it through the service contract.

Use exact versions and digests for downloadable bundles and to record or verify which binary was built, tested or released. A running service may report its revision for diagnostics. Production follows its existing promotion policy and explicit release authorization.
