"""Load OpenAI-compatible endpoints from a file. Keys come from the environment."""

from __future__ import annotations

import json
import os
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Mapping
from urllib.parse import urlparse

from conductor.errors import ConfigError
from conductor.prefix import Prefix, build_prefix
from conductor.routing import CHEAP, DEFAULT_TIERS, FRONTIER, canonicalize_tier

_FORBIDDEN_KEYS = {
    "api_key",
    "apikey",
    "api_token",
    "access_token",
    "authorization",
    "bearer",
    "password",
    "secret",
    "token",
}

_ENV_NAME = "api_key_env"


@dataclass(frozen=True)
class TierEndpoint:
    tier: str
    base_url: str
    model: str
    api_key_env: str | None
    timeout_seconds: float

    def api_key(self) -> str | None:
        if self.api_key_env is None:
            return None
        value = os.environ.get(self.api_key_env)
        if value is None or value.strip() == "":
            raise ConfigError(f"{self.api_key_env} is not set")
        return value


@dataclass(frozen=True)
class CostRate:
    input_per_million: float
    output_per_million: float
    source: str


@dataclass(frozen=True)
class ConductorConfig:
    cheap: TierEndpoint
    frontier: TierEndpoint
    prefix: Prefix
    class_tiers: dict[str, str]
    cost_rates: dict[str, CostRate]

    def endpoint_for(self, tier: str) -> TierEndpoint:
        canonical = canonicalize_tier(tier)
        if canonical == CHEAP:
            return self.cheap
        return self.frontier

    def rate_for(self, tier: str) -> CostRate | None:
        return self.cost_rates.get(canonicalize_tier(tier))


def load_config(path: str | Path) -> ConductorConfig:
    file_path = Path(path)
    try:
        raw = json.loads(file_path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise ConfigError(f"config file not found: {file_path}") from exc
    except json.JSONDecodeError as exc:
        raise ConfigError(f"config file is not JSON: {file_path}") from exc
    if not isinstance(raw, dict):
        raise ConfigError("config root must be an object")
    _reject_secret_keys(raw, where="config")
    tiers = raw.get("tiers")
    if not isinstance(tiers, dict):
        raise ConfigError("config tiers must be an object with cheap and frontier")
    for name, block in tiers.items():
        if isinstance(block, dict):
            _reject_secret_keys(block, where=f"tiers.{name}")
    cheap = _endpoint(CHEAP, tiers.get(CHEAP))
    frontier = _endpoint(FRONTIER, tiers.get(FRONTIER))
    prefix_block = raw.get("prefix")
    if not isinstance(prefix_block, dict):
        raise ConfigError("config prefix must be an object with system and tools")
    prefix = build_prefix(prefix_block.get("system", ""), prefix_block.get("tools") or [])
    class_tiers = _class_tiers(raw.get("class_tiers") or {})
    cost_rates = _cost_rates(raw.get("cost_proxy_rates"))
    return ConductorConfig(
        cheap=cheap,
        frontier=frontier,
        prefix=prefix,
        class_tiers=class_tiers,
        cost_rates=cost_rates,
    )


def _endpoint(tier: str, block: Any) -> TierEndpoint:
    if not isinstance(block, dict):
        raise ConfigError(f"tiers.{tier} must be an object")
    base_url = block.get("base_url")
    model = block.get("model")
    if not isinstance(base_url, str) or base_url.strip() == "":
        raise ConfigError(f"tiers.{tier}.base_url must be a string")
    if not isinstance(model, str) or model.strip() == "":
        raise ConfigError(f"tiers.{tier}.model must be a string")
    parsed = urlparse(base_url)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise ConfigError(f"tiers.{tier}.base_url must be an http or https URL")
    if parsed.username or parsed.password:
        raise ConfigError(f"tiers.{tier}.base_url must not carry credentials")
    path = parsed.path.rstrip("/")
    if path.endswith("/chat/completions") or path.endswith("/embeddings"):
        raise ConfigError(f"tiers.{tier}.base_url is the API root, such as https://host/v1")
    api_key_env = block.get(_ENV_NAME)
    if api_key_env is not None:
        if not isinstance(api_key_env, str) or not _is_env_name(api_key_env):
            raise ConfigError(f"tiers.{tier}.api_key_env must name an environment variable")
    timeout = block.get("timeout_seconds", 60)
    if isinstance(timeout, bool) or not isinstance(timeout, (int, float)) or timeout <= 0:
        raise ConfigError(f"tiers.{tier}.timeout_seconds must be a positive number")
    return TierEndpoint(
        tier=tier,
        base_url=base_url.rstrip("/"),
        model=model,
        api_key_env=api_key_env,
        timeout_seconds=float(timeout),
    )


def _class_tiers(block: Any) -> dict[str, str]:
    if not isinstance(block, dict):
        raise ConfigError("class_tiers must be an object")
    resolved: dict[str, str] = {}
    for name, tier in block.items():
        if name not in DEFAULT_TIERS:
            raise ConfigError(f"class_tiers has unknown job class {name!r}")
        if not isinstance(tier, str):
            raise ConfigError(f"class_tiers.{name} must be cheap or frontier")
        resolved[name] = canonicalize_tier(tier)
    return resolved


def _cost_rates(block: Any) -> dict[str, CostRate]:
    if block is None:
        return {}
    if not isinstance(block, dict):
        raise ConfigError("cost_proxy_rates must be an object")
    rates: dict[str, CostRate] = {}
    for tier, spec in block.items():
        canonical = canonicalize_tier(str(tier))
        if not isinstance(spec, dict):
            raise ConfigError(f"cost_proxy_rates.{tier} must be an object")
        source = spec.get("source")
        if not isinstance(source, str) or source.strip() == "":
            raise ConfigError(f"cost_proxy_rates.{tier}.source must name the published rate")
        rates[canonical] = CostRate(
            input_per_million=_rate_number(spec.get("input_per_million"), f"{tier}.input_per_million"),
            output_per_million=_rate_number(spec.get("output_per_million"), f"{tier}.output_per_million"),
            source=source.strip(),
        )
    return rates


def _rate_number(value: Any, field: str) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)) or value < 0:
        raise ConfigError(f"cost_proxy_rates.{field} must be a non-negative number")
    return float(value)


def _is_env_name(value: str) -> bool:
    if not value or value[0].isdigit():
        return False
    return all(char.isalnum() or char == "_" for char in value)


def _reject_secret_keys(value: Mapping[str, Any], *, where: str) -> None:
    for key in value:
        if str(key).lower() in _FORBIDDEN_KEYS:
            raise ConfigError(
                f"{where} must not contain {key}; name an environment variable in api_key_env"
            )


def method_url(endpoint: TierEndpoint, method: str) -> str:
    return f"{endpoint.base_url}/{method}"


def auth_headers(api_key: str | None) -> dict[str, str]:
    headers = {"Content-Type": "application/json"}
    if api_key is not None:
        headers["Authorization"] = f"Bearer {api_key}"
    return headers
