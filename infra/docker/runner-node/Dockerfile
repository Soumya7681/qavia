# Node runner: Supertest, Jest, Vitest (BE-4.3).
#
# The base is pinned by digest, because a runner image referenced by a moving tag
# means the platform cannot say what produced a result. Every dependency this image
# will ever need is installed at build time: a run has no network unless its target
# is allowlisted, so an `npm install` at run time would fail on a locked-down host
# and succeed on a leaky one, which is the worst of both.
FROM node:22.11.0-bookworm-slim@sha256:1b01570a09b39510be11b6d8ac43258d60c68846f26dba191843da614adb226b

# Nothing here is installed at run time, so the package manager metadata goes.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Non-root, baked in, matching the uid the driver asks for. The driver sets
# `User: 1000:1000` as well, so an image rebuilt without this line still runs
# unprivileged.
RUN groupadd --gid 1000 runner 2>/dev/null || true \
    && useradd --uid 1000 --gid 1000 --create-home --shell /usr/sbin/nologin runner 2>/dev/null || true

# The toolchain lives outside /workspace, because /workspace is a tmpfs the driver
# mounts and overwrites per run.
WORKDIR /opt/qavia
COPY runner-node/package.json runner-node/package-lock.json ./
RUN npm ci --omit=dev && npm cache clean --force

# NODE_PATH lets a generated suite `require('supertest')` without a per-run install.
ENV NODE_PATH=/opt/qavia/node_modules \
    PATH=/opt/qavia/node_modules/.bin:$PATH \
    CI=true \
    npm_config_update_notifier=false

COPY runner-node/entrypoint.sh /usr/local/bin/qavia-run
COPY runner-node/validate.mjs /opt/qavia/validate.mjs
COPY runner-node/jest.config.json /opt/qavia/jest.config.json
RUN chmod 0755 /usr/local/bin/qavia-run

USER 1000:1000
WORKDIR /workspace

# Two entry points, one image. `qavia-run test` executes a suite; `qavia-run
# validate` is the static check BE-3.4 needs, and it runs with no target at all.
ENTRYPOINT ["/usr/local/bin/qavia-run"]
CMD ["test"]
