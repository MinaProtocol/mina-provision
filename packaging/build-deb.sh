#!/usr/bin/env bash
#
# Builds the mina-provision Debian package around an already-built binary.
#
# Usage: packaging/build-deb.sh <binary> <version> <architecture> <outdir>
#
# The package is a single binary in /usr/bin with no configuration, so it
# needs no maintainer scripts. postgresql-client is a Recommends rather than a
# Depends: only the `archive` command shells out to psql, and the `blocks` and
# `config` commands are useful without it.

set -euo pipefail

BINARY="${1:?binary path required}"
VERSION="${2:?version required}"
ARCH="${3:?architecture required}"
OUTDIR="${4:-dist}"

BUILDDIR="$(mktemp -d)"
trap 'rm -rf "$BUILDDIR"' EXIT

# mktemp creates the directory 0700, which would make the package's own root
# directory unreadable for anyone but root once installed.
chmod 0755 "$BUILDDIR"

mkdir -p "$BUILDDIR/usr/bin" "$BUILDDIR/DEBIAN" "$OUTDIR"
install -m 0755 "$BINARY" "$BUILDDIR/usr/bin/mina-provision"

cat > "$BUILDDIR/DEBIAN/control" <<CONTROL
Package: mina-provision
Version: ${VERSION}
Section: utils
Priority: optional
Architecture: ${ARCH}
Maintainer: O(1)Labs <build@o1labs.org>
Recommends: postgresql-client
Homepage: https://github.com/MinaProtocol/mina-provision
Description: Fetch and place the published artifacts a Mina node needs
 mina-provision obtains the artifacts a Mina daemon, archive node, block
 producer or Rosetta stack needs before it can start: archive database
 dumps, precomputed blocks and runtime configuration files. It verifies
 what it downloads against the checksum the publisher states.
CONTROL

# -Zgzip keeps the package installable on older distributions, which is the
# same reason the mina repository forces it.
#
# --root-owner-group records root:root for every file. Without it dpkg-deb
# records the ownership of whoever ran the build, and dpkg applies that on
# install: a package built by a CI runner would put a binary in /usr/bin owned
# by the runner's numeric uid. On a host where that uid belongs to an
# unprivileged user, that user could replace the binary that others run under
# sudo.
dpkg-deb --root-owner-group -Zgzip --build "$BUILDDIR" \
  "$OUTDIR/mina-provision_${VERSION}_${ARCH}.deb"

echo "$OUTDIR/mina-provision_${VERSION}_${ARCH}.deb"
