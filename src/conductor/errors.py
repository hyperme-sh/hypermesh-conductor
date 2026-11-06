"""Errors for routing, configuration, and provider calls."""


class ConductorError(Exception):
    """Base error for the routing and measurement slice."""


class ConfigError(ConductorError):
    """The endpoints file or the process environment is not usable."""


class UnknownJobClass(ConductorError):
    """The step label is not one of the routed job classes."""


class UnknownTier(ConductorError):
    """The tier name is not cheap/local or frontier."""


class ProviderError(ConductorError):
    """The configured endpoint failed or returned usage that cannot be trusted."""

    def __init__(self, message: str, *, status: int | None = None, latency_ms: int | None = None):
        super().__init__(message)
        self.status = status
        self.latency_ms = latency_ms
