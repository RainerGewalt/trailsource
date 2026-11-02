import os
import tempfile
import unittest

from trailsource_evidence import GENESIS_HASH, FileStore, Sealer, canonical_jcs, no_policy, verify_records
from trailsource_evidence.config import config_version, expand_env, mask_secrets


class JCSTests(unittest.TestCase):
    def test_sorting_and_escaping(self):
        self.assertEqual(canonical_jcs({"b": 1, "a": "x", "c": [2, 1]}), b'{"a":"x","b":1,"c":[2,1]}')
        self.assertEqual(canonical_jcs({"k": 'a"b\\c\nd\te\x01'}), b'{"k":"a\\"b\\\\c\\nd\\te\\u0001"}')
        self.assertEqual(canonical_jcs({"n": 9007199254740993}), b'{"n":9007199254740993}')

    def test_utf16_key_order(self):
        # U+1D11E (surrogate pair, lead 0xD834) sorts before U+FF21 in UTF-16.
        got = canonical_jcs({"\U0001D11E": 1, "Ａ": 2}).decode("utf-8")
        self.assertEqual(got, '{"\U0001D11E":1,"Ａ":2}')

    def test_float_formatting_follows_es6(self):
        self.assertEqual(canonical_jcs([1.0, 0.5, 1e21, 1e-7]), b"[1,0.5,1e+21,1e-7]")


def _record(i):
    return {"event_type": "test.value.observed", "actor": {"auth": "self-declared", "id": "t"},
            "policy": no_policy(), "payload": {"i": i}}


class ChainTests(unittest.TestCase):
    def test_seq_persists_across_restart(self):
        with tempfile.TemporaryDirectory() as d:
            store = FileStore(os.path.join(d, "state", "s.json"))
            producer = {"kind": "source-bridge", "id": "p", "version": "test"}
            s1 = Sealer(store, producer, "s")
            recs = [s1.seal(_record(i)) for i in range(3)]
            self.assertTrue(recs[0]["chain_restart"])
            self.assertEqual(recs[0]["anchor_hash"], GENESIS_HASH)
            self.assertFalse(recs[1].get("chain_restart", False))
            self.assertEqual(recs[1]["prev_hash"], recs[0]["chain_hash"])

            s2 = Sealer(store, producer, "s")
            r = s2.seal(_record(3))
            self.assertEqual(r["seq"], 3)
            self.assertTrue(r["chain_restart"])
            self.assertEqual(r["anchor_hash"], recs[2]["chain_hash"])
            r2 = s2.seal(_record(4))
            self.assertEqual((r2["seq"], r2["prev_hash"]), (4, r["chain_hash"]))

            report = verify_records(recs + [r, r2])
            self.assertTrue(report.ok, report.issues)
            self.assertEqual(report.streams["s"]["restarts"], 2)

            recs[1]["payload"]["i"] = 99
            self.assertFalse(verify_records(recs).ok)

    def test_foreign_stream_rejected(self):
        with tempfile.TemporaryDirectory() as d:
            store = FileStore(os.path.join(d, "s.json"))
            Sealer(store, {"kind": "source-bridge", "id": "p", "version": "t"}, "a").seal(_record(0))
            with self.assertRaises(ValueError):
                Sealer(store, {"kind": "source-bridge", "id": "p", "version": "t"}, "b")


class ConfigTests(unittest.TestCase):
    def test_mask_and_version(self):
        cfg = {"mqtt": {"broker": "tcp://x:1883", "password": "a", "username": "u"}, "points": [{"name": "p"}]}
        self.assertEqual(mask_secrets(cfg)["mqtt"]["password"], "***")
        v1 = config_version(cfg)
        cfg["mqtt"]["password"] = "rotated"
        self.assertEqual(v1, config_version(cfg))
        cfg["mqtt"]["broker"] = "tcp://y:1883"
        self.assertNotEqual(v1, config_version(cfg))

    def test_expand_env(self):
        os.environ["TS_PY_TEST"] = "v"
        text, missing = expand_env("a: ${TS_PY_TEST}\nb: ${TS_PY_UNSET}")
        self.assertEqual(text, "a: v\nb: ")
        self.assertEqual(missing, ["TS_PY_UNSET"])


if __name__ == "__main__":
    unittest.main()
