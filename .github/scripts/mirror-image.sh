#!/usr/bin/env bash
#
# Copies a multi-arch image from one registry to another, by digest, without
# rebuilding it, and checks that the copy has the same digest.
#
# The image is built once, for GHCR. Docker Hub gets the same index: the same
# platforms, the same layers and the same provenance attestations. So
# `docker pull` from either registry gives identical bytes, and a digest
# pinned from one registry is valid in the other.
#
# Usage: mirror-image.sh <source-repo>@<digest> <target-repo> <tag>...
#
#   mirror-image.sh ghcr.io/minaprotocol/mina-provision@sha256:... \
#     docker.io/minaprotocol/mina-provision 0.0.7 latest
#
# Needs: docker buildx, and a login to both registries (read for the source,
# push for the target).

set -euo pipefail

if [ "$#" -lt 3 ]; then
  echo "usage: $0 <source-repo>@<digest> <target-repo> <tag>..." >&2
  exit 2
fi

SOURCE="$1"
TARGET="$2"
shift 2

case "$SOURCE" in
  *@sha256:*) ;;
  *) echo "the source must be pinned by digest (repo@sha256:...), got $SOURCE" >&2; exit 2 ;;
esac
want="${SOURCE##*@}"

args=()
for tag in "$@"; do
  args+=(--tag "${TARGET}:${tag}")
done

echo "copying $SOURCE to ${TARGET} as: $*"
docker buildx imagetools create "${args[@]}" "$SOURCE"

status=0
for tag in "$@"; do
  got="$(docker buildx imagetools inspect "${TARGET}:${tag}" --format '{{json .Manifest}}' \
           | python3 -c 'import json,sys; print(json.load(sys.stdin)["digest"])')"
  if [ "$got" = "$want" ]; then
    echo "${TARGET}:${tag} -> $got"
  else
    echo "${TARGET}:${tag} has digest $got, but the source is $want" >&2
    status=1
  fi
done
exit "$status"
