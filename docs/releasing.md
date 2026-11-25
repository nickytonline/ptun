# Releasing

Push a `v*` tag and [`.github/workflows/release.yml`](../.github/workflows/release.yml) publishes everything:

```bash
git tag v0.2.0
git push origin v0.2.0
```

| Channel | What gets published | Config |
|---|---|---|
| GitHub Release | `ptun_<os>_<arch>.tar.gz` (`.zip` on Windows) for darwin, linux and windows on amd64 and arm64, `checksums.txt`, a changelog | `.goreleaser.yaml` `builds`, `archives` |
| Linux packages | `.deb`, `.rpm`, `.apk` attached to the release. They depend on the OpenSSH client. | `nfpms` |
| Homebrew | `Casks/ptun.rb` in [`nickytonline/homebrew-tap`](https://github.com/nickytonline/homebrew-tap) (macOS and Linux) | `homebrew_casks` |
| GHCR | `ghcr.io/nickytonline/tunnel-broker:<version>` and `:latest`, `linux/amd64` + `linux/arm64` | `dockers_v2`, `Dockerfile.goreleaser` |
| npm | `ptun` and `@nickytonline/ptun-<os>-<cpu>` | `scripts/npm-publish.mjs`, `npm/ptun/` |
| install script, mise | Nothing to publish: both download the release archives | `install.sh` |
| `go install` | Nothing to publish: the module proxy picks up the tag | |

Tags with a suffix (`v0.2.0-rc.1`) are prereleases: the GitHub Release is marked as one, the image doesn't move
`latest`, and npm publishes under the `next` dist-tag instead of `latest`.

## One-time setup

1. **Make the repo public.** Homebrew, npm, the install script and mise download the public release archives.
2. **Create the tap repo** `nickytonline/homebrew-tap` (public, can be empty).
3. **`HOMEBREW_TAP_TOKEN` secret:** a fine-grained token with **Contents: read and write** on `homebrew-tap` only.
   The workflow's `GITHUB_TOKEN` can't push to another repo.
4. **npm:** packages publish with [trusted publishing](https://docs.npmjs.com/trusted-publishers): npm trusts the
   `npm` job's GitHub OIDC identity, so there's no long-lived token, and each package gets a provenance attestation.
   A trusted publisher can only be added to a package that already exists, so the first release needs a token once:
   1. Add an `NPM_TOKEN` secret: a granular access token with read and write access to `ptun` and the
      `@nickytonline` scope (your npm username), with a short expiry.
   2. Run the first release. The `npm` job creates the `npm` environment if it doesn't exist; you can add required
      reviewers to it under **Settings → Environments**.
   3. For each of the 7 packages (`ptun` and `@nickytonline/ptun-{darwin,linux,win32}-{x64,arm64}`), open
      **Settings → Trusted publishing** on npmjs.com and add GitHub Actions with repository `nickytonline/ptun`,
      workflow `release.yml` and environment `npm`. Under **Publishing access**, choose
      **Require two-factor authentication and disallow tokens**.
   4. Delete the `NPM_TOKEN` secret and revoke the token.

   If the release workflow file is renamed, update the trusted publisher on every package.
5. **GHCR:** nothing to set up. The workflow pushes with `GITHUB_TOKEN`. After the first release, open the package
   settings for `tunnel-broker`, make it public, and check that it's linked to this repo.

## Try it locally

GoReleaser 2.x and Docker build everything into `dist/` without publishing:

```bash
HOMEBREW_TAP_TOKEN=unused goreleaser release --snapshot --clean
dist/ptun_darwin_arm64_v8.0/ptun --version
node scripts/npm-publish.mjs --dry-run   # builds dist/npm and runs npm publish --dry-run
goreleaser check                          # validate .goreleaser.yaml
```

## If a release fails partway

GoReleaser publishes the GitHub Release first, then the cask and image. npm runs in a separate job after it.

- **npm job failed:** fix the cause (usually a missing or mismatched trusted publisher) and re-run just that job from the Actions page. The script
  skips packages whose version is already on npm, so a re-run finishes a partial publish.
- **GoReleaser failed:** delete the GitHub Release and the tag, fix, and tag again.
