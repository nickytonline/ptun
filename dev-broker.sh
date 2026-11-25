#!/usr/bin/env bash
# Run tunnel-broker on this laptop against the real Pomerium Core, for testing ptun before the broker is deployed.
#
#   BASE_DOMAIN=example.com SSH_HOST=ssh.example.com ./dev-broker.sh you@example.com   # your sign-in email
#   PTUN_BROKER_URL=http://127.0.0.1:8080 ptun 3000   # in another terminal
#
# - Port-forwards to the Raft leader's config API (only the leader serves it), like terraform/pomerium-core/tf.sh.
# - Needs a homelab-k8s checkout: HOMELAB_K8S (default ../homelab-k8s, next to this repo).
# - The shared secret comes from homelab-k8s terraform/pomerium-core/secrets.enc.yaml through `sops exec-env` (1Password prompt):
#   it only exists in the broker's environment and is never printed.
# - DEV_AUTH_EMAIL turns JWT verification off; the broker refuses it unless it listens on loopback.
# - Creates real ephemeral:* routes in Pomerium. Ctrl-C on ptun deletes its route; stopping this script leaves any live
#   tunnel's route in place until the deployed broker adopts and sweeps it (or delete it by hand).
set -euo pipefail

EMAIL=${1:?usage: dev-broker.sh <your-pomerium-email>}
NAMESPACE=${NAMESPACE:-pomerium}
LOCAL_PORT=${LOCAL_PORT:-15444} # not 15443, so it can run next to tf.sh
HERE=$(cd "$(dirname "$0")" && pwd)
HOMELAB_K8S=${HOMELAB_K8S:-$HERE/../homelab-k8s}
[ -d "$HOMELAB_K8S/terraform/pomerium-core" ] || { echo "no homelab-k8s checkout at $HOMELAB_K8S; set HOMELAB_K8S" >&2; exit 1; }
export KUBECONFIG=${KUBECONFIG:-$HOME/.kube/talos-direct.yaml}
[ -f "$KUBECONFIG" ] || KUBECONFIG=$HOME/.kube/talos-maison.yaml

if [ -z "${PF_POD:-}" ]; then
  leaders=()
  for pod in $(kubectl -n "$NAMESPACE" get pods -l statefulset.kubernetes.io/pod-name -o name); do
    state=$(kubectl -n "$NAMESPACE" logs "$pod" -c pomerium 2>/dev/null \
      | grep -o -E 'entering (leader|follower|candidate) state' | tail -1 || true)
    [ "$state" = "entering leader state" ] && leaders+=("${pod#pod/}")
  done
  [ "${#leaders[@]}" -eq 1 ] || { echo "expected one Raft leader, found ${#leaders[@]}; set PF_POD=pomerium-N" >&2; exit 1; }
  PF_POD=${leaders[0]}
fi
echo "config API: port-forward to Raft leader $PF_POD on 127.0.0.1:$LOCAL_PORT" >&2

kubectl -n "$NAMESPACE" port-forward "pod/$PF_POD" "$LOCAL_PORT:5443" >/dev/null 2>&1 &
PF=$!
trap 'kill $PF 2>/dev/null || true' EXIT
for _ in $(seq 1 20); do nc -z 127.0.0.1 "$LOCAL_PORT" 2>/dev/null && break; sleep 0.5; done
nc -z 127.0.0.1 "$LOCAL_PORT" 2>/dev/null || { echo "port-forward did not come up" >&2; exit 1; }

(cd "$HERE" && go build -o bin/tunnel-broker ./cmd/tunnel-broker)

export ALLOW_PUBLIC_TUNNELS=true
export CONFIG_API_URLS="https://127.0.0.1:$LOCAL_PORT" DEV_AUTH_EMAIL="$EMAIL" LISTEN_ADDR=127.0.0.1:8080
echo "broker on http://127.0.0.1:8080 as $EMAIL. In another terminal: PTUN_BROKER_URL=http://127.0.0.1:8080 ptun 3000" >&2
# Single quotes: $TF_VAR_shared_secret_b64 expands only inside sops' child, never in this shell or its output.
cd "$HOMELAB_K8S/terraform/pomerium-core"
sops exec-env secrets.enc.yaml 'SHARED_SECRET=$TF_VAR_shared_secret_b64 exec '"$HERE"'/bin/tunnel-broker'
