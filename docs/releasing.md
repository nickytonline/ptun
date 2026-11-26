# Releasing

Releases follow the same flow as [pomerium/pomerium](https://github.com/pomerium/pomerium/blob/main/.github/workflows/release.yaml):
you publish a GitHub Release, and [`.github/workflows/release.yml`](../.github/workflows/release.yml) builds and
publishes everything into it.

1. **Dry run.** Actions → **release** → **Run workflow** on `main`, with **Dry run** checked (the default). It builds
   every archive, package, the cask and the image as a snapshot, and runs `npm publish --dry-run`. Nothing is
   published. A manual run with **Dry run** unchecked fails on purpose.
2. **Draft the release.** Releases → **Draft a new release**. Type the new tag (`v0.2.0`, or `v0.2.0-rc.1` for a
   release candidate) and choose **Create new tag on publish**, target `main`. Click **Generate release notes** and edit
   them. Saving a draft doesn't create the tag or run anything.
3. **Publish it.** GitHub creates the tag, and the `release: published` event starts the workflow. GoReleaser uploads
   the archives, checksums and Linux packages into the release (keeping your notes), then pushes the cask and the
   image, and the npm job publishes. The release has no binaries for the few minutes this takes.

The same from a terminal: `gh release create v0.2.0 --target main --generate-notes` (add `--draft` to review first,
then `gh release edit v0.2.0 --draft=false`).

The workflow uses the GoReleaser version in `.tool-versions`, which mise also uses locally.

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
      `@nickytonline` scope (your npm username), with a short expiry. Turn on **Bypass two-factor
      authentication**: CI can't answer a 2FA prompt, so without it the publish fails.
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

GoReleaser uploads to the GitHub Release first, then pushes the cask and image. npm runs in a separate job after it.

- **npm job failed:** fix the cause (usually a missing or mismatched trusted publisher) and re-run just that job from
  the Actions page. The script skips packages whose version is already on npm, so a re-run finishes a partial publish.
- **GoReleaser failed:** never move, delete or re-create a published tag. Go's module proxy may already have cached it,
  and `go install` would then get the old code or a checksum error.
  - If the cause is outside the code (a token, the tap repo, a flaky runner), fix it and **re-run the failed jobs** of
    the same workflow run. It reuses the same release event; GoReleaser keeps the notes and replaces any assets the
    first attempt uploaded.
  - If the fix needs a code change, merge it and release the next patch or release candidate (`v0.2.1`,
    `v0.2.0-rc.2`). Mark the broken release as a prerelease or edit its notes to point at the fix.
