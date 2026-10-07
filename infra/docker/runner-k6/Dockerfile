# k6 runner: load scripts (BE-4.3).
#
# k6 is a single static binary, so this image is that binary, a shell, and jq. jq is
# installed at build time and nothing is installed at run time, which is the rule
# for every runner image: a run has no network unless its target is allowlisted, so
# a run-time install would fail on a correctly isolated host.
FROM grafana/k6:0.54.0@sha256:32000aaa40b848add83425ed7cc77535c343ca473498b0bd29464d00fdca6c79 AS k6

FROM alpine:3.20@sha256:c64c687cbea9300178b30c95835354e34c4e4febc4badfe27102879de0483b5e

RUN apk add --no-cache jq ca-certificates \
    && addgroup -g 1000 runner \
    && adduser -D -u 1000 -G runner -s /sbin/nologin runner

COPY --from=k6 /usr/bin/k6 /usr/bin/k6
COPY runner-k6/entrypoint.sh /usr/local/bin/qavia-run
COPY runner-k6/normalise.jq /usr/local/share/qavia/normalise.jq
RUN chmod 0755 /usr/local/bin/qavia-run

USER 1000:1000
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/qavia-run"]
CMD ["test"]
