import pytest

from conductor.errors import UnknownJobClass, UnknownTier
from conductor.routing import DEFAULT_TIERS, route

CHEAP_LABELS = [
    "parse",
    "  Parse  ",
    "extract",
    "extraction",
    "index",
    "indexing",
    "classify",
    "classification",
    "embed",
    "embeddings",
    "summarize",
    "summary",
    "summarization",
    "status",
    "status update",
    "status_update",
    "write-status",
]

FRONTIER_LABELS = [
    "plan",
    "planning",
    "architecture",
    "architect",
    "tradeoff",
    "tradeoffs",
    "ambiguous tradeoffs",
    "code",
    "code generation",
    "code_generation",
    "implement",
    "review",
    "code review",
    "code-review",
]


@pytest.mark.parametrize("label", CHEAP_LABELS)
def test_cheap_classes_stay_on_the_cheap_tier(label):
    decision = route(label)
    assert decision.tier == "cheap"
    assert decision.job_class in DEFAULT_TIERS
    assert DEFAULT_TIERS[decision.job_class] == "cheap"
    assert decision.suggestion_accepted is None
    assert decision.reason == f"policy tier for {decision.job_class} is cheap"


@pytest.mark.parametrize("label", FRONTIER_LABELS)
def test_frontier_classes_stay_on_the_frontier_tier(label):
    decision = route(label)
    assert decision.tier == "frontier"
    assert DEFAULT_TIERS[decision.job_class] == "frontier"


def test_every_default_class_has_one_tier():
    assert set(DEFAULT_TIERS) == {
        "parse",
        "extract",
        "index",
        "classify",
        "embed",
        "summarize",
        "status",
        "plan",
        "architecture",
        "tradeoff",
        "code",
        "review",
    }


def test_unknown_class_is_rejected():
    with pytest.raises(UnknownJobClass):
        route("dance")


def test_empty_class_is_rejected():
    with pytest.raises(UnknownJobClass):
        route("   ")


def test_upgrade_suggestion_is_rejected_and_policy_tier_stands():
    decision = route("parse", suggestion_tier="frontier")
    assert decision.tier == "cheap"
    assert decision.suggestion_tier == "frontier"
    assert decision.suggestion_accepted is False
    assert decision.reason == "rejected out-of-policy upgrade from cheap to frontier for parse"


def test_local_alias_matches_the_cheap_tier():
    decision = route("status update", suggestion_tier="local")
    assert decision.job_class == "status"
    assert decision.tier == "cheap"
    assert decision.suggestion_tier == "cheap"
    assert decision.suggestion_accepted is True


def test_downgrade_suggestion_keeps_the_frontier_tier():
    decision = route("code review", suggestion_tier="cheap")
    assert decision.job_class == "review"
    assert decision.tier == "frontier"
    assert decision.suggestion_accepted is False
    assert "policy tier stands" in decision.reason


def test_strong_alias_matches_frontier_policy():
    decision = route("architecture", suggestion_tier="strong")
    assert decision.tier == "frontier"
    assert decision.suggestion_accepted is True


def test_profile_override_is_policy_and_a_lower_suggestion_cannot_replace_it():
    decision = route("summarize", suggestion_tier="cheap", class_tiers={"summarize": "frontier"})
    assert decision.tier == "frontier"
    assert decision.suggestion_accepted is False


def test_profile_override_does_not_move_other_classes():
    decision = route("parse", class_tiers={"summarize": "frontier"})
    assert decision.tier == "cheap"


def test_unknown_suggestion_tier_is_rejected():
    with pytest.raises(UnknownTier):
        route("parse", suggestion_tier="giant")
