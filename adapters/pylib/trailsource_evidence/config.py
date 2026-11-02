"""config_version: SHA-256 over the canonical configuration with masked secrets.

Mirrors the stdlib envelope helper this repository descends from and the Go
implementation in internal/config.
"""
from __future__ import annotations

import os
import re
from typing import Any, Dict, Iterable, List, Optional, Tuple

from .record import sha256_hash

DEFAULT_SECRET_KEYS = {
    "password", "passwd", "pwd", "token", "secret", "api_key", "apikey",
    "username", "user", "connection_string", "private_key_path", "cert_path",
    "identity", "client_key",
}
MASK = "***"
_ENV = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)\}")


def expand_env(text: str) -> Tuple[str, List[str]]:
    missing: List[str] = []

    def repl(m: "re.Match[str]") -> str:
        name = m.group(1)
        if name not in os.environ:
            missing.append(name)
            return ""
        return os.environ[name]

    return _ENV.sub(repl, text), missing


def mask_secrets(value: Any, secret_keys: Optional[Iterable[str]] = None) -> Any:
    keys = {k.lower() for k in (secret_keys or DEFAULT_SECRET_KEYS)}
    if isinstance(value, dict):
        return {k: (MASK if str(k).lower() in keys else mask_secrets(v, keys)) for k, v in value.items()}
    if isinstance(value, list):
        return [mask_secrets(v, keys) for v in value]
    return value


def config_version(config: Dict[str, Any], secret_keys: Optional[Iterable[str]] = None) -> str:
    return sha256_hash(mask_secrets(_normalize(config), secret_keys))


def _normalize(v: Any) -> Any:
    """Make YAML output hashable: dates to strings, non-string keys to strings."""
    if isinstance(v, dict):
        return {str(k): _normalize(val) for k, val in v.items()}
    if isinstance(v, list):
        return [_normalize(e) for e in v]
    if hasattr(v, "isoformat"):
        return v.isoformat()
    return v
