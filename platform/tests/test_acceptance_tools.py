"""Fast checks for boundaries of the acceptance runner's measurement code."""

import json
from pathlib import Path
from tempfile import TemporaryDirectory
import unittest

from acceptance_cases import cases
from acceptance_load import latency
from acceptance_matrix import GO_RULES, REQUIREMENTS, RULES, evidence, outcome
from coverage_run import profile_counts


class AcceptanceToolsTest(unittest.TestCase):
    def test_case_ids_map_to_the_registry(self):
        ids = [name for _, name, _ in cases]
        self.assertEqual(len(ids), len(set(ids)))
        for name in ids:
            self.assertTrue(any(name == rule or name.startswith(rule + "-") for rule in RULES), name)

    def test_requirement_links_reference_registered_checks(self):
        expected = {f"FR-{i}" for i in range(1, 25)} | {f"NFR-{i}" for i in range(1, 10)}
        self.assertEqual(REQUIREMENTS.keys(), expected)
        ids = {name for _, name, _ in cases} | GO_RULES.keys()
        for requirement, (status, links, _) in REQUIREMENTS.items():
            if status == "DEFERRED":
                self.assertFalse(links, requirement)
                continue
            self.assertTrue(links, requirement)
            for link in links.split(", "):
                self.assertTrue(any(name == link or name.startswith(link + "-") for name in ids),
                                (requirement, link))

    def test_profile_merges_hits_by_statement_block(self):
        with TemporaryDirectory() as tmp:
            base = Path(tmp) / "go.cover"
            other = Path(tmp) / "app.cover"
            package = "example.com/netdosuga/gateway/"
            first = f"{package}main.go:1.1,1.2 3"
            second = f"{package}main.go:2.1,2.2 1"
            generated = f"{package}api/order.pb.go:1.1,1.2 1000 0"
            base.write_text(f"mode: atomic\n{first} 1\n{second} 0\n{generated}\n", encoding="utf-8")
            other.write_text(f"mode: atomic\n{first} 0\n{second} 4\n{generated}\n", encoding="utf-8")
            self.assertEqual(profile_counts(base, "gateway")["percent"], 75)
            self.assertEqual(profile_counts(base, "gateway", [other]),
                             {"covered": 4, "total": 4, "percent": 100, "status": "PASS"})

    def test_previous_failure_is_preserved(self):
        status = outcome("P0-ID", "functional", "P0", "file.go", "rule",
                         [{"status": "FAIL", "run_id": "first"}, {"status": "PASS", "run_id": "second"}])
        self.assertEqual(status["status"], "FAIL")

    def test_incompatible_sources_are_rejected(self):
        with TemporaryDirectory() as tmp:
            path = Path(tmp) / "acceptance.json"
            path.write_text(json.dumps({"source_sha256": "different"}), encoding="utf-8")
            with self.assertRaises(ValueError):
                evidence([path], "current")

    def test_latency_uses_nearest_rank(self):
        result = latency([4, 1, 2, 3])
        self.assertEqual((result["mean_ms"], result["p50_ms"], result["p95_ms"], result["max_ms"]),
                         (2500, 2000, 4000, 4000))
