import json
from pathlib import Path

import pytest

from conductor.config import load_config
from conductor.errors import ConfigError
from conductor.runner import execute_step, preview

EXAMPLE = Path(__file__).resolve().parents[1] / "examples" / "endpoints.json"


class _Response:
    def __init__(self, status, payload):
        self.status = status
        self._raw = json.dumps(payload).encode("utf-8")

    def read(self):
        return self._raw

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        return False


def _config(monkeypatch):
    monkeypatch.setenv("CONDUCTOR_CHEAP_API_KEY", "test-key-do-not-log")
    monkeypatch.setenv("CONDUCTOR_FRONTIER_API_KEY", "frontier-key-do-not-log")
    return load_config(EXAMPLE)


def _opener(calls, payload, status=200):
    def opener(request, timeout):
        calls.append(
            {
                "url": request.full_url,
                "body": json.loads(request.data.decode("utf-8")),
                "authorization": request.get_header("Authorization"),
                "timeout": timeout,
            }
        )
        return _Response(status, payload)

    return opener


def _clock():
    ticks = iter([10.0, 10.042])

    def clock():
        return next(ticks)

    return clock


def test_parse_and_code_use_different_tiers_and_the_same_prefix(monkeypatch):
    config = _config(monkeypatch)
    calls = []
    payload = {
        "choices": [{"message": {"role": "assistant", "content": "secret-answer"}}],
        "usage": {"prompt_tokens": 30, "completion_tokens": 4, "prompt_tokens_details": {"cached_tokens": 12}},
    }
    parse = execute_step(
        config,
        task_id="task-1",
        step_id="parse-1",
        job_class="parse",
        messages=[{"role": "user", "content": "name the fields"}],
        opener=_opener(calls, payload),
        monotonic=_clock(),
    )
    code = execute_step(
        config,
        task_id="task-1",
        step_id="code-1",
        job_class="code",
        messages=[{"role": "user", "content": "implement the parser"}],
        suggestion_tier="cheap",
        opener=_opener(calls, payload),
        monotonic=_clock(),
    )
    assert parse.record.tier == "cheap"
    assert parse.record.model_id == "local-small"
    assert parse.record.input_tokens == 30
    assert parse.record.cache_hit_tokens == 12
    assert parse.record.latency_ms == 42
    assert parse.content == "secret-answer"
    assert code.record.tier == "frontier"
    assert code.record.suggestion_accepted is False
    assert "policy tier stands" in code.record.reason
    assert calls[0]["url"] == "http://127.0.0.1:8080/v1/chat/completions"
    assert calls[1]["url"] == "http://127.0.0.1:8081/v1/chat/completions"
    assert calls[0]["authorization"] == "Bearer test-key-do-not-log"
    assert calls[0]["body"]["messages"][0] == calls[1]["body"]["messages"][0]
    assert calls[0]["body"]["tools"] == calls[1]["body"]["tools"]
    assert calls[0]["body"]["messages"][1]["content"] == "name the fields"
    assert parse.record.prefix_sha256 == code.record.prefix_sha256 == config.prefix.sha256()


def test_rejected_upgrade_stays_on_the_cheap_endpoint(monkeypatch):
    config = _config(monkeypatch)
    calls = []
    execute_step(
        config,
        task_id="task-1",
        step_id="status-1",
        job_class="status update",
        messages=[{"role": "user", "content": "status"}],
        suggestion_tier="frontier",
        opener=_opener(calls, {"usage": {"prompt_tokens": 3, "completion_tokens": 1}}),
    )
    assert calls[0]["url"].startswith("http://127.0.0.1:8080/")
    assert len(calls) == 1


def test_provider_failure_does_not_call_the_other_tier(monkeypatch, tmp_path):
    config = _config(monkeypatch)
    calls = []
    journal = tmp_path / "journal.jsonl"
    result = execute_step(
        config,
        task_id="task-1",
        step_id="index-1",
        job_class="index",
        messages=[{"role": "user", "content": "index the note"}],
        journal_path=journal,
        opener=_opener(calls, {"error": "nope"}, status=500),
    )
    assert len(calls) == 1
    assert calls[0]["url"].startswith("http://127.0.0.1:8080/")
    assert result.record.usage_status == "uncertain"
    assert result.record.input_tokens is None
    assert result.record.output_tokens is None
    assert result.record.cost_proxy is None
    assert result.record.error == "provider HTTP 500"
    stored = journal.read_text(encoding="utf-8")
    assert "test-key-do-not-log" not in stored
    assert "secret-answer" not in stored
    assert json.loads(stored)["usage_status"] == "uncertain"


def test_missing_usage_stays_null_in_the_journal(monkeypatch, tmp_path):
    config = _config(monkeypatch)
    journal = tmp_path / "journal.jsonl"
    result = execute_step(
        config,
        task_id="task-1",
        step_id="sum-1",
        job_class="summarize",
        messages=[{"role": "user", "content": "short"}],
        journal_path=journal,
        opener=_opener([], {"choices": [{"message": {"content": "done"}}]}),
    )
    assert result.record.usage_status == "uncertain"
    assert result.record.input_tokens is None
    assert result.content == "done"
    assert "done" not in journal.read_text(encoding="utf-8")


def test_missing_key_does_not_call_or_journal(monkeypatch, tmp_path):
    monkeypatch.delenv("CONDUCTOR_CHEAP_API_KEY", raising=False)
    monkeypatch.setenv("CONDUCTOR_FRONTIER_API_KEY", "frontier-key-do-not-log")
    config = load_config(EXAMPLE)
    calls = []
    journal = tmp_path / "journal.jsonl"
    with pytest.raises(ConfigError, match="CONDUCTOR_CHEAP_API_KEY"):
        execute_step(
            config,
            task_id="task-1",
            step_id="parse-1",
            job_class="parse",
            messages=[{"role": "user", "content": "parse"}],
            journal_path=journal,
            opener=_opener(calls, {}),
        )
    assert calls == []
    assert not journal.exists()


def test_embed_uses_the_embeddings_method(monkeypatch):
    config = _config(monkeypatch)
    calls = []
    result = execute_step(
        config,
        task_id="task-1",
        step_id="embed-1",
        job_class="embed",
        embedding_input="a note",
        opener=_opener(calls, {"data": [{"embedding": [0.2]}], "usage": {"prompt_tokens": 8, "total_tokens": 8}}),
    )
    assert calls[0]["url"] == "http://127.0.0.1:8080/v1/embeddings"
    assert calls[0]["body"] == {"input": "a note", "model": "local-small"}
    assert "embedding" not in json.dumps(result.record.to_dict())
    assert result.record.input_tokens == 8
    assert result.record.output_tokens == 0
    assert result.content is None


def test_priced_call_is_labeled_as_a_proxy(monkeypatch, tmp_path):
    payload = json.loads(EXAMPLE.read_text(encoding="utf-8"))
    payload["cost_proxy_rates"] = {
        "cheap": {"input_per_million": 2, "output_per_million": 4, "source": "published card"},
        "frontier": {"input_per_million": 3, "output_per_million": 6, "source": "published card"},
    }
    path = tmp_path / "endpoints.json"
    path.write_text(json.dumps(payload), encoding="utf-8")
    monkeypatch.setenv("CONDUCTOR_CHEAP_API_KEY", "test-key-do-not-log")
    monkeypatch.setenv("CONDUCTOR_FRONTIER_API_KEY", "frontier-key-do-not-log")
    result = execute_step(
        load_config(path),
        task_id="task-1",
        step_id="parse-1",
        job_class="parse",
        messages=[{"role": "user", "content": "parse"}],
        opener=_opener([], {"usage": {"prompt_tokens": 1_000_000, "completion_tokens": 500_000}}),
    )
    assert result.record.cost_proxy == pytest.approx(4.0)
    assert result.record.cost_proxy_label == "proxy: tokens × published rate (published card); not a charge"


def test_preview_names_the_model_and_omits_the_endpoint(monkeypatch):
    config = _config(monkeypatch)
    shown = preview(config, "code review", suggestion_tier="cheap")
    assert shown["job_class"] == "review"
    assert shown["tier"] == "frontier"
    assert shown["model_id"] == "frontier"
    assert shown["suggestion_accepted"] is False
    assert "8081" not in json.dumps(shown)
    assert "key" not in json.dumps(shown)
