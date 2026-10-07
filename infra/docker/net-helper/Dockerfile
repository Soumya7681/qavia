# Firewall helper (BE-4.4).
#
# The only container this platform gives a capability to, and it gets two:
# NET_ADMIN to write rules and NET_RAW because iptables opens a raw socket. It runs
# one generated script in the host network namespace and is removed immediately.
#
# It is deliberately not the runner image: the runner executes untrusted code and
# must never carry the capability that programs the firewall confining it.
FROM alpine:3.20@sha256:c64c687cbea9300178b30c95835354e34c4e4febc4badfe27102879de0483b5e

RUN apk add --no-cache iptables ip6tables

# Runs as root because writing iptables rules requires it. Nothing else about it is
# privileged: no volumes, no ports, one command, two seconds.
ENTRYPOINT []
CMD ["sh", "-c", "iptables --version"]
