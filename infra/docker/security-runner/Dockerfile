# Security probe runner (BE-9.4).
#
# It sends a fixed list of probes at the target and judges each response by a rule. Node
# with no dependencies — the standard library's http/https client is enough, and a
# security tool with a dependency tree is a security tool with a supply chain. Pinned by
# digest like every other image, because a probe run has to be traceable to what ran it.
#
# What it will never do: choose a payload. The payloads arrive fully resolved on stdin,
# from a reviewed library in the Go process. This container substitutes and sends; it
# does not decide.
FROM node:22.11.0-bookworm-slim@sha256:1b01570a09b39510be11b6d8ac43258d60c68846f26dba191843da614adb226b

RUN groupadd --gid 1000 runner 2>/dev/null || true \
    && useradd --uid 1000 --gid 1000 --create-home --shell /usr/sbin/nologin runner 2>/dev/null || true

WORKDIR /opt/qavia
COPY security-runner/probe.mjs /opt/qavia/probe.mjs
COPY security-runner/entrypoint.sh /usr/local/bin/qavia-run
RUN chmod 0755 /usr/local/bin/qavia-run

ENV NODE_ENV=production npm_config_update_notifier=false
USER 1000:1000
ENTRYPOINT ["qavia-run"]
