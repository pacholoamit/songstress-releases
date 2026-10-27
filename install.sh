#!/bin/sh
# Songstress installer bootstrap.
#
#   curl -fsSL https://raw.githubusercontent.com/pacholoamit/songstress-releases/main/install.sh | sh
#
# Downloads the songstress CLI for your platform from this repository's
# cli-v* releases, verifies its sha256 against checksums.txt, installs it,
# and runs `songstress install`: the setup wizard, or an update of the
# install it finds. Run it again to update — each CLI release pins the
# Songstress release it was tested with. The CLI source lives in cli/ here —
# binaries are reproducible (CGO_ENABLED=0 -trimpath), so you can build and
# compare instead of trusting us.
#
# Pin a version:    SONGSTRESS_CLI_VERSION=cli-v0.1.0 sh install.sh
# Skip the setup:   sh install.sh --no-run   (just installs the binary)
# Pass flags on:    curl -fsSL …/install.sh | sh -s -- --yes --install-dir /srv/songstress
#                   (everything after `--` goes to `songstress install`)
set -eu

REPO="pacholoamit/songstress-releases"
API="https://api.github.com/repos/$REPO"

say() { printf '%s\n' "$*" >&2; }
fail() { say "error: $*"; exit 1; }

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"
if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  fail "sha256sum or shasum is required to verify the download"
fi

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
  *) fail "unsupported CPU: $arch — Songstress runs on 64-bit x86 (amd64) or ARM (arm64); on a Raspberry Pi, use a 64-bit OS" ;;
esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

TAG="${SONGSTRESS_CLI_VERSION:-}"
if [ -z "$TAG" ]; then
  # The newest cli-v* release. Most releases here are app releases, so this
  # looks through them a page at a time, newest first.
  page=1
  while [ -z "$TAG" ] && [ "$page" -le 5 ]; do
    curl -fsSL -o "$tmp/releases.json" "$API/releases?per_page=100&page=$page" ||
      fail "couldn't list the releases on GitHub (offline, or rate-limited?) — retry in a while, or name one: SONGSTRESS_CLI_VERSION=cli-vX.Y.Z"
    TAG=$(grep -o '"tag_name": *"cli-v[^"]*"' "$tmp/releases.json" | head -1 | sed 's/.*"\(cli-v[^"]*\)"/\1/' || true)
    grep -q '"tag_name"' "$tmp/releases.json" || break # past the last page
    page=$((page + 1))
  done
  [ -n "$TAG" ] || fail "no cli-v* release found — see https://github.com/$REPO/releases"
fi
say "songstress CLI $TAG ($os/$arch)"

base="https://github.com/$REPO/releases/download/$TAG"
asset="songstress_${os}_${arch}.tar.gz"

curl -fsSL -o "$tmp/$asset" "$base/$asset" || fail "download failed: $base/$asset"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "checksums download failed: $base/checksums.txt"

# Match the filename field exactly (sha256sum writes `hash  ./name` or
# `hash  name`); no regex — BSD/GNU grep BRE differences bit us here once.
want=$(awk -v a="$asset" '$2 == a || $2 == "./" a { print $1; exit }' "$tmp/checksums.txt")
[ -n "$want" ] || fail "no checksum recorded for $asset"
got=$(sha256 "$tmp/$asset")
[ "$want" = "$got" ] || fail "checksum mismatch for $asset (expected $want, got $got) — not installing it"
say "checksum verified"

tar -C "$tmp" -xzf "$tmp/$asset" songstress

dest="$HOME/.local/bin"
case ":${PATH}:" in
  *:"$HOME/.local/bin":*) ;;
  *) if [ -w /usr/local/bin ]; then dest=/usr/local/bin; fi ;;
esac
mkdir -p "$dest"
install -m 0755 "$tmp/songstress" "$dest/songstress"
say "installed $dest/songstress — $("$dest/songstress" version 2>/dev/null || echo "$TAG")"

# How to run it from now on: by name when that finds this copy.
run="$dest/songstress"
case ":${PATH}:" in
  *:"$dest":*)
    found=$(command -v songstress 2>/dev/null || true)
    if [ "$found" = "$dest/songstress" ]; then
      run=songstress
    else
      say "note: $found comes first on your PATH, so \`songstress\` runs that one — remove it, or run $dest/songstress"
    fi ;;
  *)
    say "note: $dest isn't on your PATH; to run \`songstress\` by name, add this line to your shell's startup file (~/.profile, ~/.bashrc or ~/.zshrc):"
    say "  export PATH=\"$dest:\$PATH\"" ;;
esac

if [ "${1:-}" = "--no-run" ]; then
  say "run '$run install' when ready"
  exit 0
fi

# Piped stdin can't drive the wizard: reattach to the terminal when there is
# one. Without one (CI, cron) only a scripted `--yes` install can run, and the
# CLI says so otherwise.
if [ -t 0 ]; then
  exec "$dest/songstress" install "$@"
elif (: </dev/tty) 2>/dev/null; then
  exec "$dest/songstress" install "$@" </dev/tty
else
  exec "$dest/songstress" install "$@" </dev/null
fi
