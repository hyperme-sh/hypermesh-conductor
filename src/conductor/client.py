"""OpenAI-compatible chat and embeddings calls.

Usage that is missing, partial, or the wrong shape is returned as uncertain.
It is not coerced to zero. Cache-hit tokens stay null unless the provider
reports them.
"""

from __future__ import annotations

import json
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from typing import Any, Callable

from conductor.config import TierEndpoint, auth_headers, method_url
from conductor.errors import ProviderError

Monotonic = Callable[[], float]
Opener = Callable[..., Any]


@dataclass(frozen=True)
class Usage:
    input_tokens: int | None
    output_tokens: int | None
    cache_hit_tokens: int | None
    status: str  # "measured" or "uncertain"


@dataclass(frozen=True)
class CallResult:
    usage: Usage
    latency_ms: int
    content: str | None


def parse_usage(payload: Any, *, embeddings: bool = False) -> Usage:
    """Read token fields from a provider JSON body."""
    if not isinstance(payload, dict):
        return _uncertain()
    usage = payload.get("usage")
    if not isinstance(usage, dict):
        return _uncertain()
    try:
        prompt = _optional_count(usage.get("prompt_tokens"), present="prompt_tokens" in usage)
        completion = _optional_count(
            usage.get("completion_tokens"),
            present="completion_tokens" in usage,
        )
        cache = _cache_hit(usage)
    except _BadCount:
        return _uncertain()
    if embeddings and completion is None and prompt is not None:
        completion = 0
    if prompt is None or completion is None:
        return _uncertain()
    return Usage(
        input_tokens=prompt,
        output_tokens=completion,
        cache_hit_tokens=cache,
        status="measured",
    )


def chat_completion(
    endpoint: TierEndpoint,
    api_key: str | None,
    *,
    messages: list[dict[str, str]],
    tools: list[dict[str, Any]],
    opener: Opener | None = None,
    monotonic: Monotonic | None = None,
) -> CallResult:
    body: dict[str, Any] = {"model": endpoint.model, "messages": messages}
    if tools:
        body["tools"] = tools
    return _post(
        endpoint,
        api_key,
        method="chat/completions",
        body=body,
        embeddings=False,
        content_from=_chat_content,
        opener=opener,
        monotonic=monotonic,
    )


def embeddings(
    endpoint: TierEndpoint,
    api_key: str | None,
    *,
    text: str,
    opener: Opener | None = None,
    monotonic: Monotonic | None = None,
) -> CallResult:
    body = {"model": endpoint.model, "input": text}
    return _post(
        endpoint,
        api_key,
        method="embeddings",
        body=body,
        embeddings=True,
        content_from=lambda _payload: None,
        opener=opener,
        monotonic=monotonic,
    )


def _post(
    endpoint: TierEndpoint,
    api_key: str | None,
    *,
    method: str,
    body: dict[str, Any],
    embeddings: bool,
    content_from: Callable[[Any], str | None],
    opener: Opener | None,
    monotonic: Monotonic | None,
) -> CallResult:
    clock = monotonic or time.perf_counter
    url = method_url(endpoint, method)
    data = json.dumps(body, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")
    request = urllib.request.Request(url, data=data, method="POST")
    for key, value in auth_headers(api_key).items():
        request.add_header(key, value)
    open_url = opener or urllib.request.urlopen
    start = clock()
    try:
        try:
            with open_url(request, timeout=endpoint.timeout_seconds) as response:
                status = getattr(response, "status", None)
                if status is None:
                    status = response.getcode()
                raw = response.read()
        except urllib.error.HTTPError as exc:
            raise ProviderError(f"provider HTTP {exc.code}", status=exc.code) from None
        except (urllib.error.URLError, TimeoutError, OSError):
            raise ProviderError("provider request failed") from None
        if status is None or status >= 400:
            raise ProviderError(f"provider HTTP {status}", status=status)
        try:
            payload = json.loads(raw.decode("utf-8"))
        except (UnicodeError, json.JSONDecodeError):
            raise ProviderError("provider response was not JSON") from None
        usage = parse_usage(payload, embeddings=embeddings)
        content = content_from(payload)
    except ProviderError as exc:
        exc.latency_ms = _elapsed_ms(clock() - start)
        raise
    return CallResult(usage=usage, latency_ms=_elapsed_ms(clock() - start), content=content)


def _chat_content(payload: Any) -> str | None:
    if not isinstance(payload, dict):
        return None
    choices = payload.get("choices")
    if not isinstance(choices, list) or not choices:
        return None
    message = choices[0].get("message") if isinstance(choices[0], dict) else None
    if not isinstance(message, dict):
        return None
    content = message.get("content")
    if content is None:
        return None
    if not isinstance(content, str):
        return None
    return content


def _cache_hit(usage: dict[str, Any]) -> int | None:
    details = usage.get("prompt_tokens_details")
    if isinstance(details, dict) and "cached_tokens" in details and details.get("cached_tokens") is not None:
        return _required_count(details.get("cached_tokens"))
    if "prompt_cache_hit_tokens" in usage and usage.get("prompt_cache_hit_tokens") is not None:
        return _required_count(usage.get("prompt_cache_hit_tokens"))
    return None


def _optional_count(value: Any, *, present: bool) -> int | None:
    if not present or value is None:
        return None
    return _required_count(value)


def _required_count(value: Any) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise _BadCount()
    return value


def _uncertain() -> Usage:
    return Usage(input_tokens=None, output_tokens=None, cache_hit_tokens=None, status="uncertain")


def _elapsed_ms(seconds: float) -> int:
    return int(round(seconds * 1000))


class _BadCount(Exception):
    pass
