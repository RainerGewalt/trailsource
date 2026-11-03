"""Per-stream chain position persisted across restarts."""
from __future__ import annotations

import json
import os
import threading
from dataclasses import asdict, dataclass
from typing import Any, Dict, Optional, Tuple

from .record import GENESIS_HASH, SCHEMA, format_time, seal, utc_now


@dataclass
class ChainState:
    stream_id: str
    seq: int = 0
    chain_hash: str = GENESIS_HASH
    updated_at: str = ""
    sealed: bool = False


class FileStore:
    def __init__(self, path: str) -> None:
        self.path = path

    def load(self) -> Tuple[Optional[ChainState], bool]:
        if not os.path.exists(self.path):
            return None, False
        with open(self.path, "r", encoding="utf-8") as fh:
            raw = json.load(fh)
        return ChainState(**{k: raw[k] for k in ("stream_id", "seq", "chain_hash", "updated_at", "sealed") if k in raw}), True

    def save(self, state: ChainState) -> None:
        os.makedirs(os.path.dirname(self.path) or ".", exist_ok=True)
        tmp = self.path + ".tmp"
        with open(tmp, "w", encoding="utf-8") as fh:
            json.dump(asdict(state), fh, indent=2)
        os.replace(tmp, self.path)


class MemoryStore:
    def __init__(self) -> None:
        self.state: Optional[ChainState] = None

    def load(self) -> Tuple[Optional[ChainState], bool]:
        return self.state, self.state is not None

    def save(self, state: ChainState) -> None:
        self.state = ChainState(**asdict(state))


class Sealer:
    """Assigns seq/prev_hash/chain_restart, seals and persists the position.

    The first record sealed after construction carries chain_restart=true and
    anchors to the last persisted chain_hash (or genesis).
    """

    def __init__(self, store: Any, producer: Dict[str, str], stream_id: str) -> None:
        state, found = store.load()
        if found and state.stream_id != stream_id:
            raise ValueError("chain state belongs to stream %r, not %r" % (state.stream_id, stream_id))
        self._store = store
        self._producer = dict(producer)
        self._stream = stream_id
        self._state = state if found else ChainState(stream_id=stream_id)
        self._restart = True
        self._lock = threading.Lock()

    @property
    def position(self) -> Tuple[int, str, bool]:
        return self._state.seq, self._state.chain_hash, self._state.sealed

    def seal(self, record: Dict[str, Any]) -> Dict[str, Any]:
        with self._lock:
            record["schema"] = SCHEMA
            record["producer"] = dict(self._producer)
            record["stream_id"] = self._stream
            time_block = record.setdefault("time", {})
            time_block["recorded_at"] = format_time(utc_now())
            time_block.setdefault("source", "system")
            anchor = self._state.chain_hash if self._state.sealed else GENESIS_HASH
            if self._restart:
                record["chain_restart"] = True
                record["anchor_hash"] = anchor
            else:
                record.pop("chain_restart", None)
                record.pop("anchor_hash", None)
            record["prev_hash"] = anchor
            seq = self._state.seq + 1 if self._state.sealed else 0
            record["seq"] = str(seq)  # canonical decimal string on the wire
            seal(record)
            nxt = ChainState(self._stream, seq, record["chain_hash"], time_block["recorded_at"], True)
            self._store.save(nxt)
            self._state = nxt
            self._restart = False
            return record
