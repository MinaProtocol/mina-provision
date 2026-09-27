#!/usr/bin/env bash
#
# Fails unless a container image and a .deb report the same version.
#
# An operator must see the same `mina-provision --version` whichever way the
# tool was installed. The image and the package are built by different
# workflows, and the published 0.0.3 image reported `v0.0.3` while the 0.0.3
# package reported `0.0.3`.
#
# Three values must agree:
#   - the Version field of the package control file
#   - `--version` of the binary inside the package
#   - `--version` of the image
#
# The binary is taken out of the package and run directly, so the package does
# not have to be installed. The package must be for the architecture of the
# host, and the image must be available to `docker run` on the host.
#
# Usage: check-version-match.sh <image> <deb>

set -euo pipefail

IMAGE="${1:?image reference required}"
DEB="${2:?path to the .deb required}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

deb_field="$(dpkg-deb --field "$DEB" Version)"

dpkg-deb --fsys-tarfile "$DEB" | tar -xO ./usr/bin/mina-provision > "$tmp/mina-provision"
chmod +x "$tmp/mina-provision"
deb_output="$("$tmp/mina-provision" --version)"

image_output="$(docker run --rm "$IMAGE" --version)"

echo "package Version field: $deb_field"
echo "package binary:        $deb_output"
echo "image:                 $image_output"

# cobra prints "<name> version <v>". The last word is the version itself.
deb_version="${deb_output##* }"
image_version="${image_output##* }"

status=0
if [ "$deb_version" != "$deb_field" ]; then
  echo "the binary in $DEB reports $deb_version, but the package is version $deb_field" >&2
  status=1
fi
if [ "$image_version" != "$deb_field" ]; then
  echo "$IMAGE reports $image_version, but the package is version $deb_field" >&2
  status=1
fi
exit "$status"
