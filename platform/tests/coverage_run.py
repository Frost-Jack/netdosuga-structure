"""Run Go tests and gate statement coverage and mandatory database test skips."""

import argparse
import json
import os
import re
import subprocess
import time
from pathlib import Path
from uuid import uuid4

from acceptance_run import GO_MODULES, PLATFORM, ROOT, command, source_hash

MODULES = GO_MODULES
DB_TESTS = {"TestIdentityDatabase", "TestBusinessDatabase", "TestLegacyPaidUpgrade", "TestDurableMockEffects",
            "TestBusinessDurableDelivery", "TestWritableReadinessRejectsAndDiscardsReadOnlySession",
            "TestWritableRejectsAndRetiresReadOnlySession"}
COVERAGE_LINE = re.compile(r"(?P<file>.+):[\d.]+,[\d.]+ (?P<statements>\d+) (?P<hits>\d+)")
GENERATED = re.compile(r"^// Code generated .* DO NOT EDIT\.$", re.MULTILINE)


def profile_counts(profile, module, extra_profiles=()):
    module_path = (ROOT / module / "go.mod").read_text(encoding="utf-8").splitlines()[0].split()[1]
    generated = {}
    blocks = {}
    for input_profile in (profile, *extra_profiles):
        lines = input_profile.read_text(encoding="utf-8").splitlines()
        if not lines or lines[0] != "mode: atomic":
            raise ValueError(f"unexpected profile format: {input_profile.name}")
        for line in lines[1:]:
            file, count, hits = COVERAGE_LINE.fullmatch(line).groups()
            if file not in generated:
                if not file.startswith(module_path + "/"):
                    raise ValueError(f"profile contains unexpected package: {file}")
                source = ROOT / module / file.removeprefix(module_path + "/")
                generated[file] = bool(GENERATED.search(source.read_text(encoding="utf-8"))) or file.endswith("_test.go")
            if generated[file]:
                continue
            location = line.split()[0]
            old_count, old_hit = blocks.get(location, (int(count), False))
            if old_count != int(count):
                raise ValueError(f"profile statement count differs at {location}")
            blocks[location] = (old_count, old_hit or int(hits) > 0)
    total = sum(count for count, _ in blocks.values())
    covered = sum(count for count, executed in blocks.values() if executed)
    if total == 0:
        raise ValueError(f"profile contains no first-party statements: {profile.name}")
    return {"covered": covered, "total": total, "percent": round(100 * covered / total, 2),
            "status": "PASS" if covered * 100 >= total * 85 else "FAIL"}


def test_results(path):
    results = []
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line.startswith("{"):
            continue
        entry = json.loads(line)
        action = entry.get("Action")
        if action in {"pass", "fail", "skip"} and entry.get("Test"):
            results.append({"test": entry["Test"], "package": entry["Package"], "status": action.upper(),
                            "seconds": entry.get("Elapsed")})
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--local-go", type=Path,
                        help="Use this host go executable with the disposable Compose PostgreSQL.")
    parser.add_argument("--app-run", type=Path, action="append", default=[],
                        help="Merge counters from this instrumented acceptance run directory (repeatable).")
    args = parser.parse_args()
    run_id = uuid4().hex[:12]
    project = f"netdosuga-go-{run_id}"
    directory = PLATFORM / "test-results" / run_id
    directory.mkdir(parents=True, exist_ok=False)
    env = dict(os.environ, COVERAGE_RESULTS=directory.as_posix())
    report = {"run_id": run_id, "source_sha256": source_hash(), "status": "BLOCKED", "modules": {}}
    started = time.monotonic()

    def docker(*args, **kwargs):
        return command(["docker", "compose", "-p", project, "-f", PLATFORM / "compose.coverage.yaml", *args], env=env, **kwargs)

    owned = False
    try:
        for path in args.app_run:
            acceptance = json.loads((path / "acceptance.json").read_text(encoding="utf-8"))
            if acceptance["source_sha256"] != report["source_sha256"] or acceptance.get("error"):
                raise ValueError(f"incompatible application coverage source: {path}")
        report["application_runs"] = [path.name for path in args.app_run]
        for kind in ("container", "network", "volume"):
            if command(["docker", kind, "ls", "-q", "--filter", f"label=com.docker.compose.project={project}"]).strip():
                raise RuntimeError(f"Found existing resources for {project}")
        owned = True
        docker("up", "-d", "--wait", "--wait-timeout", "60", "coverage-db", capture=False, timeout=300)
        for database in ("payment_tests", "readiness_tests"):
            docker("exec", "-T", "coverage-db", "psql", "-U", "coverage", "-d", "service_tests",
                   "-v", "ON_ERROR_STOP=1", "-c", f"CREATE DATABASE {database}")
        report["postgres_version"] = docker("exec", "-T", "coverage-db", "psql", "-U", "coverage",
                                            "-d", "service_tests", "-Atc", "SHOW server_version").strip()
        report["setup_seconds"] = round(time.monotonic() - started, 3)
        if args.local_go:
            db = docker("port", "coverage-db", "5432").strip()
            if not re.fullmatch(r"127\.0\.0\.1:\d+", db):
                raise ValueError(f"Unexpected database endpoint: {db}")
            prefix = f"postgres://coverage:coverage@{db}/"
            local_env = dict(env, TEST_DATABASE_URL=prefix + "service_tests?sslmode=disable",
                             READINESS_TEST_DATABASE_URL=prefix + "readiness_tests?sslmode=disable",
                             PAYMENT_TEST_DATABASE_URL=prefix + "payment_tests?sslmode=disable")
            passed = True
            for module in MODULES:
                print("Go tests:", module, flush=True)
                with (directory / f"{module}.jsonl").open("wb") as log:
                    completed = subprocess.run(
                        [str(args.local_go.resolve()), "test", "-json", "-count=1", "-timeout=3m", "-covermode=atomic",
                         "-coverpkg=./...", "-coverprofile", str(directory / f"{module}.cover"), "./..."],
                        cwd=ROOT / module, env=local_env, stdout=log, stderr=subprocess.STDOUT, timeout=600,
                    )
                passed &= completed.returncode == 0
        else:
            try:
                docker("run", "--rm", "-T", "go-tests", capture=False, timeout=1200)
                passed = True
            except subprocess.CalledProcessError:
                passed = False
        observed_db = set()
        for module in MODULES:
            try:
                results = test_results(directory / f"{module}.jsonl")
                counts = profile_counts(directory / f"{module}.cover", module,
                                        [path / "go-app" / f"{module}.cover" for path in args.app_run])
            except (ValueError, AttributeError, OSError) as error:
                report["modules"][module] = {"status": "FAIL", "error": str(error)}
                passed = False
                continue
            observed_db.update(row["test"] for row in results if row["status"] == "PASS")
            test_status = "PASS" if results and all(row["status"] == "PASS" for row in results) else "FAIL"
            report["modules"][module] = {"status": test_status, "coverage": counts, "cases": results}
            passed &= test_status == counts["status"] == "PASS"
        counts = [entry["coverage"] for entry in report["modules"].values() if "coverage" in entry]
        if len(counts) == len(MODULES):
            total = sum(entry["total"] for entry in counts)
            covered = sum(entry["covered"] for entry in counts)
            report["overall"] = {"covered": covered, "total": total, "percent": round(100 * covered / total, 2)}
            passed &= covered * 100 >= total * 85
        report["missing_db_tests"] = sorted(DB_TESTS - observed_db)
        report["status"] = "PASS" if passed and not report["missing_db_tests"] else "FAIL"
        if source_hash() != report["source_sha256"]:
            report["status"] = "FAIL"
            report["error"] = "Workspace changed during the test run"
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        report["error"] = str(error)
    finally:
        if owned:
            try:
                docker("down", "--volumes", "--remove-orphans", capture=False, timeout=120)
            except (OSError, subprocess.SubprocessError) as error:
                report["cleanup_error"] = str(error)
                report["status"] = "FAIL"
        report["seconds"] = round(time.monotonic() - started, 3)
        path = directory / "go-coverage.json"
        path.write_text(json.dumps(report, indent=2), encoding="utf-8")
        print(path)
    return int(report["status"] != "PASS")


if __name__ == "__main__":
    raise SystemExit(main())
