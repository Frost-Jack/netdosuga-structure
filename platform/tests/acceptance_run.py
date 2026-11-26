"""Run acceptance checks using a fresh Docker Compose project per invocation."""

import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import time
import traceback
import unittest
from pathlib import Path
from uuid import uuid4

PLATFORM = Path(__file__).resolve().parents[1]
ROOT = PLATFORM.parent
GO_MODULES = ("gateway", "identity-service", "order-service", "payment-mock", "notification-service", "integration-mock")


def command(args, *, env=None, capture=True, stdin=None, timeout=60):
    return subprocess.run(
        [str(arg) for arg in args], cwd=ROOT, env=env, input=stdin,
        encoding="utf-8", capture_output=capture, check=True, timeout=timeout,
    ).stdout


def source_hash():
    files = command(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"]).split("\0")
    digest = hashlib.sha256()
    for name in sorted(set(files)):
        path = ROOT / name
        if name and not name.startswith(".idea/") and path.is_file():
            digest.update(name.encode() + b"\0")
            digest.update(hashlib.sha256(path.read_bytes()).digest())
    return digest.hexdigest()


class Stack:
    ports = {"http-router": 8080, "jaeger": 16686, "prometheus": 9090,
             "grafana": 3000, "integration-mock": 8083, "payment-mock": 8085}

    def __init__(self, run_id, directory):
        self.project = f"netdosuga-at-{run_id}"
        self.directory = directory
        self.ha = directory / "ha"
        self.env = dict(os.environ, ACCEPTANCE_HA_DIR=self.ha.as_posix(),
                        POSTGRES_PASSWORD="demo12345", GRAFANA_ADMIN_PASSWORD="demo12345",
                        MVP_SEED_PASSWORD="demo12345")
        self.env.setdefault("COMPOSE_PROGRESS", "plain")
        self.args = ["docker", "compose", "-p", self.project,
                     "--env-file", PLATFORM / "tests/bootstrap.env"]
        for config in ("ha", "bootstrap", "acceptance"):
            self.args.extend(["-f", PLATFORM / f"compose.{config}.yaml"])
        self.urls = {}
        self.primary = "pg-a"
        self.owned = False
        self.observations = {}

    def dc(self, *args, **kwargs):
        return command([*self.args, *args], env=self.env, **kwargs)

    def start(self):
        shutil.copytree(PLATFORM / "ha", self.ha)
        # The developer stack may have been promoted. A fresh stack always starts on pg-a.
        route = self.ha / "postgres.cfg"
        route.write_text(route.read_text(encoding="utf-8").replace("pg-b:5432", "pg-a:5432"), encoding="utf-8")
        for kind in ("container", "network", "volume"):
            if command(["docker", kind, "ls", "-q", "--filter", f"label=com.docker.compose.project={self.project}"]).strip():
                raise RuntimeError(f"Found existing resources for {self.project}")
        self.owned = True
        preparation = time.monotonic()
        self.dc("build", capture=False, timeout=1200)
        built = time.monotonic()
        self.observations["startup"] = {"image_build_seconds": round(built - preparation, 3)}
        self.dc("up", "-d", "--build", "--wait", "--wait-timeout", "300", capture=False, timeout=1200)
        self.observations["startup"]["stack_ready_seconds"] = round(time.monotonic() - built, 3)
        self.refresh_urls()

    def refresh_urls(self):
        for service, port in self.ports.items():
            address = self.dc("port", service, port).strip()
            if not re.fullmatch(r"127\.0\.0\.1:[1-9][0-9]*", address):
                raise ValueError(f"Unexpected Compose endpoint for {service}")
            self.urls[service] = "http://" + address

    def sql(self, sql, target=None, timeout=10):
        return self.dc("exec", "-T", target or self.primary, "psql", "-XqAt", "-v", "ON_ERROR_STOP=1",
                       "-U", "demo", "-d", "demo", stdin=sql, timeout=timeout).strip()

    def rows(self, query, target=None):
        return json.loads(self.sql(f"SELECT coalesce(json_agg(r), '[]') FROM ({query}) AS r;", target))

    def instrument_go(self):
        self.go_profiles = self.directory / "go-app"
        for module in GO_MODULES:
            (self.go_profiles / module).mkdir(parents=True)
        self.env["ACCEPTANCE_GO_DIR"] = self.go_profiles.as_posix()
        self.args.extend(["-f", PLATFORM / "compose.instrumented.yaml"])

    def collect_go_profiles(self):
        self.dc("stop", "-t", "10", "gateway-a", "gateway-b", "order-a", "order-b", "identity-service",
                "payment-mock", "notification-service", "integration-mock", timeout=60)
        for module in GO_MODULES:
            if not list((self.go_profiles / module).glob("covcounters.*")):
                raise RuntimeError(f"{module} did not flush coverage counters")
        command(["docker", "run", "--rm", "--network", "none", "--mount",
                 f"type=bind,source={self.go_profiles.as_posix()},target=/coverage",
                 "golang:1.25.3-alpine3.22", "/bin/sh", "-ec",
                 'for module in "$@"; do go tool covdata textfmt -i="/coverage/$module" -o="/coverage/$module.cover"; done',
                 "covdata", *GO_MODULES], timeout=120)

    def close(self):
        if self.owned:
            self.dc("down", "--volumes", "--remove-orphans", capture=False, timeout=120)


class Results(unittest.TestResult):
    def __init__(self):
        super().__init__()
        self.cases = []

    def startTest(self, test):
        super().startTest(test)
        self.started = time.monotonic()

    def record(self, test, status, detail=None):
        self.cases.append({"id": test.id(), "fixture_id": test.prefix, "status": status,
                           "seconds": round(time.monotonic() - self.started, 3), "detail": detail})
        print(status, test.id(), flush=True)

    def addSuccess(self, test):
        self.record(test, "PASS")

    def addError(self, test, err):
        from acceptance_cases import CheckFailure

        super().addError(test, err)
        # Inputs, session cookies and credentials must stay out of reports.
        error = err[1]
        detail = err[0].__name__
        if isinstance(error, CheckFailure):
            detail += ": " + str(error)
        frames = [frame for frame in traceback.extract_tb(err[2]) if "acceptance_" in frame.filename]
        if frames:
            detail += f" at {Path(frames[-1].filename).name}:{frames[-1].lineno}"
        self.record(test, "FAIL", detail)

    addFailure = addError

    def addSkip(self, test, reason):
        super().addSkip(test, reason)
        self.record(test, "SKIP", reason)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("suite", choices=("sanity", "functional", "browser", "faults", "load-catalog", "load-orders", "all"))
    parser.add_argument("--case", action="append", help="Run matching case IDs, e.g. F-09 or N-06a.")
    parser.add_argument("--browser", choices=("chromium", "chrome", "edge", "yandex"), default="chromium")
    parser.add_argument("--js-coverage", action="store_true",
                        help="Instrument first-party app.js and gate browser statement coverage at 85%%.")
    parser.add_argument("--go-coverage", action="store_true",
                        help="Export application counters from an instrumented sanity, functional or browser run.")
    args = parser.parse_args()
    run_id = uuid4().hex[:12]
    directory = PLATFORM / "test-results" / run_id
    directory.mkdir(parents=True, exist_ok=False)
    stack = Stack(run_id, directory)
    report = {"run_id": run_id, "source_sha256": source_hash(), "suite": args.suite, "status": "BLOCKED"}
    result = Results()
    started = time.monotonic()
    try:
        # Store the suite definition in the report even when setup fails.
        from acceptance_cases import cases
        import acceptance_negative
        import acceptance_observe

        if args.suite in ("browser", "sanity", "all"):
            import acceptance_browser
        if args.suite in ("faults", "all"):
            import acceptance_faults
        if args.suite in ("load-catalog", "load-orders"):
            import acceptance_load
        selected = [(name, function) for group, name, function in cases
                    if (group == args.suite or args.suite == "all" and not group.startswith("load-"))
                    and (not args.case or any(name == key or name.startswith(key + "-") for key in args.case))]
        if not selected:
            raise ValueError("No matching tests")
        report["selected"] = [name for name, _ in selected]
        if args.js_coverage:
            if args.suite not in ("browser", "all") or args.case:
                raise ValueError("--js-coverage requires a full browser suite")
            instrumenter = PLATFORM / "tests/js_coverage.cjs"
            command(["node", instrumenter, "instrument", directory])
            stack.original_js = (ROOT / "gateway/web/app.js").read_bytes()
            stack.instrumented_js = (directory / "app.instrumented.js").read_bytes()
        if args.go_coverage:
            if args.suite not in ("sanity", "functional", "browser"):
                raise ValueError("--go-coverage supports sanity, functional and browser suites")
            stack.instrument_go()
        stack.start()
        report["setup_seconds"] = round(time.monotonic() - started, 3)
        from acceptance_cases import Scenario

        for name, function in selected:
            Scenario(name, function, stack, args.browser).run(result)
        report["status"] = "PASS" if result.wasSuccessful() and not result.skipped else "FAIL"
        if args.go_coverage:
            stack.collect_go_profiles()
        if args.js_coverage:
            coverage = json.loads(command(["node", instrumenter, "report", directory]))
            report["javascript"] = coverage
            if coverage["covered"] * 100 < coverage["total"] * 85:
                report["status"] = "FAIL"
        if source_hash() != report["source_sha256"]:
            report["status"] = "FAIL"
            report["error"] = "Workspace changed during the test run"
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError, ImportError) as error:
        report["error"] = f"{type(error).__name__}: {error}"
        if result.cases:
            report["status"] = "FAIL"
        print(report["error"], flush=True)
    finally:
        try:
            stack.close()
        except (OSError, subprocess.SubprocessError) as error:
            report["cleanup_error"] = str(error)
            report["status"] = "FAIL"
        finished = {case["id"] for case in result.cases}
        report["cases"] = result.cases + [{"id": name, "status": "BLOCKED"}
                                          for name in report.get("selected", []) if name not in finished]
        report["observations"] = stack.observations
        report["seconds"] = round(time.monotonic() - started, 3)
        path = directory / "acceptance.json"
        path.write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
        print(path)
    return int(report["status"] != "PASS")


if __name__ == "__main__":
    raise SystemExit(main())
