"""RFC 8785 (JSON Canonicalization Scheme) serialisation, stdlib only.

Object members are sorted by UTF-16 code units, strings use the minimal
escaping of RFC 8785 section 3.2.2.2, numbers are rendered like ECMAScript
Number.prototype.toString. Integers outside +/-2**53 are treated as IEEE-754
doubles (9007199254740993 -> 9007199254740992), exactly as the Go
implementation (github.com/gowebpki/jcs) and the trail-evidence reference do.
Producers therefore transport 64-bit integers and rounding-sensitive decimals
as strings.

The number and sorting logic follows the trail-evidence reference Python
package; TODO(trail-evidence): replace this module with that package once it
is published.
"""
from __future__ import annotations

import math
from decimal import Decimal
from typing import Any, List

_MAX_SAFE = 2 ** 53


def canonical_jcs(value: Any) -> bytes:
    out: List[str] = []
    _write(out, value)
    return "".join(out).encode("utf-8")


def _write(out: List[str], v: Any) -> None:
    if v is None:
        out.append("null")
    elif v is True:
        out.append("true")
    elif v is False:
        out.append("false")
    elif isinstance(v, (int, float)):
        out.append(format_number(v))
    elif isinstance(v, str):
        out.append(_escape(v))
    elif isinstance(v, (list, tuple)):
        out.append("[")
        for i, e in enumerate(v):
            if i:
                out.append(",")
            _write(out, e)
        out.append("]")
    elif isinstance(v, dict):
        keys = sorted(v.keys(), key=lambda k: str(k).encode("utf-16-be", "surrogatepass"))
        out.append("{")
        for i, k in enumerate(keys):
            if i:
                out.append(",")
            out.append(_escape(str(k)))
            out.append(":")
            _write(out, v[k])
        out.append("}")
    else:
        raise TypeError("jcs: unsupported type %s" % type(v).__name__)


_SHORT = {'"': '\\"', "\\": "\\\\", "\b": "\\b", "\f": "\\f", "\n": "\\n", "\r": "\\r", "\t": "\\t"}


def _escape(s: str) -> str:
    parts = ['"']
    for ch in s:
        if ch in _SHORT:
            parts.append(_SHORT[ch])
        elif ord(ch) < 0x20:
            parts.append("\\u%04x" % ord(ch))
        else:
            parts.append(ch)
    parts.append('"')
    return "".join(parts)


def format_number(value: Any) -> str:
    """Render a number like ECMAScript Number.prototype.toString."""
    if isinstance(value, int):
        if -_MAX_SAFE <= value <= _MAX_SAFE:
            return str(value)
        value = float(value)
    if math.isnan(value) or math.isinf(value):
        raise ValueError("jcs: NaN and Infinity are not valid JSON numbers")
    if value == 0:
        return "0"
    sign = "-" if value < 0 else ""
    tup = Decimal(repr(abs(value))).as_tuple()
    digits = "".join(str(d) for d in tup.digits).rstrip("0")
    exponent = int(tup.exponent) + (len(tup.digits) - len(digits))
    k = len(digits)
    n = k + exponent  # value = 0.digits * 10**n
    if k <= n <= 21:
        body = digits + "0" * (n - k)
    elif 0 < n <= 21:
        body = digits[:n] + "." + digits[n:]
    elif -6 < n <= 0:
        body = "0." + "0" * (-n) + digits
    else:
        e = n - 1
        mant = digits[0] + ("." + digits[1:] if k > 1 else "")
        body = "%se%s%d" % (mant, "+" if e >= 0 else "-", abs(e))
    return sign + body
