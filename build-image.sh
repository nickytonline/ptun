#!/usr/bin/env bash
# Build the tunnel-broker image (amd64 + arm64, the Pi is arm64) and push it to the in-cluster registry.
#
#   tools/ptun/build-image.sh            # tag: git-<short sha> of HEAD
#   tools/ptun/build-image.sh pr-275     # any tag
#
# Talos mirrors docker.io through the registry (terraform/talos/patches/common.yaml, NodePort 30500, plain HTTP), so the
# Deployment references docker.io/library/tunnel-broker:<tag>@sha256:<digest>, the same pattern as pomerium-homelab.
# The image only pulls while the registry pod is up (TODO.md: move to GHCR / retire the registry).
set -euo pipefail

REGISTRY=${REGISTRY:-192.168.86.90:30500}
BUILDER=${BUILDER:-homelab-registry}
HERE=$(cd "$(dirname "$0")" && pwd)
TAG=${1:-git-$(git -C "$HERE" rev-parse --short HEAD)}
IMAGE="$REGISTRY/library/tunnel-broker:$TAG"

# A docker-container builder that knows the registry is plain HTTP (the default builders can't push to it).
if ! docker buildx inspect "$BUILDER" >/dev/null 2>&1; then
  cfg=$(mktemp)
  printf '[registry."%s"]\n  http = true\n  insecure = true\n' "$REGISTRY" > "$cfg"
  docker buildx create --name "$BUILDER" --driver docker-container --buildkitd-config "$cfg" >/dev/null
  rm -f "$cfg"
fi

meta=$(mktemp)
trap 'rm -f "$meta"' EXIT
# registry.insecure on the exporter as well: the builder's registry config alone still made buildkit push over HTTPS.
docker buildx build --builder "$BUILDER" --platform linux/amd64,linux/arm64 \
  --output "type=image,name=$IMAGE,push=true,registry.insecure=true" --metadata-file "$meta" "$HERE"
digest=$(python3 -c "import json,sys; print(json.load(open(sys.argv[1]))['containerimage.digest'])" "$meta")

echo
echo "Pushed $IMAGE"
echo "Set the image in clusters/talos/tunnel-broker/deployment.yaml to:"
echo "  docker.io/library/tunnel-broker:$TAG@$digest"
