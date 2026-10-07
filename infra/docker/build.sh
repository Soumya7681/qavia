#!/usr/bin/env bash
# Builds the runner images and records the digest of each one (BE-4.3).
#
# The digest is the point. A tag is mutable, so a run recorded against a tag cannot
# say what actually executed; the digest this script prints is what an admin pastes
# into runner.image_* in settings, and it is what the platform stores on the run row.
#
#   ./build.sh                       build every image locally, record local IDs
#   ./build.sh --push                build, push, and record the registry digest
#   ./build.sh --image node --push   one family only
#
# A local build records an image ID, which another host cannot pull. That is why
# --push exists, and why this script says so rather than pretending an ID is a
# digest.
set -euo pipefail

cd "$(dirname "$0")"

REGISTRY=${QAVIA_REGISTRY:-ghcr.io/hyscaler}
PLATFORM=${QAVIA_PLATFORM:-linux/amd64}
DIGESTS=digests.json
push=false
only=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --push) push=true; shift ;;
        --image) only=${2:?--image needs a family}; shift 2 ;;
        --registry) REGISTRY=${2:?--registry needs a host}; shift 2 ;;
        -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
        *) echo "build.sh: unknown argument '$1'" >&2; exit 64 ;;
    esac
done

families=(node playwright k6 python net-helper mock-server security-runner)
[[ -n $only ]] && families=("$only")

rows=()

for family in "${families[@]}"; do
    directory=runner-$family
    [[ $family == net-helper ]] && directory=net-helper
    [[ $family == mock-server ]] && directory=mock-server
    [[ $family == security-runner ]] && directory=security-runner
    [[ -d $directory ]] || { echo "build.sh: no such family '$family'" >&2; exit 64; }

    # The firewall helper is not a runner: it carries iptables and no toolchain,
    # and the naming keeps that difference visible on a host's image list.
    if [[ $family == net-helper ]]; then
        repository=$REGISTRY/qavia-net-helper
    elif [[ $family == mock-server ]]; then
        # Not a runner either: it serves traffic rather than running a suite, and the
        # naming keeps that visible on a host's image list.
        repository=$REGISTRY/qavia-mock-server
    elif [[ $family == security-runner ]]; then
        repository=$REGISTRY/qavia-runner-security
    else
        repository=$REGISTRY/qavia-runner-$family
    fi
    echo "==> building $repository ($PLATFORM)"

    # The build context is this directory, not the family's, because the Node
    # image's validator is shared with Playwright and a context per family would
    # mean a copy per family.
    docker build \
        --platform "$PLATFORM" \
        --file "$directory/Dockerfile" \
        --tag "$repository:build" \
        --label "org.opencontainers.image.source=https://github.com/hyscaler/qavia" \
        --label "qavia.runner.family=$family" \
        .

    if $push; then
        docker push "$repository:build" >/dev/null
        # RepoDigests carries the registry digest, the only reference another host
        # can pull.
        digest=$(docker inspect --format '{{index .RepoDigests 0}}' "$repository:build" | cut -d@ -f2)
        reference="$repository@$digest"
    else
        digest=$(docker inspect --format '{{.Id}}' "$repository:build")
        reference="$repository:build"
        echo "    local build: $digest is an image ID, not a pullable digest." \
             "Re-run with --push before pointing a runner host at it."
    fi

    rows+=("$family	$digest	$reference")
    echo "    setting: runner.image_$family = $reference"
done

# Rewritten whole, so a partial build never leaves a stale digest claiming to
# describe the current image. Only the families built this run are recorded; the
# rest keep whatever the file already said.
printf '%s\n' "${rows[@]}" | python3 -c '
import json, os, sys

path, registry, platform, pushed = sys.argv[1:5]
try:
    with open(path) as handle:
        data = json.load(handle)
except (OSError, ValueError):
    data = {}

images = data.get("images") or {}
for line in sys.stdin.read().splitlines():
    if not line.strip():
        continue
    family, digest, reference = line.split("\t")
    images[family] = {"digest": digest, "reference": reference}

data.update({"registry": registry, "platform": platform,
             "pushed": pushed == "true", "images": images})
with open(path, "w") as handle:
    json.dump(data, handle, indent=2, sort_keys=True)
    handle.write("\n")
' "$DIGESTS" "$REGISTRY" "$PLATFORM" "$push"

echo
echo "recorded in $DIGESTS. Paste each reference into Settings > Runner; no deploy needed."
