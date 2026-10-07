#!/bin/sh
# Playwright runner entry point. Same two modes as every other image, so the Go
# side never branches on framework (BE-4.3).
set -eu

REPORT_DIR=/workspace/.qavia
REPORT=$REPORT_DIR/report.json
mkdir -p "$REPORT_DIR"
printf '{"schema":"qavia.run/1","framework":"playwright","results":[]}\n' > "$REPORT"

# Vite, which vitest builds on, resolves imports from the file's own directory
# upwards and ignores NODE_PATH, so a suite in /workspace cannot see the toolchain in
# /opt/qavia without this. A symlink into the writable tmpfs is enough, costs nothing,
# and keeps the dependencies out of the workspace archive: a 400-file suite still
# arrives as 400 files rather than as node_modules.
if [ ! -e /workspace/node_modules ]; then
    ln -s /opt/qavia/node_modules /workspace/node_modules 2>/dev/null || true
fi

mode=${1:-test}
[ $# -gt 0 ] && shift

case "$mode" in
test)
    set +e
    # `list` streams progress to stdout for the live view; `json` goes to a file,
    # named by the environment variable Playwright reads for exactly that.
    PLAYWRIGHT_JSON_OUTPUT_NAME="$REPORT_DIR/playwright.json" \
        playwright test \
        --config /opt/qavia/playwright.config.ts \
        --reporter=list,json \
        --workers=1 \
        "$@"
    suite_status=$?
    set -e

    node /opt/qavia/normalise.mjs "$REPORT_DIR/playwright.json" "$REPORT" || true

    # The video, trace, and screenshot of every failed attempt, base64 in a manifest
    # the driver reads through the same channel as the report: a tmpfs cannot be copied
    # out of, so evidence leaves as text or not at all (BE-7.5).
    node /opt/qavia/artifacts.mjs "$REPORT_DIR/playwright.json" "$REPORT_DIR/artifacts.json" || true

    exit $suite_status
    ;;
validate)
    exec node /opt/qavia/validate.mjs validate "$REPORT"
    ;;
browse)
    # A browser session (BE-7.1). Commands arrive as one JSON object per line on stdin
    # and replies leave the same way, so the container needs no port and keeps the
    # network it was given: only the target its allowlist opened.
    exec node /opt/qavia/browse.mjs
    ;;
pdf)
    # Printing a report (BE-5.9.3). The browser is already in this image, which is the
    # whole reason the platform prints here instead of putting a browser in the process
    # that serves requests.
    #
    # The PDF leaves as base64 through the same report channel every image uses,
    # because that channel is a text stream: the workspace is a tmpfs that stops
    # existing when the container does, so a binary file has to leave as text.
    node /opt/qavia/print.mjs "/workspace/${QAVIA_REPORT_SOURCE:-report.html}" \
        "$REPORT_DIR/report.pdf" || exit $?
    base64 -w0 "$REPORT_DIR/report.pdf" > "$REPORT_DIR/report.pdf.b64"
    exit 0
    ;;
*)
    echo "qavia-run: unknown mode '$mode' (expected 'test' or 'validate')" >&2
    exit 64
    ;;
esac
