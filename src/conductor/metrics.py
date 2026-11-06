"""Append-only measurement journal and the efficiency summary.

Fields line up with the compaction-aware scorecard requested by
hypermesh-bench issue 12: peak context, a labeled cost-per-step proxy, and
accuracy under full history, end-of-conversation compaction, and single-action
compaction. Accuracy stays null in this repository. Unmeasured token fields
stay null. A missing provider cache report stays null rather than zero.
"""

from __future__ import annotations

import json
from collections import Counter
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterable

from conductor.errors import ConfigError
from conductor.routing import CHEAP, FRONTIER

SCHEMA_STEP = "hypermesh.conductor.step.v0"
SCHEMA_SUMMARY = "hypermesh.conductor.efficiency.v0"

NUMBERS_POLICY = (
    "Unmeasured fields stay null. "
    "Lost or uncertain usage stays uncertain and is never recorded as zero. "
    "A cost figure is a labeled proxy from published rates, or null. "
    "This record is not a savings measurement. "
    "Compaction-mode accuracy stays null and belongs to hypermesh-bench issue 12."
)

ACCURACY_UNMEASURED = {
    "context_mode": None,
    "full_history": None,
    "end_of_conversation_compaction": None,
    "single_action_compaction": None,
}

_REQUIRED = (
    "schema",
    "task_id",
    "step_id",
    "job_class",
    "tier",
    "model_id",
    "reason",
    "suggestion_tier",
    "suggestion_accepted",
    "input_tokens",
    "output_tokens",
    "cache_hit_tokens",
    "latency_ms",
    "prefix_sha256",
    "usage_status",
    "error",
    "cost_proxy",
    "cost_proxy_label",
    "peak_context_tokens",
)


@dataclass(frozen=True)
class StepRecord:
    task_id: str
    step_id: str
    job_class: str
    tier: str
    model_id: str
    reason: str
    suggestion_tier: str | None
    suggestion_accepted: bool | None
    input_tokens: int | None
    output_tokens: int | None
    cache_hit_tokens: int | None
    latency_ms: int | None
    prefix_sha256: str
    usage_status: str
    error: str | None = None
    cost_proxy: float | None = None
    cost_proxy_label: str | None = None

    def __post_init__(self) -> None:
        if self.tier not in {CHEAP, FRONTIER}:
            raise ConfigError(f"unknown tier {self.tier!r}")
        if self.usage_status not in {"measured", "uncertain"}:
            raise ConfigError(f"unknown usage_status {self.usage_status!r}")
        if self.usage_status == "measured":
            if self.input_tokens is None or self.output_tokens is None:
                raise ConfigError("measured usage needs input and output tokens")
        elif (
            self.input_tokens is not None
            or self.output_tokens is not None
            or self.cache_hit_tokens is not None
            or self.cost_proxy is not None
        ):
            raise ConfigError("uncertain usage cannot carry token counts or a cost proxy")

    def to_dict(self) -> dict[str, Any]:
        peak = self.input_tokens if self.usage_status == "measured" else None
        return {
            "schema": SCHEMA_STEP,
            "task_id": self.task_id,
            "step_id": self.step_id,
            "job_class": self.job_class,
            "tier": self.tier,
            "model_id": self.model_id,
            "reason": self.reason,
            "suggestion_tier": self.suggestion_tier,
            "suggestion_accepted": self.suggestion_accepted,
            "input_tokens": self.input_tokens,
            "output_tokens": self.output_tokens,
            "cache_hit_tokens": self.cache_hit_tokens,
            "latency_ms": self.latency_ms,
            "prefix_sha256": self.prefix_sha256,
            "usage_status": self.usage_status,
            "error": self.error,
            "cost_proxy": self.cost_proxy,
            "cost_proxy_label": self.cost_proxy_label,
            "peak_context_tokens": peak,
            "cost_per_step": self.cost_proxy,
            "accuracy": dict(ACCURACY_UNMEASURED),
        }


def append_journal(path: str | Path, record: StepRecord) -> None:
    file_path = Path(path)
    if not file_path.parent.exists():
        raise ConfigError(f"journal directory does not exist: {file_path.parent}")
    line = json.dumps(record.to_dict(), sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    with file_path.open("a", encoding="utf-8") as handle:
        handle.write(line + "\n")
        handle.flush()


def read_journal(path: str | Path) -> list[StepRecord]:
    file_path = Path(path)
    try:
        text = file_path.read_text(encoding="utf-8")
    except FileNotFoundError as exc:
        raise ConfigError(f"journal not found: {file_path}") from exc
    records: list[StepRecord] = []
    for line_number, line in enumerate(text.splitlines(), start=1):
        if line.strip() == "":
            continue
        try:
            payload = json.loads(line)
        except json.JSONDecodeError as exc:
            raise ConfigError(f"journal line {line_number} is not JSON") from exc
        records.append(_record_from_dict(payload, line_number))
    return records


def summarize(records: Iterable[StepRecord]) -> dict[str, Any]:
    rows = list(records)
    by_task: dict[str, list[StepRecord]] = {}
    order: list[str] = []
    for row in rows:
        if row.task_id not in by_task:
            order.append(row.task_id)
            by_task[row.task_id] = []
        by_task[row.task_id].append(row)
    tasks = [_group_summary(task_id, by_task[task_id]) for task_id in order]
    return {
        "schema": SCHEMA_SUMMARY,
        "numbers_policy": NUMBERS_POLICY,
        "tasks": tasks,
        "totals": _totals(rows),
        "accuracy": dict(ACCURACY_UNMEASURED),
    }


def _totals(rows: list[StepRecord]) -> dict[str, Any]:
    block = _measure_block(rows)
    block["by_tier"] = {
        CHEAP: _measure_block([row for row in rows if row.tier == CHEAP]),
        FRONTIER: _measure_block([row for row in rows if row.tier == FRONTIER]),
    }
    return block


def _group_summary(task_id: str, rows: list[StepRecord]) -> dict[str, Any]:
    return {"task_id": task_id, **_totals(rows)}


def _measure_block(rows: list[StepRecord]) -> dict[str, Any]:
    if not rows:
        return {
            "steps": 0,
            "uncertain_steps": 0,
            "input_tokens": None,
            "output_tokens": None,
            "tokens_complete": False,
            "measured_input_tokens": None,
            "measured_output_tokens": None,
            "peak_context_tokens": None,
            "cache_hit_tokens": None,
            "cache_hit_rate": None,
            "cache_report_complete": False,
            "measured_cache_hit_tokens": None,
            "prefix_reuse_rate": None,
            "latency_ms": None,
            "measured_latency_ms": None,
            "cost_proxy": None,
            "cost_proxy_label": None,
            "cost_per_step": None,
        }
    uncertain = sum(1 for row in rows if row.usage_status != "measured")
    measured = [row for row in rows if row.usage_status == "measured"]
    tokens_complete = uncertain == 0
    measured_input = sum(row.input_tokens or 0 for row in measured)
    measured_output = sum(row.output_tokens or 0 for row in measured)
    peak_values = [row.input_tokens for row in measured if row.input_tokens is not None]
    peak_complete = tokens_complete and all(row.input_tokens is not None for row in rows)
    eligible = [row for row in measured if row.input_tokens is not None and row.input_tokens > 0]
    reported = [row for row in eligible if row.cache_hit_tokens is not None]
    cache_complete = uncertain == 0 and len(eligible) > 0 and len(reported) == len(eligible)
    cache_tokens = sum(row.cache_hit_tokens or 0 for row in reported)
    cache_inputs = sum(row.input_tokens or 0 for row in reported)
    over_reported = bool(reported) and cache_tokens > cache_inputs
    if cache_complete and not over_reported:
        cache_rate = cache_tokens / cache_inputs
        reported_cache = cache_tokens
    else:
        cache_rate = None
        reported_cache = None
    latency_values = [row.latency_ms for row in rows if row.latency_ms is not None]
    latency_complete = len(rows) > 0 and len(latency_values) == len(rows)
    costs = [row.cost_proxy for row in rows]
    cost_complete = len(rows) > 0 and all(cost is not None for cost in costs) and uncertain == 0
    labels = {row.cost_proxy_label for row in rows if row.cost_proxy is not None}
    prefix_rate = _prefix_reuse(rows)
    return {
        "steps": len(rows),
        "uncertain_steps": uncertain,
        "input_tokens": measured_input if tokens_complete else None,
        "output_tokens": measured_output if tokens_complete else None,
        "tokens_complete": tokens_complete,
        "measured_input_tokens": measured_input,
        "measured_output_tokens": measured_output,
        "peak_context_tokens": max(peak_values) if peak_complete and peak_values else None,
        "cache_hit_tokens": reported_cache,
        "cache_hit_rate": cache_rate,
        "cache_report_complete": cache_complete and not over_reported,
        "measured_cache_hit_tokens": cache_tokens if reported and not over_reported else None,
        "prefix_reuse_rate": prefix_rate,
        "latency_ms": sum(latency_values) if latency_complete else None,
        "measured_latency_ms": sum(latency_values) if latency_values else None,
        "cost_proxy": float(sum(cost for cost in costs if cost is not None)) if cost_complete else None,
        "cost_proxy_label": " | ".join(sorted(label for label in labels if label)) if cost_complete and labels else None,
        "cost_per_step": (sum(costs) / len(rows)) if cost_complete else None,
    }


def _prefix_reuse(rows: list[StepRecord]) -> float | None:
    """Fraction of steps that share the most common prefix hash.

    One step has nothing to reuse against, so the rate is null. This is the
    byte-stability proxy used when a provider does not report cache-hit tokens.
    """
    if len(rows) < 2:
        return None
    counts = Counter(row.prefix_sha256 for row in rows if row.prefix_sha256)
    if not counts:
        return None
    return counts.most_common(1)[0][1] / len(rows)


def _record_from_dict(payload: Any, line_number: int) -> StepRecord:
    if not isinstance(payload, dict):
        raise ConfigError(f"journal line {line_number} must be an object")
    if payload.get("schema") != SCHEMA_STEP:
        raise ConfigError(f"journal line {line_number} has an unknown schema")
    missing = [field for field in _REQUIRED if field not in payload]
    if missing:
        raise ConfigError(f"journal line {line_number} is missing {', '.join(missing)}")
    tier = payload["tier"]
    if tier not in {CHEAP, FRONTIER}:
        raise ConfigError(f"journal line {line_number} has an unknown tier")
    suggestion = payload["suggestion_tier"]
    accepted = payload["suggestion_accepted"]
    if suggestion is not None and suggestion not in {CHEAP, FRONTIER}:
        raise ConfigError(f"journal line {line_number} has an unknown suggestion tier")
    if accepted is not None and not isinstance(accepted, bool):
        raise ConfigError(f"journal line {line_number} has a bad suggestion_accepted")
    if (suggestion is None) != (accepted is None):
        raise ConfigError(f"journal line {line_number} has a mismatched suggestion")
    status = payload["usage_status"]
    if status not in {"measured", "uncertain"}:
        raise ConfigError(f"journal line {line_number} has an unknown usage_status")
    input_tokens = _nullable_count(payload["input_tokens"], line_number, "input_tokens")
    output_tokens = _nullable_count(payload["output_tokens"], line_number, "output_tokens")
    cache_hit = _nullable_count(payload["cache_hit_tokens"], line_number, "cache_hit_tokens")
    if status == "measured" and (input_tokens is None or output_tokens is None):
        raise ConfigError(f"journal line {line_number} is measured but tokens are null")
    if status == "uncertain" and (input_tokens is not None or output_tokens is not None or cache_hit is not None):
        raise ConfigError(f"journal line {line_number} is uncertain but contains token counts")
    latency = payload["latency_ms"]
    if latency is not None and (isinstance(latency, bool) or not isinstance(latency, int) or latency < 0):
        raise ConfigError(f"journal line {line_number} has a bad latency_ms")
    cost = payload["cost_proxy"]
    if cost is not None and (isinstance(cost, bool) or not isinstance(cost, (int, float))):
        raise ConfigError(f"journal line {line_number} has a bad cost_proxy")
    if status == "uncertain" and cost is not None:
        raise ConfigError(f"journal line {line_number} is uncertain but has a cost proxy")
    for text_field in ("task_id", "step_id", "job_class", "model_id", "prefix_sha256"):
        if not isinstance(payload[text_field], str) or payload[text_field].strip() == "":
            raise ConfigError(f"journal line {line_number} has an empty {text_field}")
    return StepRecord(
        task_id=payload["task_id"],
        step_id=payload["step_id"],
        job_class=payload["job_class"],
        tier=tier,
        model_id=payload["model_id"],
        reason=payload["reason"] if isinstance(payload["reason"], str) else "",
        suggestion_tier=suggestion,
        suggestion_accepted=accepted,
        input_tokens=input_tokens,
        output_tokens=output_tokens,
        cache_hit_tokens=cache_hit,
        latency_ms=latency,
        prefix_sha256=payload["prefix_sha256"],
        usage_status=status,
        error=payload["error"] if isinstance(payload["error"], str) else None,
        cost_proxy=None if cost is None else float(cost),
        cost_proxy_label=payload["cost_proxy_label"] if isinstance(payload["cost_proxy_label"], str) else None,
    )


def _nullable_count(value: Any, line_number: int, field: str) -> int | None:
    if value is None:
        return None
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise ConfigError(f"journal line {line_number} has a bad {field}")
    return value
