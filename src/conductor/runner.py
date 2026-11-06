"""Run one classified step against the tier the rules selected.

A failed call stays on that tier. The other endpoint is not contacted.
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable

from conductor.client import CallResult, chat_completion, embeddings
from conductor.config import ConductorConfig
from conductor.errors import ConfigError, ProviderError
from conductor.metrics import StepRecord, append_journal
from conductor.prefix import build_messages
from conductor.routing import route

_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,199}$")
Opener = Callable[..., Any]
Clock = Callable[[], float]


@dataclass(frozen=True)
class RunResult:
    record: StepRecord
    content: str | None


def preview(config: ConductorConfig, job_class: str, *, suggestion_tier: str | None = None) -> dict[str, Any]:
    decision = route(job_class, suggestion_tier=suggestion_tier, class_tiers=config.class_tiers)
    endpoint = config.endpoint_for(decision.tier)
    return {
        "job_class": decision.job_class,
        "tier": decision.tier,
        "model_id": endpoint.model,
        "reason": decision.reason,
        "suggestion_tier": decision.suggestion_tier,
        "suggestion_accepted": decision.suggestion_accepted,
        "prefix_sha256": config.prefix.sha256(),
    }


def execute_step(
    config: ConductorConfig,
    *,
    task_id: str,
    step_id: str,
    job_class: str,
    messages: list[dict[str, str]] | None = None,
    embedding_input: str | None = None,
    suggestion_tier: str | None = None,
    journal_path: str | Path | None = None,
    opener: Opener | None = None,
    monotonic: Clock | None = None,
) -> RunResult:
    _require_id(task_id, "task_id")
    _require_id(step_id, "step_id")
    decision = route(job_class, suggestion_tier=suggestion_tier, class_tiers=config.class_tiers)
    endpoint = config.endpoint_for(decision.tier)
    # Resolve the key before any network call so a missing key is a local
    # configuration error and is not written up as uncertain model usage.
    api_key = endpoint.api_key()
    prefix_hash = config.prefix.sha256()
    try:
        if decision.job_class == "embed":
            if messages:
                raise ConfigError("embed steps take input text, not chat messages")
            if not isinstance(embedding_input, str) or embedding_input == "":
                raise ConfigError("embed steps need input text")
            result = embeddings(
                endpoint,
                api_key,
                text=embedding_input,
                opener=opener,
                monotonic=monotonic,
            )
        else:
            if embedding_input is not None:
                raise ConfigError("chat steps take messages, not embedding input")
            chat_messages = build_messages(config.prefix, messages or [])
            result = chat_completion(
                endpoint,
                api_key,
                messages=chat_messages,
                tools=config.prefix.wire_tools(),
                opener=opener,
                monotonic=monotonic,
            )
    except ProviderError as exc:
        record = _record(
            task_id=task_id,
            step_id=step_id,
            decision_job=decision.job_class,
            decision_tier=decision.tier,
            decision_reason=decision.reason,
            suggestion_tier=decision.suggestion_tier,
            suggestion_accepted=decision.suggestion_accepted,
            model_id=endpoint.model,
            prefix_sha256=prefix_hash,
            call=None,
            error=str(exc),
            latency_ms=exc.latency_ms,
            cost_proxy=None,
            cost_proxy_label=None,
        )
        _store(journal_path, record)
        return RunResult(record=record, content=None)

    cost_proxy, cost_label = _price(config, decision.tier, result)
    record = _record(
        task_id=task_id,
        step_id=step_id,
        decision_job=decision.job_class,
        decision_tier=decision.tier,
        decision_reason=decision.reason,
        suggestion_tier=decision.suggestion_tier,
        suggestion_accepted=decision.suggestion_accepted,
        model_id=endpoint.model,
        prefix_sha256=prefix_hash,
        call=result,
        error=None,
        latency_ms=result.latency_ms,
        cost_proxy=cost_proxy,
        cost_proxy_label=cost_label,
    )
    _store(journal_path, record)
    return RunResult(record=record, content=result.content)


def _price(config: ConductorConfig, tier: str, result: CallResult) -> tuple[float | None, str | None]:
    usage = result.usage
    rate = config.rate_for(tier)
    if rate is None or usage.status != "measured":
        return None, None
    if usage.input_tokens is None or usage.output_tokens is None:
        return None, None
    amount = (usage.input_tokens / 1_000_000) * rate.input_per_million
    amount += (usage.output_tokens / 1_000_000) * rate.output_per_million
    label = f"proxy: tokens × published rate ({rate.source}); not a charge"
    return amount, label


def _record(
    *,
    task_id: str,
    step_id: str,
    decision_job: str,
    decision_tier: str,
    decision_reason: str,
    suggestion_tier: str | None,
    suggestion_accepted: bool | None,
    model_id: str,
    prefix_sha256: str,
    call: CallResult | None,
    error: str | None,
    latency_ms: int | None,
    cost_proxy: float | None,
    cost_proxy_label: str | None,
) -> StepRecord:
    if call is None or call.usage.status != "measured":
        return StepRecord(
            task_id=task_id,
            step_id=step_id,
            job_class=decision_job,
            tier=decision_tier,
            model_id=model_id,
            reason=decision_reason,
            suggestion_tier=suggestion_tier,
            suggestion_accepted=suggestion_accepted,
            input_tokens=None,
            output_tokens=None,
            cache_hit_tokens=None,
            latency_ms=latency_ms,
            prefix_sha256=prefix_sha256,
            usage_status="uncertain",
            error=error,
            cost_proxy=None,
            cost_proxy_label=None,
        )
    return StepRecord(
        task_id=task_id,
        step_id=step_id,
        job_class=decision_job,
        tier=decision_tier,
        model_id=model_id,
        reason=decision_reason,
        suggestion_tier=suggestion_tier,
        suggestion_accepted=suggestion_accepted,
        input_tokens=call.usage.input_tokens,
        output_tokens=call.usage.output_tokens,
        cache_hit_tokens=call.usage.cache_hit_tokens,
        latency_ms=latency_ms,
        prefix_sha256=prefix_sha256,
        usage_status="measured",
        error=None,
        cost_proxy=cost_proxy,
        cost_proxy_label=cost_proxy_label,
    )


def _store(path: str | Path | None, record: StepRecord) -> None:
    if path is not None:
        append_journal(path, record)


def _require_id(value: str, field: str) -> None:
    if not isinstance(value, str) or _ID.fullmatch(value) is None:
        raise ConfigError(f"{field} must be a short stable id")
