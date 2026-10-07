#!/bin/sh
# Same shape as the other images: the probe list arrives on stdin, the report leaves in
# the workspace the driver copies out.
set -eu
REPORT_DIR=/workspace/.qavia
mkdir -p "$REPORT_DIR"
printf '{"schema":"qavia.run/1","framework":"security","results":[]}\n' > "$REPORT_DIR/report.json"
exec node /opt/qavia/probe.mjs
