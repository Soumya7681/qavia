#!/bin/sh
# Two jobs, one image: execute a suite, or statically validate one (BE-3.4, BE-4.3).
#
# It is a shell script and not a Node program on purpose. It runs before the
# workspace is trusted, so it depends on nothing the workspace provides.
#
# Vitest is the default because it is what the platform's own export scaffolding
# declares (internal/testfiles/export.go), it reads TypeScript with no transform to
# configure, and it needs no config file in the workspace. Jest stays available for
# a suite a client already had, selected with QAVIA_TEST_RUNNER=jest.
set -eu

REPORT_DIR=/workspace/.qavia
REPORT=$REPORT_DIR/report.json
mkdir -p "$REPORT_DIR"

# The platform reads this file whatever happened, so it is written before anything
# can fail. An empty report plus a non-zero exit is "the suite did not run", which
# is a different outcome from "the suite ran and failed".
printf '{"schema":"qavia.run/1","framework":"node","results":[]}\n' > "$REPORT"

# Vite, which vitest builds on, resolves imports from the file's own directory
# upwards and ignores NODE_PATH, so a suite in /workspace cannot see the toolchain in
# /opt/qavia without this.
#
# A directory of symlinks rather than one symlink to the directory, because vite writes
# its cache into node_modules/.vite: with a symlinked directory that write lands on the
# read-only /opt/qavia and the run fails after the tests have already passed, which is
# the worst possible failure — a correct result reported as an error.
if [ ! -e /workspace/node_modules ]; then
    mkdir -p /workspace/node_modules 2>/dev/null || true
    for package in /opt/qavia/node_modules/* /opt/qavia/node_modules/.bin; do
        [ -e "$package" ] || continue
        ln -s "$package" "/workspace/node_modules/$(basename "$package")" 2>/dev/null || true
    done
fi

mode=${1:-test}
[ $# -gt 0 ] && shift

runner=${QAVIA_TEST_RUNNER:-vitest}

case "$mode" in
test)
    # Test failures are data, not an error, so a non-zero exit must not skip
    # normalisation.
    set +e
    case "$runner" in
    vitest)
        # Two reporters, deliberately: `default` writes progress to stdout, which is
        # what the platform streams to a watching browser, and `json` writes the
        # machine-readable report to a file. With only the JSON reporter a run is
        # silent until the moment it ends, which reads as a hung suite.
        vitest run \
            --root=/workspace \
            --reporter=default \
            --reporter=json \
            --outputFile.json="$REPORT_DIR/raw.json" \
            --no-color \
            --pool=forks \
            --poolOptions.forks.singleFork=true \
            "$@"
        ;;
    jest)
        # Jest needs a config, and a generated workspace has none, so the image
        # carries a default that the workspace's own config still overrides.
        config_flag=""
        for candidate in jest.config.js jest.config.cjs jest.config.mjs jest.config.json jest.config.ts; do
            [ -f "/workspace/$candidate" ] && config_flag="--config=/workspace/$candidate" && break
        done
        [ -z "$config_flag" ] && config_flag="--config=/opt/qavia/jest.config.json"

        jest \
            --ci \
            --colors=false \
            --runInBand \
            --json \
            --outputFile="$REPORT_DIR/raw.json" \
            --testLocationInResults \
            "$config_flag" \
            "$@"
        ;;
    *)
        echo "qavia-run: unknown QAVIA_TEST_RUNNER '$runner' (expected 'vitest' or 'jest')" >&2
        exit 64
        ;;
    esac
    suite_status=$?
    set -e

    if [ -f "$REPORT_DIR/raw.json" ]; then
        node /opt/qavia/validate.mjs normalise "$REPORT_DIR/raw.json" "$REPORT" || true
    fi
    exit $suite_status
    ;;
validate)
    # No target, no network, no execution: parse and type-check only. This is what
    # BE-3.4 calls to reject a generated file before a human ever sees it.
    exec node /opt/qavia/validate.mjs validate "$REPORT"
    ;;
coverage)
    # The repository's own coverage command, run here rather than assembled by the
    # platform, because the toolchain setup above — the node_modules symlink in
    # particular — is this image's business and not the caller's (BE-6.6).
    #
    # The command arrives in the environment rather than as arguments so a command with
    # quoting survives intact.
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
