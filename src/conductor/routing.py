"""Deterministic job-class routing.

Rules are the authority. A suggestion can match the policy tier. It cannot
move a step onto a higher tier, and it cannot replace the policy tier with a
lower one. There is no retry onto the other tier when a call fails.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Mapping

from conductor.errors import UnknownJobClass, UnknownTier

CHEAP = "cheap"
FRONTIER = "frontier"
TIER_RANK = {CHEAP: 0, FRONTIER: 1}

# Default policy from the routing design. A profile may replace individual
# entries; a suggestion may not.
DEFAULT_TIERS: dict[str, str] = {
    "parse": CHEAP,
    "extract": CHEAP,
    "index": CHEAP,
    "classify": CHEAP,
    "embed": CHEAP,
    "summarize": CHEAP,
    "status": CHEAP,
    "plan": FRONTIER,
    "architecture": FRONTIER,
    "tradeoff": FRONTIER,
    "code": FRONTIER,
    "review": FRONTIER,
}

_ALIASES = {
    "parsing": "parse",
    "extraction": "extract",
    "indexing": "index",
    "classification": "classify",
    "embedding": "embed",
    "embeddings": "embed",
    "summary": "summarize",
    "summarization": "summarize",
    "status update": "status",
    "write status": "status",
    "planning": "plan",
    "architect": "architecture",
    "tradeoffs": "tradeoff",
    "ambiguous tradeoff": "tradeoff",
    "ambiguous tradeoffs": "tradeoff",
    "codegen": "code",
    "code generation": "code",
    "implement": "code",
    "code review": "review",
}


@dataclass(frozen=True)
class Decision:
    """Preview of one routing decision. The policy tier is the tier that is used."""

    job_class: str
    tier: str
    reason: str
    suggestion_tier: str | None
    suggestion_accepted: bool | None

    def to_dict(self) -> dict:
        return {
            "job_class": self.job_class,
            "tier": self.tier,
            "reason": self.reason,
            "suggestion_tier": self.suggestion_tier,
            "suggestion_accepted": self.suggestion_accepted,
        }


def normalize_label(value: str) -> str:
    text = value.strip().lower().replace("-", " ").replace("_", " ")
    return " ".join(text.split())


def canonicalize_class(value: str) -> str:
    label = normalize_label(value)
    if not label:
        raise UnknownJobClass("job class is empty")
    if label in DEFAULT_TIERS:
        return label
    aliased = _ALIASES.get(label)
    if aliased is None:
        raise UnknownJobClass(f"unknown job class {value!r}")
    return aliased


def canonicalize_tier(value: str) -> str:
    label = normalize_label(value)
    if label in {CHEAP, "local"}:
        return CHEAP
    if label in {FRONTIER, "strong"}:
        return FRONTIER
    raise UnknownTier(f"unknown tier {value!r}")


def route(
    job_class: str,
    *,
    suggestion_tier: str | None = None,
    class_tiers: Mapping[str, str] | None = None,
) -> Decision:
    """Choose the policy tier for a step and record whether a suggestion matched."""
    canonical = canonicalize_class(job_class)
    policy = DEFAULT_TIERS[canonical]
    if class_tiers is not None and canonical in class_tiers:
        policy = canonicalize_tier(class_tiers[canonical])

    if suggestion_tier is None:
        return Decision(
            job_class=canonical,
            tier=policy,
            reason=f"policy tier for {canonical} is {policy}",
            suggestion_tier=None,
            suggestion_accepted=None,
        )

    suggested = canonicalize_tier(suggestion_tier)
    if suggested == policy:
        return Decision(
            job_class=canonical,
            tier=policy,
            reason=f"suggestion matches policy tier {policy} for {canonical}",
            suggestion_tier=suggested,
            suggestion_accepted=True,
        )
    if TIER_RANK[suggested] > TIER_RANK[policy]:
        reason = f"rejected out-of-policy upgrade from {policy} to {suggested} for {canonical}"
    else:
        reason = f"rejected tier change from {policy} to {suggested} for {canonical}; policy tier stands"
    return Decision(
        job_class=canonical,
        tier=policy,
        reason=reason,
        suggestion_tier=suggested,
        suggestion_accepted=False,
    )
