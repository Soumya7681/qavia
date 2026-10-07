#!/bin/sh
# One mode, unlike the runners: a mock server serves until it is stopped.
set -eu
exec node /opt/qavia/server.mjs
