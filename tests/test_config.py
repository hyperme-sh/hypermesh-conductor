import json
from pathlib import Path

import pytest

from conductor.config import load_config
from conductor.errors import ConfigError

EXAMPLE = Path(__file__).resolve().parents[1] / "examples" / "endpoints.json"


def _write(tmp_path, payload):
    path = tmp_path / "endpoints.json"
    path.write_text(json.dumps(payload), encoding="utf-8")
    return path


def _base():
    return json.loads(EXAMPLE.read_text(encoding="utf-8"))


def test_example_config_loads_without_keys():
    config = load_config(EXAMPLE)
    assert config.cheap.model == "local-small"
    assert config.frontier.model == "frontier"
    assert config.cheap.api_key_env == "CONDUCTOR_CHEAP_API_KEY"
    assert config.prefix.system.startswith("You are Conductor.")
    assert config.prefix.sha256()


def test_inline_api_key_is_rejected(tmp_path):
    payload = _base()
    payload["tiers"]["cheap"]["api_key"] = "sk-test"
    with pytest.raises(ConfigError, match="api_key"):
        load_config(_write(tmp_path, payload))


def test_credentials_in_the_url_are_rejected(tmp_path):
    payload = _base()
    payload["tiers"]["frontier"]["base_url"] = "https://user:secret@example.test/v1"
    with pytest.raises(ConfigError, match="credentials"):
        load_config(_write(tmp_path, payload))


def test_method_url_is_rejected_as_the_api_root(tmp_path):
    payload = _base()
    payload["tiers"]["cheap"]["base_url"] = "http://127.0.0.1:8080/v1/chat/completions"
    with pytest.raises(ConfigError, match="API root"):
        load_config(_write(tmp_path, payload))


def test_unknown_class_tier_is_rejected(tmp_path):
    payload = _base()
    payload["class_tiers"] = {"dance": "cheap"}
    with pytest.raises(ConfigError, match="unknown job class"):
        load_config(_write(tmp_path, payload))


def test_cost_rates_require_a_source(tmp_path):
    payload = _base()
    payload["cost_proxy_rates"] = {"cheap": {"input_per_million": 1, "output_per_million": 2}}
    with pytest.raises(ConfigError, match="source"):
        load_config(_write(tmp_path, payload))
