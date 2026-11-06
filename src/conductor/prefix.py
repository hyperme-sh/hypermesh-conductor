"""Stable cacheable prefix: system prompt plus tool definitions.

Step text is appended after this prefix. The prefix bytes do not include the
task id, the step id, timestamps, or the user message. Compaction of the tail
is a later change; this module only keeps the prefix canonical and hashable.
"""

from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass
from typing import Any

from conductor.errors import ConfigError


@dataclass(frozen=True)
class Prefix:
    system: str
    tools: tuple[dict[str, Any], ...]

    def material(self) -> bytes:
        payload = {
            "system": self.system,
            "tools": [canonicalize(tool) for tool in self.tools],
        }
        return json.dumps(payload, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")

    def sha256(self) -> str:
        return hashlib.sha256(self.material()).hexdigest()

    def wire_tools(self) -> list[dict[str, Any]]:
        return [canonicalize(tool) for tool in self.tools]


def canonicalize(value: Any) -> Any:
    """Recursively sort object keys so equivalent tool definitions share bytes."""
    if isinstance(value, dict):
        return {key: canonicalize(value[key]) for key in sorted(value)}
    if isinstance(value, list):
        return [canonicalize(item) for item in value]
    if isinstance(value, bool) or value is None or isinstance(value, (str, int, float)):
        return value
    raise ConfigError(f"prefix value of type {type(value).__name__} is not JSON")


def build_prefix(system: str, tools: list[dict[str, Any]] | None) -> Prefix:
    if not isinstance(system, str) or system == "":
        raise ConfigError("prefix system prompt must be a non-empty string")
    raw_tools = tools or []
    if not isinstance(raw_tools, list):
        raise ConfigError("prefix tools must be a list")
    normalized: list[dict[str, Any]] = []
    for tool in raw_tools:
        if not isinstance(tool, dict):
            raise ConfigError("each tool definition must be an object")
        normalized.append(canonicalize(tool))
    names = []
    for tool in normalized:
        function = tool.get("function")
        name = function.get("name") if isinstance(function, dict) else None
        if not isinstance(name, str) or name == "":
            raise ConfigError("each tool needs a function name")
        names.append(name)
    if len(names) != len(set(names)):
        raise ConfigError("tool function names must be unique")
    ordered = tuple(sorted(normalized, key=lambda tool: tool["function"]["name"]))
    return Prefix(system=system, tools=ordered)


def build_messages(prefix: Prefix, step_messages: list[dict[str, str]]) -> list[dict[str, str]]:
    """Place the stable system message ahead of the step. Steps cannot replace it."""
    prepared: list[dict[str, str]] = []
    for message in step_messages:
        if not isinstance(message, dict):
            raise ConfigError("each step message must be an object")
        role = message.get("role")
        content = message.get("content")
        if role == "system":
            raise ConfigError("step messages cannot include a system role; the prefix owns it")
        if role not in {"user", "assistant", "tool"}:
            raise ConfigError(f"unsupported step message role {role!r}")
        if not isinstance(content, str):
            raise ConfigError("step message content must be a string")
        prepared.append({"role": role, "content": content})
    if not prepared:
        raise ConfigError("a chat step needs at least one message after the prefix")
    return [{"role": "system", "content": prefix.system}, *prepared]
