import json
import os
import unittest

from trailsource_evidence import canonical_jcs, core_of, seal, sha256_hash, verify_records

HERE = os.path.dirname(os.path.abspath(__file__))
VECTORS = os.path.join(HERE, "..", "..", "..", "testdata", "vectors.json")


class VectorTests(unittest.TestCase):
    def setUp(self):
        with open(VECTORS, "r", encoding="utf-8") as fh:
            self.vectors = json.load(fh)
        self.assertGreaterEqual(len(self.vectors), 3)

    def test_vectors_reproduce_byte_for_byte(self):
        sealed = {}
        for v in self.vectors:
            rec = dict(v["record"])
            rec.pop("content_hash", None)
            rec.pop("chain_hash", None)
            seal(rec)
            self.assertEqual(rec["content_hash"], v["expected_content_hash"], v["name"])
            self.assertEqual(rec["chain_hash"], v["expected_chain_hash"], v["name"])
            self.assertEqual(canonical_jcs(core_of(rec)).decode("utf-8"), v["expected_core_jcs"], v["name"])
            single = verify_records([rec])
            self.assertTrue(single.ok, (v["name"], single.issues))
            sealed[v["name"]] = rec
        report = verify_records([sealed["genesis-minimal"], sealed["unicode-and-escaping"]])
        self.assertTrue(report.ok, report.issues)
        self.assertEqual(report.streams["vec"]["restarts"], 1)

    def test_payload_hash_matches_spec_example(self):
        self.assertEqual(canonical_jcs({"b": 1, "a": "x"}), b'{"a":"x","b":1}')
        self.assertEqual(sha256_hash({"b": 1, "a": "x"}), self.vectors[0]["expected_content_hash"])


if __name__ == "__main__":
    unittest.main()
