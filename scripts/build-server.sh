#!/usr/bin/env bash
# Build the server (cmd/lovkilde) for every platform a release ships, packed one archive per
# platform with a SHA256SUMS beside them:
#
#   scripts/build-server.sh [outdir]      (default dist/)
#
# The build is release rNNNNN, the number of commits up to the one built; it says so in
# --version and the log.
#
# The server is pure Go, so every platform cross-compiles from one machine. -trimpath keeps the
# build machine's paths out of the binaries; the commit is stamped in by the go command (the
# server reports it as its build).
set -euo pipefail

cd "$(dirname "$0")/.."
out="${1:-dist}"
rm -rf "$out"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
release="r$(printf %05d "$(git rev-list --count HEAD)")"

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os="${target%/*}" arch="${target#*/}"
  name="lovkilde-$os-$arch"
  exe=lovkilde
  [ "$os" = windows ] && exe=lovkilde.exe
  mkdir -p "$work/$name"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=$release" -o "$work/$name/$exe" ./cmd/lovkilde
  if [ "$os" = windows ]; then
    (cd "$work" && zip -qr "$out/$name.zip" "$name")
  else
    tar -C "$work" -czf "$out/$name.tar.gz" "$name"
  fi
  echo "built $name"
done
(cd "$out" && sha256sum lovkilde-* > SHA256SUMS)
