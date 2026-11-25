#!/usr/bin/env bash
# Build the tunnel-broker image (amd64 + arm64, the Pi is arm64) and push it to the in-cluster registry.
#
#   ./build-image.sh            # tag: git-<short sha> of HEAD
#   ./build-image.sh pr-275     # any tag
#
# Homelab defaults: Talos mirrors docker.io through the registry (homelab-k8s terraform/talos/patches/common.yaml, NodePort 30500, plain HTTP), so the
# Deployment references docker.io/library/tunnel-broker:<tag>@sha256:<digest>, the same pattern as pomerium-homelab.
# The image only pulls while the registry pod is up (homelab-k8s TODO.md: move to GHCR / retire the registry).
# Elsewhere, set REGISTRY to your own registry (HTTPS, logged in with `docker login`), for example:
#   REGISTRY=ghcr.io/you ./build-image.sh   # pushes ghcr.io/you/tunnel-broker:<tag>
set -euo pipefail

HOMELAB_REGISTRY=192.168.86.90:30500
REGISTRY=${REGISTRY:-$HOMELAB_REGISTRY}
HERE=$(cd "$(dirname "$0")" && pwd)
TAG=${1:-git-$(git -C "$HERE" rev-parse --short HEAD)}

if [ "$REGISTRY" = "$HOMELAB_REGISTRY" ]; then
  # Plain HTTP, and nodes pull it through the docker.io mirror.
  BUILDER=${BUILDER:-homelab-registry}
  IMAGE="$REGISTRY/library/tunnel-broker:$TAG"
  DEPLOY_REF="docker.io/library/tunnel-broker:$TAG"
  EXTRA_OUTPUT=",registry.insecure=true"
else
  BUILDER=${BUILDER:-tunnel-broker}
  IMAGE="$REGISTRY/tunnel-broker:$TAG"
  DEPLOY_REF="$IMAGE"
  EXTRA_OUTPUT=""
fi

# A docker-container builder (the default docker driver can't push a multi-arch image). For the homelab registry it
# also needs to know the registry is plain HTTP.
if ! docker buildx inspect "$BUILDER" >/dev/null 2>&1; then
  if [ "$REGISTRY" = "$HOMELAB_REGISTRY" ]; then
    cfg=$(mktemp)
    printf '[registry."%s"]\n  http = true\n  insecure = true\n' "$REGISTRY" > "$cfg"
    docker buildx create --name "$BUILDER" --driver docker-container --buildkitd-config "$cfg" >/dev/null
    rm -f "$cfg"
  else
    docker buildx create --name "$BUILDER" --driver docker-container >/dev/null
  fi
fi

meta=$(mktemp)
trap 'rm -f "$meta"' EXIT
# Homelab: registry.insecure on the exporter as well, since the builder's registry config alone still made buildkit
# push over HTTPS.
docker buildx build --builder "$BUILDER" --platform linux/amd64,linux/arm64 \
  --output "type=image,name=$IMAGE,push=true$EXTRA_OUTPUT" --metadata-file "$meta" "$HERE"
digest=$(python3 -c "import json,sys; print(json.load(open(sys.argv[1]))['containerimage.digest'])" "$meta")

echo
echo "Pushed $IMAGE"
echo "Set the image in your Deployment (homelab-k8s: clusters/talos/tunnel-broker/deployment.yaml) to:"
echo "  $DEPLOY_REF@$digest"
