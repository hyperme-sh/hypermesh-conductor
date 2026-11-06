import json

import pytest

from conductor.errors import ConfigError
from conductor.metrics import ACCURACY_UNMEASURED, NUMBERS_POLICY, StepRecord, append_journal, read_journal, summarize


def row(**overrides):
    payload = dict(
        task_id="task-1",
        step_id="step-1",
        job_class="parse",
        tier="cheap",
        model_id="local-small",
        reason="policy tier for parse is cheap",
        suggestion_tier=None,
        suggestion_accepted=None,
        input_tokens=100,
        output_tokens=10,
        cache_hit_tokens=25,
        latency_ms=5,
        prefix_sha256="a" * 64,
        usage_status="measured",
        error=None,
        cost_proxy=None,
        cost_proxy_label=None,
    )
    payload.update(overrides)
    return StepRecord(**payload)


def test_summary_reports_tokens_per_task_and_the_tier_split():
    records = [
        row(step_id="s1", input_tokens=100, output_tokens=10, cache_hit_tokens=40),
        row(
            step_id="s2",
            job_class="code",
            tier="frontier",
            model_id="frontier",
            input_tokens=50,
            output_tokens=20,
            cache_hit_tokens=10,
            latency_ms=7,
        ),
        row(task_id="task-2", step_id="s3", input_tokens=5, output_tokens=1, cache_hit_tokens=0, latency_ms=3),
    ]
    summary = summarize(records)
    assert summary["schema"] == "hypermesh.conductor.efficiency.v0"
    assert summary["numbers_policy"] == NUMBERS_POLICY
    assert summary["accuracy"] == ACCURACY_UNMEASURED
    assert [task["task_id"] for task in summary["tasks"]] == ["task-1", "task-2"]

    first = summary["tasks"][0]
    assert first["input_tokens"] == 150
    assert first["output_tokens"] == 30
    assert first["peak_context_tokens"] == 100
    assert first["by_tier"]["cheap"]["input_tokens"] == 100
    assert first["by_tier"]["cheap"]["output_tokens"] == 10
    assert first["by_tier"]["cheap"]["steps"] == 1
    assert first["by_tier"]["frontier"]["input_tokens"] == 50
    assert first["by_tier"]["frontier"]["steps"] == 1
    assert first["cache_hit_rate"] == pytest.approx(50 / 150)
    assert first["prefix_reuse_rate"] == 1.0
    assert first["latency_ms"] == 12

    assert summary["tasks"][1]["input_tokens"] == 5
    assert summary["tasks"][1]["cache_hit_rate"] == 0.0
    assert summary["totals"]["input_tokens"] == 155
    assert summary["totals"]["output_tokens"] == 31
    assert summary["totals"]["by_tier"]["cheap"]["input_tokens"] == 105
    assert summary["totals"]["by_tier"]["frontier"]["output_tokens"] == 20
    assert summary["totals"]["peak_context_tokens"] == 100


def test_omitted_cache_figures_leave_the_hit_rate_null():
    summary = summarize([row(cache_hit_tokens=None), row(step_id="s2", cache_hit_tokens=5)])
    assert summary["totals"]["cache_hit_rate"] is None
    assert summary["totals"]["cache_hit_tokens"] is None
    assert summary["totals"]["cache_report_complete"] is False
    assert summary["totals"]["input_tokens"] == 200


def test_uncertain_usage_is_not_counted_as_zero():
    summary = summarize(
        [
            row(input_tokens=80, output_tokens=8, cache_hit_tokens=8),
            row(
                step_id="s2",
                tier="frontier",
                job_class="review",
                model_id="frontier",
                usage_status="uncertain",
                input_tokens=None,
                output_tokens=None,
                cache_hit_tokens=None,
                cost_proxy=None,
                error="provider HTTP 500",
            ),
        ]
    )
    totals = summary["totals"]
    assert totals["input_tokens"] is None
    assert totals["output_tokens"] is None
    assert totals["tokens_complete"] is False
    assert totals["measured_input_tokens"] == 80
    assert totals["measured_output_tokens"] == 8
    assert totals["peak_context_tokens"] is None
    assert totals["cache_hit_rate"] is None
    assert totals["cost_proxy"] is None
    assert totals["by_tier"]["frontier"]["input_tokens"] is None
    assert totals["by_tier"]["frontier"]["steps"] == 1
    assert totals["uncertain_steps"] == 1


def test_cost_proxy_is_averaged_only_when_every_step_has_one():
    label = "proxy: tokens × published rate (card); not a charge"
    priced = summarize(
        [
            row(cost_proxy=2.0, cost_proxy_label=label),
            row(step_id="s2", tier="frontier", job_class="plan", model_id="frontier", cost_proxy=4.0, cost_proxy_label=label),
        ]
    )
    assert priced["totals"]["cost_proxy"] == pytest.approx(6.0)
    assert priced["totals"]["cost_per_step"] == pytest.approx(3.0)
    assert priced["totals"]["cost_proxy_label"] == label

    mixed = summarize([row(cost_proxy=2.0, cost_proxy_label=label), row(step_id="s2")])
    assert mixed["totals"]["cost_proxy"] is None
    assert mixed["totals"]["cost_per_step"] is None


def test_prefix_reuse_needs_two_steps():
    assert summarize([row()])["totals"]["prefix_reuse_rate"] is None
    summary = summarize([row(), row(step_id="s2", prefix_sha256="b" * 64)])
    assert summary["totals"]["prefix_reuse_rate"] == 0.5


def test_cache_hits_above_input_tokens_leave_the_rate_null():
    summary = summarize([row(input_tokens=10, cache_hit_tokens=11)])
    assert summary["totals"]["cache_hit_rate"] is None
    assert summary["totals"]["cache_hit_tokens"] is None


def test_empty_journal_summary_has_null_totals():
    summary = summarize([])
    assert summary["tasks"] == []
    assert summary["totals"]["steps"] == 0
    assert summary["totals"]["input_tokens"] is None
    assert summary["totals"]["by_tier"]["cheap"]["steps"] == 0
    assert summary["totals"]["by_tier"]["frontier"]["input_tokens"] is None
    assert summary["accuracy"]["full_history"] is None
    assert summary["accuracy"]["end_of_conversation_compaction"] is None
    assert summary["accuracy"]["single_action_compaction"] is None


def test_journal_roundtrip(tmp_path):
    path = tmp_path / "journal.jsonl"
    append_journal(path, row())
    loaded = read_journal(path)
    assert loaded[0] == row()
    assert json.loads(path.read_text(encoding="utf-8"))["accuracy"]["context_mode"] is None


def test_malformed_journal_fails_closed(tmp_path):
    path = tmp_path / "journal.jsonl"
    path.write_text("{}\n", encoding="utf-8")
    with pytest.raises(ConfigError, match="schema"):
        read_journal(path)


def test_measured_record_cannot_hide_null_tokens():
    with pytest.raises(ConfigError, match="measured"):
        row(input_tokens=None, output_tokens=None)


def test_uncertain_record_cannot_carry_zero_tokens():
    with pytest.raises(ConfigError, match="uncertain"):
        row(usage_status="uncertain", input_tokens=0, output_tokens=0, cache_hit_tokens=0)
