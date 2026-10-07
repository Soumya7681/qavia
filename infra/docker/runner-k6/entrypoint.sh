#!/bin/sh
# k6 runner entry point (BE-4.3). Same two modes as every other runner image, so
# the Go side never branches on framework.
set -eu

REPORT_DIR=/workspace/.qavia
REPORT=$REPORT_DIR/report.json
mkdir -p "$REPORT_DIR"
printf '{"schema":"qavia.run/1","framework":"k6","results":[]}\n' > "$REPORT"

mode=${1:-test}
[ $# -gt 0 ] && shift

# The platform names the entry script, because a load run is one script and the
# platform is the party that knows which one it generated.
script=${QAVIA_ENTRY_FILE:-script.js}

case "$mode" in
test)
    set +e
    k6 run --no-usage-report --summary-export="$REPORT_DIR/k6.json" "$script" "$@"
    suite_status=$?
    set -e

    # A load script has thresholds, not test cases, so each threshold becomes one
    # result. A load run then reads the same way a functional run does.
    if [ -f "$REPORT_DIR/k6.json" ]; then
        jq -f /usr/local/share/qavia/normalise.jq \
            --arg file "$script" \
            "$REPORT_DIR/k6.json" > "$REPORT.tmp" && mv "$REPORT.tmp" "$REPORT"
    fi
    exit $suite_status
    ;;
validate)
    # k6 builds the module graph and resolves every import without making a single
    # request, which is exactly the static check BE-3.4 wants.
    if k6 archive --archive-out /tmp/qavia.tar "$script" >/dev/null 2>"$REPORT_DIR/k6.err"; then
        jq -n --arg file "$script" \
            '{schema:"qavia.validate/1", files:[$file], problems:[]}' > "$REPORT"
        exit 0
    fi
    jq -Rn --arg file "$script" --rawfile err "$REPORT_DIR/k6.err" \
        '{schema:"qavia.validate/1", files:[$file],
          problems:[{file:$file, line:0, message:($err|ltrimstr("\n")|.[0:8000])}]}' > "$REPORT"
    cat "$REPORT_DIR/k6.err" >&2
    exit 1
    ;;
*)
    echo "qavia-run: unknown mode '$mode' (expected 'test' or 'validate')" >&2
    exit 64
    ;;
esac
