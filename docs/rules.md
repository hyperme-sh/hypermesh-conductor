# Policy learning

Online heuristics run on the request. The offline `dream` command replays the journal and writes a proposal. The proposal is not active until `rules apply`.

## Online

The active file (`rules_file` in the endpoints config, when set) can name class tiers, a peak-token escalation threshold, and `cache_prefix.prefer_on_match`. Escalation is a written rule. A prefix match without a written tier change is only a suggestion, and routing rejects suggestions that would move off the policy tier. Nothing in this path edits AIuditor.

## Offline

```bash
conductor dream --journal runs/journal.jsonl --outcomes runs/outcomes.jsonl --proposals rules/proposals
conductor rules diff --rules rules/active.json --proposal rules/proposals/0001.json
conductor rules apply --rules rules/active.json --proposal rules/proposals/0001.json
conductor rules rollback --rules rules/active.json
```

`dream` reads metrics rows and outcome records (`test_passed`, `test_failed`, `retry`, `escalation`, `aiuditor_deny`, `user_correction`, `link_used`, `link_unused`). It writes `rules/proposals/NNNN.json` with evidence step ids and prevalence. Repeated cheap-tier misses propose an escalation threshold. Cache reports that never hit propose keeping the system prompt and tools in the prefix. Repeated link outcomes propose index link adds or drops. AIuditor denials are recorded as observations. They do not become permission to skip the hop.

Prompt bodies stay out of the proposal unless `--bodies` is set. That flag reads `history.jsonl`. Keys are not part of either file.

`apply` refuses a file that contains `aiuditor`, `fail_closed`, `deny_job_classes`, `bypass`, or `fail_open`. The previous active file is copied under `rules/revisions/` first. `rollback` restores that copy.

Activation here is the local `apply` command. A bench replay of compaction modes stays in hypermesh-bench issue 12. This command does not claim a measured saving.
