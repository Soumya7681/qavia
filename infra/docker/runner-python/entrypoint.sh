#!/bin/sh
# Python runner entry point (BE-4.3). Same two modes as every other runner image.
set -eu

REPORT_DIR=/workspace/.qavia
REPORT=$REPORT_DIR/report.json
mkdir -p "$REPORT_DIR"
printf '{"schema":"qavia.run/1","framework":"pytest","results":[]}\n' > "$REPORT"

mode=${1:-test}
[ $# -gt 0 ] && shift

case "$mode" in
test)
    set +e
    # The report plugin writes the platform's shape directly, so there is no second
    # conversion step to keep in sync.
    python -m pytest -p qavia_report --qavia-report="$REPORT" -q "$@"
    suite_status=$?
    set -e
    exit $suite_status
    ;;
validate)
    exec python /opt/qavia/qavia_report.py validate "$REPORT"
    ;;
coverage)
    # The repository's own coverage command (BE-6.6). In the environment rather than as
    # arguments, so quoting survives.
    if [ -z "${QAVIA_COVERAGE_COMMAND:-}" ]; then
        echo "qavia-run: coverage needs QAVIA_COVERAGE_COMMAND" >&2
        exit 64
    fi
    cd /workspace || exit 1
    sh -c "$QAVIA_COVERAGE_COMMAND"
    exit $?
    ;;
*)
    echo "qavia-run: unknown mode '$mode' (expected 'test' or 'validate')" >&2
    exit 64
    ;;
esac
