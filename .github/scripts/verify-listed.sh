#!/usr/bin/env bash
#
# Checks that a `deb-s3 list` output has a row for one exact package, version
# and architecture.
#
# The listing covers every package in the distribution, not only this one: the
# stable repository is shared with the other Mina packages. A plain grep for
# the version therefore passes on another package's row, and the dots of a
# version are regex wildcards, so `0.8.0` matches `3.3.0-8c0c2e6`. The match
# here compares whole fields as strings.
#
# `deb-s3 list` (MinaProtocol fork, lib/deb/s3/cli.rb, `def list`) prints one
# row per package: name, full version, architecture, separated by spaces and
# padded to align. None of the three fields can contain a space.
#
# Usage: deb-s3 list ... | verify-listed.sh <package> <version> <arch>
#
# Prints the rows of <package> for the log. Exit status: 0 when the exact row
# is present, 1 when it is not, 2 on a usage error.

set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: verify-listed.sh <package> <version> <arch> < listing" >&2
  exit 2
fi

awk -v p="$1" -v v="$2" -v a="$3" '
  $1 == p { print }
  $1 == p && $2 == v && $3 == a { found = 1 }
  END { exit !found }
'
