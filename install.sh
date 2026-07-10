#!/bin/sh
# Songstress installer bootstrap.
#
#   curl -fsSL https://raw.githubusercontent.com/pacholoamit/songstress-releases/main/install.sh | sh
#
# Downloads the songstress CLI for your platform from this repository's
# cli-v* releases, verifies its sha256 against checksums.txt, installs it,
# and launches the interactive setup. The CLI source lives in cli/ here —
# binaries are reproducible (CGO_ENABLED=0 -trimpath), so you can build and
# compare instead of trusting us.
#
# Pin a version:   SONGSTRESS_CLI_VERSION=cli-v0.1.0 sh install.sh
# Skip the wizard: sh install.sh --no-run   (just installs the binary)
set -eu

REPO="pacholoamit/songstress-releases"
API="https://api.github.com/repos/$REPO"

say() { printf '%s\n' "$*" >&2; }
fail() { say "error: $*"; exit 1; }

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"

os=$(uname -s)
case "$os" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "unsupported OS: $os (Linux and macOS are supported; on Windows use WSL2)" ;;
esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) fail "unsupported architecture: $arch" ;;
esac

TAG="${SONGSTRESS_CLI_VERSION:-}"
if [ -z "$TAG" ]; then
  # Newest cli-v* release (releases here also include app releases — filter).
  TAG=$(curl -fsSL "$API/releases?per_page=30" \
    | grep -o '"tag_name": *"cli-v[^"]*"' \
    | head -1 \
    | sed 's/.*"\(cli-v[^"]*\)"/\1/')
  [ -n "$TAG" ] || fail "no cli-v* release found — see https://github.com/$REPO/releases"
fi
say "songstress CLI $TAG ($os/$arch)"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
base="https://github.com/$REPO/releases/download/$TAG"
asset="songstress_${os}_${arch}.tar.gz"

curl -fsSL -o "$tmp/$asset" "$base/$asset" || fail "download failed: $base/$asset"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "checksums download failed"

# Match the filename field exactly (sha256sum writes `hash  ./name` or
# `hash  name`); no regex — BSD/GNU grep BRE differences bit us here once.
want=$(awk -v a="$asset" '$2 == a || $2 == "./" a { print $1; exit }' "$tmp/checksums.txt")
[ -n "$want" ] || fail "no checksum recorded for $asset"
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$asset" | cut -d' ' -f1)
else
  got=$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)
fi
[ "$want" = "$got" ] || fail "checksum mismatch for $asset (expected $want, got $got)"
say "checksum verified"

tar -C "$tmp" -xzf "$tmp/$asset" songstress

dest="$HOME/.local/bin"
case ":${PATH}:" in
  *:"$HOME/.local/bin":*) ;;
  *) if [ -w /usr/local/bin ]; then dest=/usr/local/bin; fi ;;
esac
mkdir -p "$dest"
install -m 0755 "$tmp/songstress" "$dest/songstress"
say "installed $dest/songstress"
case ":${PATH}:" in
  *:"$dest":*) ;;
  *) say "note: add $dest to your PATH" ;;
esac

if [ "${1:-}" = "--no-run" ]; then
  say "run 'songstress install' when ready"
  exit 0
fi

# Piped stdin can't drive the wizard — reattach to the terminal when present.
if [ -t 0 ]; then
  exec "$dest/songstress" install
elif [ -e /dev/tty ]; then
  exec "$dest/songstress" install < /dev/tty
else
  say "no TTY available — run 'songstress install' (or 'songstress install --yes …') yourself"
fi
