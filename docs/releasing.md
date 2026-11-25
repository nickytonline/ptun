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
4. **`NPM_TOKEN` secret:** an npm granular access token that can publish `ptun` and the `@nickytonline` scope.
   The scope is your npm username, so it already exists. Packages are published with provenance.
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

- **npm job failed:** fix the cause (usually the token) and re-run just that job from the Actions page. The script
  skips packages whose version is already on npm, so a re-run finishes a partial publish.
- **GoReleaser failed:** delete the GitHub Release and the tag, fix, and tag again.
