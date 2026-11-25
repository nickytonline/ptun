#!/bin/sh
# Installs the ptun binary from GitHub Releases.
#
#   curl -fsSL https://raw.githubusercontent.com/nickytonline/ptun/main/install.sh | sh
#
# Settings (environment variables):
#   PTUN_VERSION       version to install, e.g. 0.2.0 or v0.2.0 (default: the latest release)
#   PTUN_INSTALL_DIR   where to put ptun (default: ~/.local/bin)
set -eu

repo="nickytonline/ptun"
version="${PTUN_VERSION:-latest}"
install_dir="${PTUN_INSTALL_DIR:-$HOME/.local/bin}"

case "$version" in
  latest | v*) ;;
  *) version="v$version" ;;
esac

err() {
  echo "ptun install: $*" >&2
  exit 1
}

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) err "unsupported OS $(uname -s). On Windows, download the zip from https://github.com/$repo/releases" ;;
esac

case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) err "unsupported CPU $(uname -m)" ;;
esac

if [ "$version" = latest ]; then
  base="https://github.com/$repo/releases/latest/download"
else
  base="https://github.com/$repo/releases/download/$version"
fi

archive="ptun_${os}_${arch}.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading $archive ($version)"
if ! curl -fsSL "$base/$archive" -o "$tmp/$archive"; then
  [ "$version" = latest ] && err "download failed: $base/$archive"
  err "download failed: $base/$archive. Is $version a release? See https://github.com/$repo/releases"
fi
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || err "download failed: $base/checksums.txt"

expected=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$expected" ] || err "$archive is not in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
else
  actual=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
fi
[ "$expected" = "$actual" ] || err "checksum mismatch for $archive"

tar -xzf "$tmp/$archive" -C "$tmp" ptun
mkdir -p "$install_dir"
mv "$tmp/ptun" "$install_dir/ptun"
chmod 755 "$install_dir/ptun"

if installed=$("$install_dir/ptun" --version 2>&1); then
  echo "Installed $installed to $install_dir/ptun"
else
  echo "Installed ptun to $install_dir/ptun, but it didn't run: $installed" >&2
  echo "Check that $install_dir allows running programs (not mounted noexec) and that this is a $os/$arch machine." >&2
fi
case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) echo "Add $install_dir to your PATH to run ptun." ;;
esac
command -v ssh >/dev/null 2>&1 || echo "ptun needs ssh on your PATH. Install an OpenSSH client."
