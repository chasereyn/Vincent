#!/usr/bin/env bash
# herdr `[[build]]` step for the Vincent plugin: put a `vincent` binary at
# <plugin root>/bin/vincent.
#
# Fast path: download the release asset for this platform whose tag matches
# the manifest's `version`, verify it against the release's checksums.txt,
# install it. Fallback: if the download or the checksum fails and `go` is on
# PATH, build from this checkout. Otherwise fail with one sentence; herdr
# shows it in the install output.
#
# Build commands run with the checkout as the working directory and may not
# receive the runtime env, so the plugin root is taken from this script's own
# location rather than from HERDR_PLUGIN_ROOT.
set -euo pipefail

NAME="vincent"
REPO="chasereyn/vincent"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN_DIR="$ROOT/bin"
VERSION="$(grep -m1 '^version' "$ROOT/herdr-plugin.toml" | sed -E 's/.*"([^"]+)".*/\1/')"
TAG="v${VERSION}"

case "$(uname -s)-$(uname -m)" in
  Darwin-arm64)                 asset="vincent-darwin-arm64" ;;
  Darwin-x86_64)                asset="vincent-darwin-amd64" ;;
  Linux-x86_64)                 asset="vincent-linux-amd64" ;;
  Linux-aarch64 | Linux-arm64)  asset="vincent-linux-arm64" ;;
  *)                            asset="" ;;
esac

build_from_source() {
  if ! command -v go >/dev/null 2>&1; then
    echo "$NAME: $1, and go is not on PATH to build from source" >&2
    exit 1
  fi
  echo "$NAME: $1; building from source with go"
  mkdir -p "$BIN_DIR"
  (cd "$ROOT" && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$BIN_DIR/$NAME" .)
  echo "$NAME: built $BIN_DIR/$NAME"
  exit 0
}

if [ -z "$asset" ]; then
  build_from_source "no release binary for $(uname -s)-$(uname -m)"
fi

base="https://github.com/${REPO}/releases/download/${TAG}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# Release downloads can 404 for a minute or two after a release publishes,
# so retry on every error, 404 included.
dl() { curl -fsSL --retry 5 --retry-delay 3 --retry-all-errors "$1" -o "$2"; }

echo "$NAME: downloading $asset ($TAG)"
if ! dl "$base/$asset" "$tmp/$asset" || ! dl "$base/checksums.txt" "$tmp/checksums.txt"; then
  build_from_source "download of $asset at $TAG failed"
fi

expected="$(awk -v a="$asset" '$2 == a {print $1}' "$tmp/checksums.txt")"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$asset" | awk '{print $1}')"
else
  actual="$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')"
fi
if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
  echo "$NAME: checksum mismatch for $asset (expected '${expected:-none listed}', got $actual)" >&2
  exit 1
fi

mkdir -p "$BIN_DIR"
install -m 0755 "$tmp/$asset" "$BIN_DIR/$NAME"
echo "$NAME: installed $BIN_DIR/$NAME ($("$BIN_DIR/$NAME" --version))"
