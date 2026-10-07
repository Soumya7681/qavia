# Mock server: a spec-shaped API a client app can point at (BE-8.6, F-10.6 to F-10.8).
#
# Node with no dependencies at all — the standard library's http server is enough, and
# a mock with a dependency tree is a mock that needs an npm install on a host with no
# network. The base is pinned by digest for the same reason every other image here is:
# a running mock has to be traceable to what produced it.
FROM node:22.11.0-bookworm-slim@sha256:1b01570a09b39510be11b6d8ac43258d60c68846f26dba191843da614adb226b

RUN groupadd --gid 1000 runner 2>/dev/null || true \
    && useradd --uid 1000 --gid 1000 --create-home --shell /usr/sbin/nologin runner 2>/dev/null || true

WORKDIR /opt/qavia
COPY mock-server/server.mjs /opt/qavia/server.mjs
COPY mock-server/entrypoint.sh /usr/local/bin/qavia-mock
RUN chmod 0755 /usr/local/bin/qavia-mock

ENV NODE_ENV=production \
    QAVIA_MOCK_PORT=8080 \
    npm_config_update_notifier=false

# The config arrives on stdin as one JSON document, so the container needs no writable
# path to be configured: the workspace is a tmpfs the driver mounts, and a mock that
# read its configuration from a file would need one written into it first.
USER 1000:1000
EXPOSE 8080

ENTRYPOINT ["qavia-mock"]
