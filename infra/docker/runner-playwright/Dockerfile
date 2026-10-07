# Playwright runner: browser tests (BE-4.3).
#
# The base is Playwright's own image, pinned by digest, because the browser build
# and the library version have to match exactly and Playwright is the only party
# that knows which pair is correct.
FROM mcr.microsoft.com/playwright:v1.48.2-jammy@sha256:ca6450233e72d43475836930f32bad51cff9258794c6f5f506264dda4276b2cf

RUN groupadd --gid 1000 runner 2>/dev/null || true \
    && useradd --uid 1000 --gid 1000 --create-home --shell /usr/sbin/nologin runner 2>/dev/null || true

WORKDIR /opt/qavia
COPY runner-playwright/package.json runner-playwright/package-lock.json ./
RUN npm ci --omit=dev && npm cache clean --force

ENV NODE_PATH=/opt/qavia/node_modules \
    PATH=/opt/qavia/node_modules/.bin:$PATH \
    CI=true \
    PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
    npm_config_update_notifier=false

COPY runner-playwright/entrypoint.sh /usr/local/bin/qavia-run
COPY runner-node/validate.mjs /opt/qavia/validate.mjs
COPY runner-playwright/normalise.mjs /opt/qavia/normalise.mjs
COPY runner-playwright/print.mjs /opt/qavia/print.mjs
COPY runner-playwright/browse.mjs /opt/qavia/browse.mjs
COPY runner-playwright/artifacts.mjs /opt/qavia/artifacts.mjs
COPY runner-playwright/playwright.config.ts /opt/qavia/playwright.config.ts
RUN chmod 0755 /usr/local/bin/qavia-run

# The browser sandbox wants a writable profile directory, and the driver mounts
# /workspace and /tmp as tmpfs. Nothing else is writable.
ENV XDG_CACHE_HOME=/tmp/.cache \
    HOME=/tmp

USER 1000:1000
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/qavia-run"]
CMD ["test"]
