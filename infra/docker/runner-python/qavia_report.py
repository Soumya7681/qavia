"""pytest reporter and static validator for the Python runner (BE-3.4, BE-4.3).

One file, two entry points, for the same reason as the Node image: the validator
must work with no network and no dependency the workspace provides.
"""

from __future__ import annotations

import json
import pathlib
import re
import sys

WORKSPACE = pathlib.Path("/workspace")
_STATUS = {"passed": "passed", "failed": "failed", "skipped": "skipped", "error": "failed"}


def pytest_addoption(parser) -> None:
    parser.addoption("--qavia-report", default="/workspace/.qavia/report.json")


class _Reporter:
    """Collects one result per test, keyed so a retry is a separate attempt."""

    def __init__(self, path: str) -> None:
        self.path = path
        self.attempts: dict[str, int] = {}
        self.results: list[dict[str, object]] = []

    def pytest_runtest_logreport(self, report) -> None:
        # A skip is decided during setup and never reaches the call phase, so it has
        # to be taken from there or it disappears from the report. Setup and
        # teardown are otherwise interesting only when they fail: a failing fixture
        # is a failing test, and a passing one would be reported twice.
        if report.when == "setup":
            if not (report.failed or report.skipped):
                return
        elif report.when == "teardown":
            if not report.failed:
                return
        elif report.when != "call":
            return

        attempt = self.attempts.get(report.nodeid, 0) + 1
        self.attempts[report.nodeid] = attempt

        status = "failed"
        if report.passed:
            status = "passed"
        elif report.skipped:
            status = "skipped"

        message = None
        if report.failed:
            message = str(report.longrepr)[:8000]

        self.results.append({
            "name": report.nodeid.split("::", 1)[-1].replace("::", " > "),
            "file": report.nodeid.split("::", 1)[0],
            "status": _STATUS.get(status, "failed"),
            "durationMs": round((report.duration or 0) * 1000),
            "attempt": attempt,
            "failureMessage": message,
        })

    def pytest_sessionfinish(self) -> None:
        pathlib.Path(self.path).write_text(
            json.dumps({
                "schema": "qavia.run/1",
                "framework": "pytest",
                "results": self.results,
            }) + "\n",
            encoding="utf8",
        )


def pytest_configure(config) -> None:
    config.pluginmanager.register(_Reporter(config.getoption("--qavia-report")), "qavia-reporter")


# Patterns that mean the model produced the shape of a test rather than a test. A
# file that imports cleanly and asserts nothing is worse than one that does not
# compile: it passes forever and reports coverage it does not have (BE-3.4).
_PLACEHOLDERS = [
    (re.compile(r"#\s*TODO\b", re.I), "contains a TODO instead of an assertion"),
    (re.compile(r"\bassert\s+True\s*$"), "asserts True, which cannot fail"),
    (re.compile(r"\bassert\s+1\s*==\s*1\b"), "asserts 1 == 1, which cannot fail"),
    (re.compile(r"^\s*pass\s*$"), "has an empty test body"),
    (re.compile(r"pytest\.skip\(|@pytest\.mark\.skip"), "has a test marked skip"),
    (re.compile(r"raise NotImplementedError"), "raises NotImplementedError"),
]


def _placeholders(path: pathlib.Path, text: str) -> list[dict[str, object]]:
    """Reports lines that assert nothing, with the line number."""
    found: list[dict[str, object]] = []
    for number, line in enumerate(text.splitlines(), start=1):
        for pattern, message in _PLACEHOLDERS:
            if pattern.search(line):
                found.append({
                    "file": str(path.relative_to(WORKSPACE)),
                    "line": number,
                    "code": "QAVIA_PLACEHOLDER",
                    "message": message,
                })
    return found


def _validate(report_path: str) -> int:
    """Compiles every generated file and rejects placeholder tests.

    A file that does not parse is caught here rather than by a run that looks like a
    test failure."""
    files = [
        path for path in sorted(WORKSPACE.rglob("*.py"))
        if ".qavia" not in path.parts and "site-packages" not in path.parts
    ]
    problems: list[dict[str, object]] = []

    if not files:
        problems.append({"file": "", "line": 0, "message": "no test files were written"})

    for path in files:
        try:
            text = path.read_text(encoding="utf8")
            compile(text, str(path), "exec")
            problems.extend(_placeholders(path, text))
        except SyntaxError as error:
            problems.append({
                "file": str(path.relative_to(WORKSPACE)),
                "line": error.lineno or 0,
                "message": f"{error.msg}",
            })

    pathlib.Path(report_path).write_text(
        json.dumps({
            "schema": "qavia.validate/1",
            "files": [str(path.relative_to(WORKSPACE)) for path in files],
            "problems": problems,
        }) + "\n",
        encoding="utf8",
    )
    return 0 if not problems else 1


if __name__ == "__main__":
    if len(sys.argv) >= 2 and sys.argv[1] == "validate":
        sys.exit(_validate(sys.argv[2] if len(sys.argv) > 2 else "/workspace/.qavia/report.json"))
    print(f"qavia_report.py: unknown mode {sys.argv[1:]!r}", file=sys.stderr)
    sys.exit(64)
