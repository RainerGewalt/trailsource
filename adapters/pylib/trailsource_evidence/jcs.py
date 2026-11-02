"""RFC 8785 (JSON Canonicalization Scheme) serialisation.

Object members are sorted by UTF-16 code units, strings use the minimal
escaping of RFC 8785 section 3.2.2.2, integers are emitted verbatim. Floats
are formatted following the ES6 Number::toString rules as far as they are
needed here; the adapters in this repository never place floats into hashed
content (floating point values travel as decimal strings), so this branch
only matters for foreign records.
"""
from __future__ import annotations

import math
from typing import Any, List


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
    elif isinstance(v, int):
        out.append(str(v))
    elif isinstance(v, float):
        out.append(_format_float(v))
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
        keys = sorted(v.keys(), key=lambda k: str(k).encode("utf-16-be"))
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


def _format_float(f: float) -> str:
    if math.isnan(f) or math.isinf(f):
        raise ValueError("jcs: NaN and Infinity are not representable")
    if f == 0:
        return "0"
    if f.is_integer() and abs(f) < 1e21:
        return str(int(f))
    r = repr(f)
    if "e" not in r and "E" not in r:
        return r
    # ES6 exponent notation: mantissa digits without trailing zeros, "e+N"/"e-N".
    mant, exp = r.lower().split("e")
    exp_i = int(exp)
    if -7 < exp_i < 21:
        # repr chose exponent form but ES6 would not; expand via Decimal.
        from decimal import Decimal
        return format(Decimal(r), "f")
    sign = "+" if exp_i >= 0 else "-"
    return "%se%s%d" % (mant, sign, abs(exp_i))
