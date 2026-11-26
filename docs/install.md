# Self-hosting the tunnel broker

`ptun` is only the client. Each `ptun 3000` asks a **tunnel broker** for a link, and the broker creates a short-lived
route in your Pomerium Core through its config API. This page covers running that broker next to your own Pomerium.

## 1. Pomerium prerequisites

Set these up first. The broker can't work around any of them.

1. **Pomerium Core with SSH upstream tunnels.** `ptun` runs `ssh -R <hostname>:443:...` against Pomerium's SSH
   listener, so Core needs [Native SSH Access](https://www.pomerium.com/docs/capabilities/native-ssh-access) and
   [reverse tunneling](https://www.pomerium.com/docs/capabilities/reverse-tunneling):
   - `ssh_address` (for example `0.0.0.0:2200`), reachable from your users' laptops. Its host and port are what you set
     in `SSH_HOST` and `SSH_PORT`.
   - SSH host keys and a user CA key (`ssh_host_keys`, `ssh_user_ca_key`).
   - The `ssh_upstream_tunnel` runtime flag (off by default), for example `RUNTIME_FLAGS={"ssh_upstream_tunnel":true}`.
   - For `ptun --mcp`: Pomerium's `mcp` runtime flag. Without it, turn MCP tunnels off with `ALLOW_MCP_TUNNELS=false`.
2. **The config API and the shared secret.** The broker manages routes and policies through Core's config API
   (Connect `ConfigService`), authenticated with Core's `shared_secret` (base64) as the API token, the same way the
   Pomerium Terraform provider does it.
   - The API is served on Core's `grpc_address`. That address must be reachable from the broker. Core defaults to
     loopback, which only a local port-forward can reach.
   - It has only been tested over HTTPS: `https://` URLs with the certificate derived from the shared secret
     (`tls_derive`, `grpc_insecure: false`). The broker verifies that certificate against the CA it derives from the
     same shared secret, so it needs no CA file (see `CONFIG_API_TLS_VERIFY` below).
   - With a clustered (Raft) databroker, only the leader answers. List every member in `CONFIG_API_URLS`, and the
     broker tries each in turn.
   - **The shared secret is full admin over Pomerium.** Anything that can reach the broker without going through
     Pomerium can forge its identity header, so only Pomerium should be able to reach the broker (in Kubernetes, a
     NetworkPolicy that admits only the Pomerium pods).
3. **Wildcard DNS and a wildcard certificate for `BASE_DOMAIN`.** Tunnel links are `t-<id>.<BASE_DOMAIN>`,
   `<id>.<BASE_DOMAIN>` (`--stable`) and `<name>-<id>.<BASE_DOMAIN>` (`--name`), all one label below `BASE_DOMAIN`.
   `*.<BASE_DOMAIN>` must resolve to Pomerium, and Pomerium needs a certificate that covers `*.<BASE_DOMAIN>`.
4. **A Pomerium route in front of the broker** (its host is `BROKER_HOST`, for example `tunnels.example.com`):
   - `from: https://<BROKER_HOST>`, `to:` the broker's HTTP address (port `8080` by default).
   - `pass_identity_headers: true`, so Pomerium sends the signed `X-Pomerium-Jwt-Assertion` header. The broker reads
     the caller's email (tunnel owner) and `sub` (stable-link id) from it and rejects requests without it.
   - Its policy decides **who may create tunnels**. Everyone it allows can publish local apps on your domain.
   - `ptun` signs in through Pomerium's programmatic login on this route and then sends `Authorization: Pomerium <jwt>`.
   - The broker fetches Pomerium's signing keys from `https://<BROKER_HOST>/.well-known/pomerium/jwks.json` unless
     `JWKS_URL` says otherwise. That URL must resolve and be reachable from wherever the broker runs.

The broker only ever touches the routes and policies it owns: names that start with `ROUTE_PREFIX` (`ephemeral:`)
**and** whose description has the marker `managed by tunnel-broker`. Routes you manage yourself (Terraform, file
routes) are left alone. Terraform ignores the broker's routes because they aren't in its state.

## 2. Settings

The broker is configured only through environment variables.

| Variable | Default | What it does |
|---|---|---|
| `SHARED_SECRET` | (required) | Core's shared secret, base64, used as the config API token. |
| `CONFIG_API_URLS` | (required) | Config API endpoints, comma-separated. Each call tries them in order, starting with the last one that worked. |
| `CONFIG_API_TLS_VERIFY` | `true` | Verifies the config API certificate. A certificate derived from the shared secret (`tls_derive`) is accepted for any host name in `CONFIG_API_URLS`. Any other certificate must chain to the system roots or `CONFIG_API_CA_FILE` and match the host name. `false` skips the check and logs a warning: the broker would then send the full-admin shared secret to whatever answers. |
| `CONFIG_API_CA_FILE` | (none) | A PEM bundle to trust for the config API, besides the system roots. Only needed when its certificate isn't derived from the shared secret and comes from a private CA. |
| `BROKER_HOST` | (required) | The host of the broker's own Pomerium route. Sets the default `JWKS_URL` and `JWT_AUDIENCE`, so it is optional when both are set, and in dev mode (`DEV_AUTH_EMAIL`). |
| `JWKS_URL` | `https://<BROKER_HOST>/.well-known/pomerium/jwks.json` | Where to fetch Pomerium's JWT signing keys. |
| `JWT_AUDIENCE` | `<BROKER_HOST>` | Expected `aud` claim of `X-Pomerium-Jwt-Assertion`. |
| `BASE_DOMAIN` | (required) | Tunnel hosts are `t-<id>.<BASE_DOMAIN>` (and stable links under it). Also part of the stable-link id. |
| `SSH_HOST` | (required) | Pomerium's SSH endpoint host, sent to `ptun` for `ssh -R`. |
| `SSH_PORT` | `2200` | Pomerium's SSH endpoint port. |
| `ROUTE_PREFIX` | `ephemeral:` | Name prefix of the routes and policies the broker owns. Must end with `:`. |
| `TUNNEL_TTL` | `10m` | A tunnel with no heartbeat for this long is deleted (Go duration). Stable links use a shorter lease. |
| `HEARTBEAT_INTERVAL` | `2m` | How often `ptun` heartbeats, sent to the client (Go duration). |
| `MAX_TUNNELS_PER_USER` | `5` | Live tunnels per email. |
| `ALLOW_PUBLIC_TUNNELS` | `false` | Exactly `true` allows `ptun --public` (anyone with the link, no sign-in). Otherwise the broker answers 403. |
| `ALLOW_MCP_TUNNELS` | `true` | Allows `ptun --mcp`. Any other non-empty value refuses it. |
| `LISTEN_ADDR` | `:8080` | HTTP listen address. |
| `DEV_AUTH_EMAIL` | (unset) | Local testing only: turns JWT verification off and treats every request as this email. Refused unless `LISTEN_ADDR` is a loopback address. Never set it on a deployed broker. |

The broker refuses to start while a required setting is missing and names the missing ones.

The `ptun` CLI reads two variables of its own:

| Variable | What it does |
|---|---|
| `PTUN_NO_BROWSER` | Any value: print the sign-in URL instead of opening a browser. |
| `PTUN_BROKER_URL` | Dev mode only: talk plain HTTP to a local broker (`http://127.0.0.1:<port>` or `http://localhost:<port>`) and skip sign-in. |

## 3. Run it

Run **one** broker per Pomerium. Leases live in memory, and every broker adopts all `ROUTE_PREFIX` routes at startup,
so a second broker would delete the first one's live tunnels after `TUNNEL_TTL`. A restart is fine: the broker adopts
existing routes with a fresh lease and clients keep heartbeating.

Each release publishes a multi-arch (`linux/amd64`, `linux/arm64`) image to GHCR, tagged with the version
(without the `v`). Stable releases also move `latest`; release candidates (`1.0.0-rc.1`) only get their version tag:

```bash
docker pull ghcr.io/nickytonline/tunnel-broker:latest
```

Pin a version (or a digest) in production rather than `latest`. To build the image yourself from the repo root
(multi-arch with `docker buildx build --platform linux/amd64,linux/arm64`):

```bash
docker build -t tunnel-broker:local .
```

### Docker

```bash
# SHARED_SECRET comes from your shell environment, so it isn't written on the command line.
docker run -d --name tunnel-broker --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -e SHARED_SECRET \
  -e CONFIG_API_URLS=https://pomerium.internal.example.com:5443 \
  -e BROKER_HOST=tunnels.example.com \
  -e BASE_DOMAIN=example.com \
  -e SSH_HOST=ssh.example.com \
  -e SSH_PORT=2200 \
  ghcr.io/nickytonline/tunnel-broker:1.0.0  # the version you're running, without the v
```

How you wire the two together depends on where Pomerium runs:

- **Pomerium on the same host.** The example binds port 8080 to loopback, so point the Pomerium route for
  `tunnels.example.com` at `http://127.0.0.1:8080`. The broker still has to reach Pomerium's config API from inside
  its container, where `127.0.0.1` is the container itself. Set `CONFIG_API_URLS` to an address the container can
  reach, and make sure Pomerium's config API listens on that address rather than only on loopback.
- **Pomerium in a container.** Put both containers on a shared Docker network: drop `-p` and add
  `--network <net>` to the command above. Point the route at `http://tunnel-broker:8080` (the container name), and set
  `CONFIG_API_URLS` to the Pomerium container's name, for example `https://pomerium:5443`. With `tls_derive` the name
  doesn't need to match the certificate. With any other certificate, it must cover the host name you put in
  `CONFIG_API_URLS`.

Either way, keep port 8080 closed to everything except Pomerium: the broker holds the full-admin shared secret.
`GET /healthz` is unauthenticated and returns 200.

### Kubernetes

[`deploy/kubernetes/`](../deploy/kubernetes/) has a plain Deployment and Service, plus a Secret
placeholder (`shared-secret.yaml.example`) that `kubectl apply` skips:

1. Set `image:` in `deployment.yaml` to a released version of `ghcr.io/nickytonline/tunnel-broker` (or your own
   build in a registry your cluster can pull from).
2. Fill in the `example.com` values in `deployment.yaml`.
3. Put the shared secret in the Secret without committing it, for example:
   `kubectl -n <ns> create secret generic tunnel-broker --from-literal=SHARED_SECRET="$SHARED_SECRET"`.
   If the broker runs in Pomerium's namespace, it can reference Pomerium's existing Secret instead of a copy.
4. `kubectl -n <ns> apply -f deploy/kubernetes/`
5. Add a NetworkPolicy that admits only the Pomerium pods to port 8080, and let the broker reach the config API port on
   every Pomerium member.
6. Create the Pomerium route to `http://tunnel-broker.<ns>.svc.cluster.local:8080`.

## 4. Check it

```bash
kubectl -n <ns> logs deploy/tunnel-broker   # "tunnel-broker listening"; "adopt failed, retrying" means the config API isn't reachable yet
ptun init ssh.example.com:2200 --broker tunnels.example.com
ptun 3000
```

If something fails, see [Troubleshooting](../README.md#troubleshooting) in the README.
