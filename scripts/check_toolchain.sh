#!/usr/bin/env bash
# The go line of go.mod names the one toolchain that builds TrailSource: the
# release archives (GoReleaser runs this script first), the container image
# (Dockerfile build stage) and CI. Fails when the go in use or the
# Dockerfile names another version, or when a binary given as argument was
# built with another one.
#
#   scripts/check_toolchain.sh [binary...]
set -euo pipefail
cd "$(dirname "$0")/.."
want="go$(awk '$1 == "go" { print $2; exit }' go.mod)"
[[ $want =~ ^go1\.[0-9]+\.[0-9]+$ ]] || { echo "check_toolchain: go.mod must name a full toolchain version (go 1.x.y), got '${want#go}'" >&2; exit 1; }
got="$(go env GOVERSION)"
[[ $got == "$want" ]] || { echo "check_toolchain: go is $got, go.mod names $want" >&2; exit 1; }
image="$(awk '/^FROM .*golang:/ { sub(/.*golang:/, ""); sub(/ .*/, ""); print; exit }' Dockerfile)"
[[ $image == "${want#go}-alpine"* ]] || { echo "check_toolchain: Dockerfile builds with golang:$image, go.mod names $want" >&2; exit 1; }
for bin in "$@"; do
  v="$(go version "$bin" | awk '{ print $2 }')"
  [[ $v == "$want" ]] || { echo "check_toolchain: $bin was built with $v, go.mod names $want" >&2; exit 1; }
done
echo "toolchain $want: go, Dockerfile (golang:$image)${*:+ and $# binaries} agree"
