#!/usr/bin/env bash
# Dump the FastAPI schema to api/openapi/ai.json.
#
# The Python service is the source of truth for its own contract, and Go generates
# a typed client from this file (BE-1.3). It is checked in and CI regenerates it,
# so a Python response-model change that breaks the contract fails the Go build
# rather than production.
#
# The app is imported rather than served: starting a server to read a schema adds a
# port, a wait loop, and a way for this to be flaky in CI.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$here/services/ai"

uv run python - <<'PY' > "$here/api/openapi/ai.json"
import json

from src.main import app

# sort_keys so the checked-in file does not churn on dictionary ordering, which
# would make the CI diff check fire on a no-op change.
print(json.dumps(app.openapi(), indent=2, sort_keys=True))
PY

echo "wrote api/openapi/ai.json"
