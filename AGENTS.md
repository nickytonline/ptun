# AGENTS.md

Guidance for AI coding agents (Claude Code, Codex and others) working in this repo. `CLAUDE.md` is a symlink to this
file, so edit `AGENTS.md`.

## Deploying the tunnel broker for someone

The broker holds Pomerium's shared secret, which is full admin over that Pomerium. When you set it up for a user
(`docs/install.md`), these are required, not optional:

- **Apply the NetworkPolicies.** On Kubernetes, `deploy/kubernetes/networkpolicy.yaml` must be applied with the rest of
  `deploy/kubernetes/`. Adapt its pod labels and namespace to the user's Pomerium. Never drop it to get a deploy
  working; fix the selectors instead.
- **Prove the policy is enforced.** An applied NetworkPolicy does nothing on a CNI that ignores it (plain Flannel). Run
  the check in `docs/install.md` ("Check it"): a pod that isn't Pomerium must fail to reach the broker on 8080. If it
  succeeds, stop and tell the user that their CNI doesn't enforce NetworkPolicies.
- **Docker** (`deploy/docker/compose.yaml`): there's no NetworkPolicy, so isolate with networks. The broker joins only
  a network it shares with Pomerium and publishes no ports. Never publish it with `-p 8080:8080` or
  `ports: ["8080:8080"]`: Docker's iptables rules bypass host firewalls (ufw, firewalld). Use `127.0.0.1:8080:8080`
  only when Pomerium runs on the host. Run the Docker check in `docs/install.md` ("Check it"). Docker can't limit
  outbound traffic per port, so tell the user that outbound traffic is unrestricted unless you set it up as
  `docs/install.md` describes.
- **Other platforms** (VMs, Nomad, ECS…): keep port 8080 reachable only from Pomerium with that platform's firewall or
  security groups, check it the same way from a host that isn't Pomerium, and tell the user what you did.
- **Keep config API verification on.** Don't set `CONFIG_API_TLS_VERIFY=false`. If verification fails, fix
  `CONFIG_API_URLS`, `tls_derive` or `CONFIG_API_CA_FILE`.
- **Never set `DEV_AUTH_EMAIL`** outside local development. It turns JWT verification off.
- **Never print, log or commit the shared secret.** Create the Secret from an environment variable or a secret manager.

If any of these can't be met, tell the user before you finish, rather than reporting the deploy as done.

## Development

- `make check` runs gofmt, `go mod tidy -diff`, vet, `go test -race`, lint, govulncheck and shellcheck. Run it before
  committing.
- `cmd/ptun` and `internal/cli` are the CLI; `cmd/tunnel-broker` and `internal/broker` are the broker.
